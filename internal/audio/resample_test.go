package audio

import (
	"math"
	"slices"
	"testing"
)

func sine(rate, hz, n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(0.5 * math.Sin(2*math.Pi*float64(hz)*float64(i)/float64(rate)))
	}
	return out
}

func rms(x []float32) float64 {
	var sum float64
	for _, s := range x {
		sum += float64(s) * float64(s)
	}
	return math.Sqrt(sum / float64(len(x)))
}

func TestResamplerRates(t *testing.T) {
	for _, c := range []struct{ in, out int }{{24000, 16000}, {24000, 48000}, {48000, 16000}, {44100, 16000}, {16000, 48000}} {
		r := NewResampler(c.in, c.out)
		in := sine(c.in, 440, c.in) // one second of 440 Hz
		out := r.Process(in)
		if want := c.out; len(out) != want {
			t.Errorf("%d -> %d: %d samples from 1 s, want %d", c.in, c.out, len(out), want)
		}
		// A passband tone keeps its level: compare away from the start transient.
		got, want := rms(out[len(out)/4:]), rms(in[len(in)/4:])
		if math.Abs(got-want)/want > 0.01 {
			t.Errorf("%d -> %d: rms %.4f, want %.4f", c.in, c.out, got, want)
		}
		// A tone above the output's Nyquist frequency is removed.
		high := r.Process(sine(c.in, c.out*6/10, c.in))
		if c.out < c.in && rms(high[len(high)/4:]) > 0.01 {
			t.Errorf("%d -> %d: %d Hz leaks at rms %.4f", c.in, c.out, c.out*6/10, rms(high[len(high)/4:]))
		}
	}
}

// TestResamplerBlocks: the output is the same however the input is split.
func TestResamplerBlocks(t *testing.T) {
	in := sine(24000, 1000, 10000)
	whole := NewResampler(24000, 16000).Process(in)
	for _, size := range []int{1, 7, 480, 4096} {
		r := NewResampler(24000, 16000)
		var got []float32
		for i := 0; i < len(in); i += size {
			got = append(got, r.Process(in[i:min(i+size, len(in))])...)
		}
		if !slices.Equal(got, whole) {
			t.Fatalf("block size %d: output differs", size)
		}
	}
}

func TestResamplerPassthrough(t *testing.T) {
	in := sine(16000, 440, 100)
	if out := NewResampler(16000, 16000).Process(in); !slices.Equal(out, in) {
		t.Error("equal rates changed the audio")
	}
}
