package audio

import "math"

// Resampler converts a stream between two rates by any rational ratio
// (24 kHz to 16 or 48 kHz, 48 kHz to 16 kHz, ...) with a polyphase FIR
// filter. Its output does not depend on how the input is split into blocks.
//
// It is wonky-talkie's Resampler with a longer filter when downsampling:
// wonky's 24 taps per phase gave 48 kHz to 16 kHz a 24-tap filter that let
// 9.6 kHz through at -24 dB, folding noise into the band the VAD and ASR
// hear. The filter here spans 24 taps of the slower rate on both sides.
type Resampler struct {
	up, down int
	taps     int         // per phase
	phases   [][]float64 // [phase][tap] = h[phase + tap*up]
	history  []float64   // the last taps-1 input samples
	consumed int64       // input samples so far
	next     int64       // index of the next output sample
}

const tapsPerPhase = 24

// NewResampler returns a resampler from rate in to rate out; equal rates
// pass audio through.
func NewResampler(in, out int) *Resampler {
	g := gcd(in, out)
	r := &Resampler{up: out / g, down: in / g}
	if r.up == r.down {
		return r
	}
	r.taps = (tapsPerPhase*max(r.up, r.down) + r.up - 1) / r.up
	h := lowpass(r.taps*r.up, 0.92/float64(max(r.up, r.down)))
	r.phases = make([][]float64, r.up)
	for p := range r.phases {
		r.phases[p] = make([]float64, r.taps)
		for t := range r.taps {
			r.phases[p][t] = h[p+t*r.up] * float64(r.up)
		}
	}
	r.history = make([]float64, r.taps-1)
	return r
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// lowpass is scipy.signal.firwin(n, cutoff) with its default Hamming window:
// a windowed sinc normalized to unit gain at DC. cutoff is relative to the
// Nyquist frequency.
func lowpass(n int, cutoff float64) []float64 {
	h := make([]float64, n)
	alpha := float64(n-1) / 2
	var sum float64
	for i := range h {
		m := float64(i) - alpha
		h[i] = cutoff * sinc(cutoff*m) * (0.54 - 0.46*math.Cos(2*math.Pi*float64(i)/float64(n-1)))
		sum += h[i]
	}
	for i := range h {
		h[i] /= sum
	}
	return h
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

// Process converts the next block of input, returning the output samples
// it completes.
func (r *Resampler) Process(in []float32) []float32 {
	if r.up == r.down {
		return in
	}
	buf := make([]float64, len(r.history)+len(in))
	copy(buf, r.history)
	for i, s := range in {
		buf[len(r.history)+i] = float64(s)
	}
	consumed := r.consumed + int64(len(in))
	L, M := int64(r.up), int64(r.down)
	var end int64 // outputs whose newest input sample has arrived
	if consumed > 0 {
		end = (consumed*L-1)/M + 1
	}
	out := make([]float32, 0, max(0, end-r.next))
	base := r.consumed - int64(r.taps-1) // the input index of buf[0]
	for k := r.next; k < end; k++ {
		n := k * M
		phase := r.phases[n%L]
		newest := n/L - base
		var y float64
		for t, c := range phase {
			y += c * buf[newest-int64(t)]
		}
		out = append(out, float32(y))
	}
	r.next, r.consumed = end, consumed
	copy(r.history, buf[len(buf)-len(r.history):])
	return out
}
