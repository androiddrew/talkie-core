package talkie

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/androiddrew/talkie-core/internal/audio"
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

// pump returns the device callback. On the audio thread it fills the speaker
// from the player, resamples the microphone and the played audio to 16 kHz
// through identical resamplers, so they stay aligned, and hands them to the
// owner goroutine. It blocks rather than drop a block, whose player
// notifications the engine needs; the queue holds seconds of audio, so it
// only blocks if the owner goroutine stalls.
func pump(player *audio.Player, rate int, blocks chan<- block) device.Callback {
	micRS, refRS := audio.NewResampler(rate, audio.SampleRate), audio.NewResampler(rate, audio.SampleRate)
	return func(in, out []float32) {
		played := player.Read(out)
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
// at Config.OutputRate.
func (t *Talkie) Run(ctx context.Context) error {
	if t.running {
		return errors.New("talkie: Run is already running")
	}
	t.running = true
	defer func() { t.running = false }()
	blocks := make(chan block, 256) // about 5 s of 20 ms periods
	dev, err := t.open(device.Config{
		Rate: t.player.Rate(), Period: period, Input: t.cfg.InputDevice, Output: t.cfg.OutputDevice,
	}, pump(t.player, t.player.Rate(), blocks))
	if err != nil {
		return err
	}
	t.useProfile(dev.Names())
	if err := dev.Start(); err != nil {
		return errors.Join(err, dev.Close())
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- t.conv.run(ctx, blocks) }()
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

// Listen opens a listening window as push-to-talk does: the user can speak
// without the wake word. It takes effect on Run's goroutine.
func (t *Talkie) Listen() {
	t.conv.post(func() { _ = t.conv.eng.Listen(0, 0) })
}

// EndSession ends the conversation in progress, if any.
func (t *Talkie) EndSession() {
	t.conv.post(func() { t.conv.eng.EndSession("client") })
}

// useProfile tunes the conversation with the room profile of the devices
// in use, if there is one: the latency, unless Config.AudioLatency sets
// it, and the profile's advice, logged. It runs before the conversation
// starts.
func (t *Talkie) useProfile(input, output string) {
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
	if t.cfg.AudioLatency == 0 && len(p.Latency.Runs) > 0 {
		t.conv.eng.SetIOLatency(p.Latency.Assumed())
	}
	if log != nil {
		log.Info("using room profile", "trained", p.Trained.Format(time.DateOnly), "latency", t.conv.eng.IOLatency(), "echo_gain_db", p.Echo.GainDB)
		if p.Echo.Advice != "" {
			log.Warn(p.Echo.Advice)
		}
	}
}
