package talkie

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/androiddrew/talkie-core/internal/calibrate"
	"github.com/androiddrew/talkie-core/internal/device"
)

// Step is one stage of room training, for the application to present.
type Step struct {
	Name        string // "noise", "echo"
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
// the speaker-to-microphone delay and level from quiet chirps. It needs no
// models. Save the result with SaveProfile at ProfilePath(dir, p.Input,
// p.Output) for Run to find it.
func Train(ctx context.Context, cfg Config, prompt Prompt) (Profile, error) {
	return train(ctx, cfg, prompt, openDevice)
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

func train(ctx context.Context, cfg Config, prompt Prompt, open opener) (Profile, error) {
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
	return p, nil
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
