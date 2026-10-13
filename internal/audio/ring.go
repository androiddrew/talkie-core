// Package audio holds talkie's audio plumbing: a ring buffer addressed by
// absolute sample position and the player that owns the speaker.
package audio

// SampleRate is the rate of the listening side: microphone audio, the
// engine's clock and everything the models hear. Mono float32 in [-1, 1].
const SampleRate = 16000

// Ring is a fixed-size buffer of the most recent samples, addressed by
// absolute position: sample n of the stream is at index n, however many
// times the buffer has wrapped. It is not safe for concurrent use.
type Ring struct {
	buf   []float32
	total int64 // samples ever written: the position of the next sample
}

// NewRing returns a ring holding the last capacity samples.
func NewRing(capacity int) *Ring {
	return &Ring{buf: make([]float32, capacity)}
}

// Write appends samples to the stream.
func (r *Ring) Write(samples []float32) {
	capacity := int64(len(r.buf))
	if skip := int64(len(samples)) - capacity; skip > 0 {
		// Only the newest capacity samples can be held.
		r.total += skip
		samples = samples[skip:]
	}
	start := r.total % capacity
	n := copy(r.buf[start:], samples)
	copy(r.buf, samples[n:])
	r.total += int64(len(samples))
}

// Total returns how many samples have been written: the position of the
// next one.
func (r *Ring) Total() int64 { return r.total }

// Oldest returns the position of the oldest sample still held.
func (r *Ring) Oldest() int64 { return max(0, r.total-int64(len(r.buf))) }

// Read returns a copy of samples [start, end), clamped to what is held.
func (r *Ring) Read(start, end int64) []float32 {
	start, end = max(start, r.Oldest()), min(end, r.total)
	if end <= start {
		return nil
	}
	capacity := int64(len(r.buf))
	out := make([]float32, end-start)
	s := start % capacity
	n := copy(out, r.buf[s:])
	copy(out[n:], r.buf)
	return out
}
