package audio

import "math"

// The built-in cues, synthesized at any rate: a listening cue of two glassy
// notes a fifth up (E5, B5), the idle cue the same notes down and a little
// quieter, and a thinking loop, a quiet fifth (330 and 495 Hz) that swells
// and fades every 1.6 s.

// ListeningSound returns the built-in listening cue at rate.
func ListeningSound(rate int) []float32 {
	return normalize(twoNotes(rate, 659.25, 987.77), -12)
}

// IdleSound returns the built-in idle cue at rate.
func IdleSound(rate int) []float32 {
	return normalize(twoNotes(rate, 987.77, 659.25), -14)
}

// ThinkingSound returns one period of the built-in thinking loop at rate.
// Both tones have whole cycles in the period and the swell starts and ends
// at zero, so it loops seamlessly.
func ThinkingSound(rate int) []float32 {
	const period = 1.6
	n := int(math.Round(period * float64(rate)))
	out := make([]float64, n)
	for i := range out {
		t := float64(i) / float64(rate)
		env := 0.5 - 0.5*math.Cos(2*math.Pi*t/period)
		out[i] = env * env * (math.Sin(2*math.Pi*330*t) + 0.6*math.Sin(2*math.Pi*495*t))
	}
	return normalize(out, -28)
}

// glass is a struck tone's partials: harmonic, with a little second and third.
var glass = [][2]float64{{1, 1}, {2, 0.3}, {3, 0.1}}

// twoNotes plays a and then, 90 ms later, b, which rings a little longer.
func twoNotes(rate int, a, b float64) []float64 {
	first := note(rate, a, 0.06, 0.25)
	second := note(rate, b, 0.084, 0.4)
	off := int(0.09 * float64(rate))
	out := make([]float64, off+len(second))
	copy(out, first)
	for i, s := range second {
		out[off+i] += s
	}
	return out
}

// note is a struck glassy tone: a 4 ms attack, an exponential decay of time
// constant tau, and a 10 ms fade at the end so it never clicks off.
func note(rate int, freq, tau, length float64) []float64 {
	n := int(length * float64(rate))
	out := make([]float64, n)
	for i := range out {
		t := float64(i) / float64(rate)
		env := math.Exp(-t / tau)
		if t < 0.004 {
			env *= t / 0.004
		}
		if rem := length - t; rem < 0.01 {
			env *= rem / 0.01
		}
		var s float64
		for _, p := range glass {
			s += p[1] * math.Sin(2*math.Pi*freq*p[0]*t)
		}
		out[i] = env * s
	}
	return out
}

// normalize scales x to a peak of peakDB dBFS.
func normalize(x []float64, peakDB float64) []float32 {
	var peak float64
	for _, s := range x {
		peak = math.Max(peak, math.Abs(s))
	}
	g := math.Pow(10, peakDB/20) / peak
	out := make([]float32, len(x))
	for i, s := range x {
		out[i] = float32(s * g)
	}
	return out
}
