package talkie

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/androiddrew/talkie-core/internal/audio"
	"github.com/androiddrew/talkie-core/internal/calibrate"
	"github.com/androiddrew/talkie-core/internal/device"
)

// period is the audio per device callback.
const period = 20 * time.Millisecond

// block is one device period, converted for the engine: the microphone and
// what the speaker played, both at 16 kHz, and the player's notifications.
type block struct {
	mic, ref []float32
	played   []audio.Event
}

// Ready chime settings.
const (
	// chimeWait is how long Run waits for the device to deliver the chime's
	// echo before giving up on it.
	chimeWait = 2 * time.Second
	// maxSessionShift is how far this session's latency may be from the
	// trained range before the profile's echo canceller, trained at that
	// latency, is left out.
	maxSessionShift = 25 * time.Millisecond
)

// readyChime is training's chirp after the same lead of silence: it tells
// the user talkie is listening, and measures as training does. A narrower
// 500 Hz to 2 kHz sweep measured 5-10 ms long on a desktop's speakers, and
// barely above minCorrelation.
func readyChime(rate int) []float32 {
	lead := make([]float32, int(chirpLead.Seconds()*float64(rate)))
	return append(lead, calibrate.Chirp(rate, chirpLength, chirpLevel)...)
}

// pump returns the device callback. On the audio thread it fills the speaker
// from the player, plus chime at the very start, resamples the microphone and the played audio to 16 kHz
// through identical resamplers, so they stay aligned, and hands them to the
// owner goroutine. It blocks rather than drop a block, whose player
// notifications the engine needs; the queue holds seconds of audio, so it
// only blocks if the owner goroutine stalls.
func pump(player *audio.Player, rate int, blocks chan<- block, chime []float32) device.Callback {
	micRS, refRS := audio.NewResampler(rate, audio.SampleRate), audio.NewResampler(rate, audio.SampleRate)
	return func(in, out []float32) {
		played := player.Read(out)
		for i := range out {
			if len(chime) == 0 {
				break
			}
			out[i] += chime[0]
			chime = chime[1:]
		}
		mic := make([]float32, len(in))
		for i, s := range in {
			if !math.IsNaN(float64(s)) { // NaN would poison the resampler
				mic[i] = s
			}
		}
		// Copy what was played: the device reuses out, and at 16 kHz the
		// resampler passes its input through.
		ref := append([]float32(nil), out...)
		blocks <- block{mic: clip(micRS.Process(mic)), ref: clip(refRS.Process(ref)), played: played}
	}
}

// clip limits samples to [-1, 1] in place. Devices can deliver samples past
// full scale, and the resampler's filter overshoots near it; the VAD and the
// echo canceller reject anything outside.
func clip(samples []float32) []float32 {
	for i, s := range samples {
		samples[i] = max(-1, min(1, s))
	}
	return samples
}

// run is the owner goroutine: it feeds device blocks to the conversation
// and runs work posted by transcriptions and turns, until ctx is done.
func (c *conversation) run(ctx context.Context, blocks <-chan block) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case b := <-blocks:
			if err := c.audio(b.mic, b.ref, b.played); err != nil {
				return err
			}
		case <-c.wake:
			c.drain()
		}
	}
}

