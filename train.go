package talkie

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/androiddrew/talkie-core/internal/audio"
	"github.com/androiddrew/talkie-core/internal/calibrate"
	"github.com/androiddrew/talkie-core/internal/device"
	"github.com/androiddrew/talkie-core/internal/engine"
)

// Step is one stage of room training, for the application to present.
type Step struct {
	Name        string // "noise", "echo", "canceller", "wake", "speech"
	Instruction string // what to tell the user
}

// Prompt shows a step's instruction and returns when the user is ready, or
// with an error to stop training. A terminal application prints and waits
// for Enter; a graphical one shows a dialog.
type Prompt func(ctx context.Context, step Step) error

// Training settings.
const (
	trainChirps   = 5                      // each on a fresh device open
	chirpLength   = 300 * time.Millisecond // 300 Hz to 3 kHz
	chirpLevel    = 0.1                    // -20 dBFS: quiet, well clear of clipping
	chirpLead     = 500 * time.Millisecond // silence before it, while the device settles
	chirpTail     = 700 * time.Millisecond // room for the longest delay plus reverb
	maxEchoDelay  = 500 * time.Millisecond
	noiseDuration = 3 * time.Second
)

// Train measures the room through cfg's devices: the background noise, then
// the speaker-to-microphone delay and level from quiet chirps. When
// cfg.Kokoro names the Kokoro files (with cfg.ORTLibrary), it then speaks a
// short paragraph while the echo canceller learns the room, so Run can start
// with it learned. When cfg.WakeWord is set (with cfg.ORTLibrary), the user
// then says the wake word a few times and reads a sentence, to tune the wake
// word's threshold to their voice. Save the result with SaveProfile at
// ProfilePath(dir, p.Input, p.Output) for Run to find it.
func Train(ctx context.Context, cfg Config, prompt Prompt) (Profile, error) {
	var speak speech
	if cfg.Kokoro.ModelPath != "" {
		speak = func(rate int) ([]float32, error) { return synthesize(ctx, cfg, trainingSpeech, rate) }
	}
	var scorer *wakeScorer
	if cfg.WakeWord != "" && cfg.ORTLibrary != "" {
		var err error
		if scorer, err = newWakeScorer(ctx, cfg); err != nil {
			return Profile{}, fmt.Errorf("talkie: load the wake word models: %w", err)
		}
		defer scorer.close()
	}
	return train(ctx, cfg, prompt, openDevice, speak, scorer)
}

// speech returns audio to play at rate while the echo canceller learns.
type speech func(rate int) ([]float32, error)

// trainingSpeech is about ten seconds of speech, enough for the echo
// canceller to learn the room across the voice's whole band.
const trainingSpeech = "Hello! I'm learning how this room sounds, so that I can still hear you while I'm talking. " +
	"Please stay quiet for a few more seconds. Listening closely to an echo is how I learn to ignore it. Thank you, that's all."

// synthesize speaks text with the configured Kokoro voice at rate.
func synthesize(ctx context.Context, cfg Config, words string, rate int) ([]float32, error) {
	tts, err := loadKokoro(cfg)
	if err != nil {
		return nil, fmt.Errorf("talkie: load Kokoro: %w", err)
	}
	defer tts.Close()
	var out []float32
	err = newKokoroVoice(tts, cfg.Kokoro, rate).Speak(ctx, words, func(samples []float32) error {
		out = append(out, samples...)
		return nil
	})
	return out, err
}

// duplex is an open audio device; device.Duplex is one, and tests supply
// simulated ones.
type duplex interface {
	Start() error
	Close() error
	Names() (input, output string)
}

type opener func(device.Config, device.Callback) (duplex, error)

func openDevice(c device.Config, cb device.Callback) (duplex, error) {
	d, err := device.Open(c, cb)
	if err != nil {
		return nil, err
	}
	return d, nil
}

