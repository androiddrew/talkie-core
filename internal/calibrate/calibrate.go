// Package calibrate measures a room from what talkie plays and what the
// microphone hears: the speaker-to-microphone delay and level from a chirp,
// and the noise floor from silence.
package calibrate

import (
	"math"
	"time"

	"github.com/androiddrew/talkie-core/internal/audio"
)

// Chirp returns a sweep from 300 Hz to 3 kHz, the band where speakers and
// microphones both work, over d at rate, with peak amplitude amp and a
// raised-sine envelope so it starts and ends without clicks.
func Chirp(rate int, d time.Duration, amp float64) []float32 {
	n := int(d.Seconds() * float64(rate))
	out := make([]float32, n)
	length := d.Seconds()
	for i := range out {
		t := float64(i) / float64(rate)
		phase := 2 * math.Pi * (300*t + (3000-300)*t*t/(2*length))
		out[i] = float32(amp * math.Sin(phase) * math.Sin(math.Pi*t/length))
	}
	return out
}

// Echo is a chirp's measured path from the speaker to the microphone.
type Echo struct {
	// Heard reports whether the microphone heard the chirp at all; with a
	// headset it doesn't, and Delay and GainDB mean nothing.
	Heard bool
	// Delay is from writing the chirp's first sample to the speaker to it
	// arriving at the microphone: device buffers plus the air.
	Delay time.Duration
	// GainDB is the echo's level relative to what was played: the
	// least-squares gain of the direct path, in dB.
	GainDB float64
	// Correlation is the normalized correlation at Delay, 0 to 1.
	Correlation float64
}

// minCorrelation is the normalized correlation below which the chirp was
// not heard.
const minCorrelation = 0.3

// Measure finds the chirp played at sample start of played in heard, which
// was recorded on the same clock at rate, searching delays up to maxDelay.
func Measure(played, heard []float32, start, length, rate int, maxDelay time.Duration) Echo {
	// Correlate at 16 kHz: plenty of precision, a ninth of the work at 48 kHz.
	p := audio.NewResampler(rate, audio.SampleRate).Process(append([]float32(nil), played...))
	h := audio.NewResampler(rate, audio.SampleRate).Process(append([]float32(nil), heard...))
	s := start * audio.SampleRate / rate
	n := length * audio.SampleRate / rate
	if s+n > len(p) {
		return Echo{}
	}
	ref := p[s : s+n]
	var refEnergy float64
	for _, v := range ref {
		refEnergy += float64(v) * float64(v)
	}
	maxLag := int(maxDelay.Seconds() * audio.SampleRate)
	best, bestLag := 0.0, -1
	for lag := 0; lag <= maxLag && s+lag+n <= len(h); lag++ {
		var dot, energy float64
		for i, v := range ref {
			x := float64(h[s+lag+i])
			dot += float64(v) * x
			energy += x * x
		}
		if energy == 0 {
			continue
		}
		if c := dot / math.Sqrt(refEnergy*energy); c > best {
			best, bestLag = c, lag
		}
	}
	if bestLag < 0 || best < minCorrelation || refEnergy == 0 {
		return Echo{Correlation: max(best, 0)}
	}
	var dot float64
	for i, v := range ref {
		dot += float64(v) * float64(h[s+bestLag+i])
	}
	return Echo{
		Heard:       true,
		Delay:       time.Duration(bestLag) * time.Second / audio.SampleRate,
		GainDB:      20 * math.Log10(math.Abs(dot/refEnergy)),
		Correlation: best,
	}
}

// LevelDBFS is the RMS level of samples in dB relative to full scale; -120
// for silence.
func LevelDBFS(samples []float32) float64 {
	var sum float64
	for _, s := range samples {
		sum += float64(s) * float64(s)
	}
	if len(samples) == 0 || sum == 0 {
		return -120
	}
	return 10 * math.Log10(sum/float64(len(samples)))
}

// Peak is the largest magnitude in samples.
func Peak(samples []float32) float64 {
	var p float64
	for _, s := range samples {
		p = max(p, math.Abs(float64(s)))
	}
	return p
}
