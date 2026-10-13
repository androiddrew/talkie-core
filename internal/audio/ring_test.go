package audio

import (
	"slices"
	"testing"
)

func seq(from, n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(from + i)
	}
	return out
}

// TestRing writes a counting stream in uneven frames, including frames
// larger than the ring, and checks every read against the positions.
func TestRing(t *testing.T) {
	for _, frames := range [][]int{{3}, {7, 1, 12}, {25}, {10}, {9, 30, 2}} {
		r := NewRing(10)
		pos := 0
		for i := 0; pos < 120; i++ {
			n := frames[i%len(frames)]
			r.Write(seq(pos, n))
			pos += n
			if r.Total() != int64(pos) || r.Oldest() != int64(max(0, pos-10)) {
				t.Fatalf("frames %v: total %d oldest %d after %d samples", frames, r.Total(), r.Oldest(), pos)
			}
			for start := pos - 12; start < pos; start++ {
				for end := start; end <= pos+2; end++ {
					got := r.Read(int64(start), int64(end))
					from, to := max(start, pos-10, 0), min(end, pos)
					var want []float32
					if to > from {
						want = seq(from, to-from)
					}
					if !slices.Equal(got, want) {
						t.Fatalf("frames %v at %d: Read(%d, %d) = %v, want %v", frames, pos, start, end, got, want)
					}
				}
			}
		}
	}
}