// Run talks with the user through the audio devices until ctx is done. It
// opens Config.InputDevice and Config.OutputDevice, or the system defaults,
// at Config.OutputRate. Unless Config.DisableReadyChime, it starts with a
// short chime, from whose echo it measures this session's audio latency.
func (t *Talkie) Run(ctx context.Context) error {
	if t.running {
		return errors.New("talkie: Run is already running")
	}
	t.running = true
	defer func() { t.running = false }()
	blocks := make(chan block, 256) // about 5 s of 20 ms periods
	var chime []float32
	if !t.cfg.DisableReadyChime {
		chime = readyChime(t.player.Rate())
	}
	dev, err := t.open(device.Config{
		Rate: t.player.Rate(), Period: period, Input: t.cfg.InputDevice, Output: t.cfg.OutputDevice,
	}, pump(t.player, t.player.Rate(), blocks, chime))
	if err != nil {
		return err
	}
	if err := dev.Start(); err != nil {
		return errors.Join(err, dev.Close())
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The blocks heard while measuring go to the conversation afterwards,
	// so the echo canceller learns from the chime too.
	var early []block
	var session time.Duration
	if chime != nil {
		early, session = t.measureChime(ctx, blocks)
	}
	input, output := dev.Names()
	t.useProfile(input, output, session)
	done := make(chan error, 1)
	go func() {
		for _, b := range early {
			if err := t.conv.audio(b.mic, b.ref, b.played); err != nil {
				done <- err
				return
			}
		}
		done <- t.conv.run(ctx, blocks)
	}()
	err = <-done
	// The device may be in a callback that is sending: keep draining until
	// it is closed, so the callback can't block forever.
	closed := make(chan struct{})
	go func() {
		for {
			select {
			case <-blocks:
			case <-closed:
				return
			}
		}
	}()
	err = errors.Join(err, dev.Close())
	close(closed)
	return err
}

// measureChime collects the blocks that carry the ready chime and its echo
// and returns them with the latency measured from them: zero when the
// microphone didn't hear the chime, as with a headset, and the latency is
// left to the profile and the first reply.
func (t *Talkie) measureChime(ctx context.Context, blocks <-chan block) ([]block, time.Duration) {
	lead := int(chirpLead.Seconds() * audio.SampleRate)
	length := int(chirpLength.Seconds() * audio.SampleRate)
	want := lead + length + int((maxEchoDelay+period).Seconds()*audio.SampleRate)
	var early []block
	var mic, ref []float32
	timeout := time.After(chimeWait)
	for len(mic) < want {
		select {
		case <-ctx.Done():
			return early, 0
		case <-timeout:
			t.logInfo("the ready chime's echo did not arrive in time: measuring the latency from the first reply")
			return early, 0
		case b := <-blocks:
			early = append(early, b)
			mic, ref = append(mic, b.mic...), append(ref, b.ref...)
		}
	}
	e := calibrate.Measure(ref, mic, lead, length, audio.SampleRate, maxEchoDelay)
	if !e.Heard {
		t.logInfo("the microphone did not hear the ready chime, as with a headset: measuring the latency from the first reply", "correlation", e.Correlation)
		return early, 0
	}
	t.logInfo("ready chime", "latency", e.Delay, "echo_gain_db", math.Round(e.GainDB*10)/10)
	t.publish(Event{Type: EventLatency, Latency: e.Delay, LatencySource: LatencyFromChime, EchoGainDB: e.GainDB})
	return early, e.Delay
}

func (t *Talkie) logInfo(msg string, args ...any) {
	if t.cfg.Logger != nil {
		t.cfg.Logger.Info(msg, args...)
	}
}

// Listen opens a listening window as push-to-talk does: the user can speak
// without the wake word. It takes effect on Run's goroutine.
func (t *Talkie) Listen() {
	t.conv.post(func() { _ = t.conv.eng.Listen(0, 0) })
}

// Interrupt stops the agent, whether it is talking or thinking, and listens
// to the user without the wake word, whatever Config.BargeIn is set to: for
// a key or a button. When the agent isn't talking or thinking it listens, as
// Listen does. It takes effect on Run's goroutine.
func (t *Talkie) Interrupt() {
	t.conv.post(t.conv.eng.Interrupt)
}

// EndSession ends the conversation in progress, if any.
func (t *Talkie) EndSession() {
	t.conv.post(func() { t.conv.eng.EndSession("client") })
}

// useProfile tunes the conversation with this session's latency, measured
// from the ready chime (zero if not), and the room profile of the devices
// in use, if there is one. The latency comes from Config.AudioLatency if
// set, else the session, else the profile. The profile's echo canceller is
// left out when the session's latency is far from the trained one, as some
// device opens are; it would start misaligned. It runs before the
// conversation starts.
func (t *Talkie) useProfile(input, output string, session time.Duration) {
	if t.cfg.AudioLatency == 0 && session > 0 {
		t.conv.eng.SetIOLatency(session)
	}
	if t.cfg.IgnoreRoomProfile {
		return
	}
	log := t.cfg.Logger
	dir := t.cfg.RoomProfileDir
	if dir == "" {
		var err error
		if dir, err = DefaultProfileDir(); err != nil {
			return
		}
	}
	p, err := LoadProfile(ProfilePath(dir, input, output))
	if err != nil {
		if log != nil {
			if errors.Is(err, ErrNoProfile) {
				log.Info("no room profile for these devices: run room training to tune talkie to the room", "input", input, "output", output)
			} else {
				log.Warn("room profile not used", "err", err)
			}
		}
		return
	}
	if t.cfg.AudioLatency == 0 && session == 0 && len(p.Latency.Runs) > 0 {
		t.conv.eng.SetIOLatency(p.Latency.Assumed())
	}
	t.drift = newDrift(p.Latency)
	shifted := false
	if session > 0 && len(p.Latency.Runs) > 0 {
		// Judge the room against this session's latency, so a slow device
		// open isn't taken for a changed room.
		t.drift = newDrift(LatencyProfile{Runs: []time.Duration{session}, Min: session, Max: session})
		shifted = session < p.Latency.Min-maxSessionShift || session > p.Latency.Max+maxSessionShift
		if shifted && log != nil {
			log.Info("this session's audio latency differs from the room profile's, as some device opens do: the echo canceller learns the room again",
				"latency", session, "trained_min", p.Latency.Min, "trained_max", p.Latency.Max)
		}
	}
	t.wakeThreshold = 0
	if w := p.Wake; w != nil && t.cfg.WakeWord != "" {
		switch {
		case !sameWakeWord(w.Keyword, t.cfg.WakeWord):
			if log != nil {
				log.Info("the room profile's wake word training is for another wake word: train again to tune this one", "trained", w.Keyword, "wake_word", t.cfg.WakeWord)
			}
		case w.Threshold > 0:
			t.wakeThreshold = w.Threshold
		}
	}
	restored := false
	if p.Canceller != nil && !t.cfg.DisableEchoCancellation && !shifted {
		if err := t.conv.eng.RestoreEcho(p.Canceller.Snapshot); err != nil {
			if log != nil {
				log.Warn("room profile's echo canceller not used: train again", "err", err)
			}
		} else {
			restored = true
		}
	}
	if log != nil {
		log.Info("using room profile", "trained", p.Trained.Format(time.DateOnly), "latency", t.conv.eng.IOLatency(), "echo_gain_db", p.Echo.GainDB,
			"echo_canceller_restored", restored, "wake_threshold", t.wakeThreshold)
		if p.Echo.Advice != "" {
			log.Warn(p.Echo.Advice)
		}
		if p.Wake != nil && p.Wake.Advice != "" {
			log.Warn(p.Wake.Advice)
		}
	}
}