func train(ctx context.Context, cfg Config, prompt Prompt, open opener, speak speech, scorer *wakeScorer) (Profile, error) {
	if prompt == nil {
		return Profile{}, errors.New("talkie: Train needs a prompt")
	}
	rate := cfg.OutputRate
	if rate == 0 {
		rate = 48000
	}
	dc := device.Config{Rate: rate, Period: period, Input: cfg.InputDevice, Output: cfg.OutputDevice}
	p := Profile{Trained: time.Now().UTC(), Rate: rate}

	if err := prompt(ctx, Step{Name: "noise", Instruction: "Please be quiet for a few seconds while talkie listens to the room."}); err != nil {
		return Profile{}, err
	}
	quiet, _, input, output, err := record(ctx, open, dc, nil, int(noiseDuration.Seconds()*float64(rate)))
	if err != nil {
		return Profile{}, err
	}
	p.Input, p.Output = input, output
	p.NoiseDBFS = calibrate.LevelDBFS(quiet[len(quiet)/6:]) // skip the device settling

	if err := prompt(ctx, Step{Name: "echo", Instruction: fmt.Sprintf("talkie will play %d short, quiet chirps to measure the speaker. Please stay quiet.", trainChirps)}); err != nil {
		return Profile{}, err
	}
	chirp := calibrate.Chirp(rate, chirpLength, chirpLevel)
	lead := int(chirpLead.Seconds() * float64(rate))
	play := make([]float32, lead+len(chirp)+int(chirpTail.Seconds()*float64(rate)))
	copy(play[lead:], chirp)
	var gains []float64
	for range trainChirps {
		heard, played, _, _, err := record(ctx, open, dc, play, len(play))
		if err != nil {
			return Profile{}, err
		}
		e := calibrate.Measure(played, heard, lead, len(chirp), rate, maxEchoDelay)
		if calibrate.Peak(heard) >= 0.99 {
			p.Echo.Clipped = true
		}
		if !e.Heard {
			continue
		}
		p.Latency.Runs = append(p.Latency.Runs, e.Delay)
		gains = append(gains, e.GainDB)
	}
	// Most runs must hear the chirp to trust it.
	if len(p.Latency.Runs) > trainChirps/2 {
		runs := slices.Sorted(slices.Values(p.Latency.Runs))
		p.Latency.Min, p.Latency.Median, p.Latency.Max = runs[0], runs[len(runs)/2], runs[len(runs)-1]
		slices.Sort(gains)
		p.Echo.Heard, p.Echo.GainDB = true, gains[len(gains)/2]
	} else {
		p.Latency = LatencyProfile{}
	}
	p.Echo.adviseEcho()

	if speak != nil && p.Echo.Heard {
		if err := prompt(ctx, Step{Name: "canceller", Instruction: "talkie will now speak for about ten seconds while it learns its own echo. Please stay quiet."}); err != nil {
			return Profile{}, err
		}
		words, err := speak(rate)
		if err != nil {
			return Profile{}, err
		}
		play := append(words, make([]float32, rate)...) // and the echo's tail
		heard, played, _, _, err := record(ctx, open, dc, play, len(play))
		if err != nil {
			return Profile{}, err
		}
		if p.Canceller, err = learnEcho(heard, played, rate); err != nil {
			return Profile{}, err
		}
	}

	if scorer != nil {
		var err error
		if p.Wake, err = trainWake(ctx, prompt, open, dc, scorer); err != nil {
			return Profile{}, err
		}
	}
	return p, nil
}

// learnEcho runs talkie's echo canceller over a recording of its own
// speech, aligned and resampled exactly as Run feeds the engine, and
// returns what it learned.
func learnEcho(heard, played []float32, rate int) (*CancellerProfile, error) {
	c, err := engine.NewCanceller(engine.Defaults().Echo)
	if err != nil {
		return nil, err
	}
	mic := clip(audio.NewResampler(rate, audio.SampleRate).Process(slices.Clone(heard)))
	ref := clip(audio.NewResampler(rate, audio.SampleRate).Process(slices.Clone(played)))
	frame := engine.EchoFrame
	out := make([]float32, frame)
	frames := min(len(mic), len(ref)) / frame
	var in, rest float64
	for f := range frames {
		m, r := mic[f*frame:(f+1)*frame], ref[f*frame:(f+1)*frame]
		if err := c.Cancel(m, r, out); err != nil {
			return nil, err
		}
		if f >= frames*2/3 { // how well it learned: the last third
			in += energy(m)
			rest += energy(out)
		}
	}
	var erle float64
	if in > 0 && rest > 0 {
		erle = 10 * math.Log10(in/rest)
	}
	return &CancellerProfile{Snapshot: c.Snapshot(), ERLEDB: erle}, nil
}

func energy(x []float32) float64 {
	var e float64
	for _, v := range x {
		e += float64(v) * float64(v)
	}
	return e
}

// record opens the devices, plays play (silence when nil) and records the
// microphone until n samples have arrived, then closes them. It returns
// what was heard and what was played, aligned, and the device names.
func record(ctx context.Context, open opener, c device.Config, play []float32, n int) (heard, played []float32, input, output string, err error) {
	var mu sync.Mutex
	done := make(chan struct{})
	finished := false
	cb := func(in, out []float32) {
		mu.Lock()
		defer mu.Unlock()
		clear(out)
		if finished {
			return
		}
		if len(played) < len(play) {
			copy(out, play[len(played):])
		}
		heard = append(heard, in...)
		played = append(played, out...)
		if len(heard) >= n {
			finished = true
			close(done)
		}
	}
	d, err := open(c, cb)
	if err != nil {
		return nil, nil, "", "", err
	}
	input, output = d.Names()
	if err := d.Start(); err != nil {
		return nil, nil, "", "", errors.Join(err, d.Close())
	}
	select {
	case <-done:
	case <-ctx.Done():
		err = ctx.Err()
	}
	err = errors.Join(err, d.Close())
	mu.Lock()
	defer mu.Unlock()
	return heard[:min(n, len(heard))], played[:min(n, len(played))], input, output, err
}
