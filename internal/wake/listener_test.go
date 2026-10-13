package wake

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// ramp returns samples 0, 1, 2, ... n-1 scaled by 1/32768, so a window's
// first sample value is its start offset.
func ramp(n int) []float32 {
	samples := make([]float32, n)
	for i := range samples {
		samples[i] = float32(i) / 32768
	}
	return samples
}

// run writes stream in frames of the given size, scoring windows as accepted
// when their End is in accept, and returns the scored windows' ends and the
// detections.
func run(t *testing.T, stream []float32, frame int, c Config, accept ...int64) (ends, detected []int64) {
	t.Helper()
	set := map[int64]bool{}
	for _, end := range accept {
		set[end] = true
	}
	l, err := New(c, func(_ context.Context, w Window) (int64, bool, error) {
		if len(w.Samples) != c.Window || w.Samples[0] != float32(w.Start)/32768 || w.Samples[c.Window-1] != float32(w.End-1)/32768 {
			t.Fatalf("window [%d,%d) has wrong samples", w.Start, w.End)
		}
		return w.End, set[w.End], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	emit := func(e Event[int64]) error {
		if e.Result != e.Window.End {
			t.Fatalf("result %d for window ending %d", e.Result, e.Window.End)
		}
		ends = append(ends, e.Window.End)
		if e.Detected {
			detected = append(detected, e.Window.End)
		}
		return nil
	}
	for start := 0; start < len(stream); start += frame {
		if err := l.Write(context.Background(), stream[start:min(start+frame, len(stream))], emit); err != nil {
			t.Fatal(err)
		}
	}
	if l.Position() != int64(len(stream)) {
		t.Fatalf("position %d after %d samples", l.Position(), len(stream))
	}
	return ends, detected
}

// frames are the write sizes every geometry is checked with: windows must
// not depend on how the audio was framed.
var frames = []int{1, 2, 3, 7, 1000}

func TestWindows(t *testing.T) {
	for _, frame := range frames {
		// Window 4, hop 2: windows end every 2 samples once 4 have arrived; the
		// trailing partial hop (sample 10) is not scored.
		ends, _ := run(t, ramp(11), frame, Config{Window: 4, Hop: 2, Confirm: 1})
		if want := []int64{4, 6, 8, 10}; !reflect.DeepEqual(ends, want) {
			t.Errorf("frame %d: window ends %v, want %v", frame, ends, want)
		}
		// A window that is not a multiple of the hop starts at the next hop.
		ends, _ = run(t, ramp(12), frame, Config{Window: 5, Hop: 3, Confirm: 1})
		if want := []int64{6, 9, 12}; !reflect.DeepEqual(ends, want) {
			t.Errorf("frame %d: window ends %v, want %v", frame, ends, want)
		}
		ends, _ = run(t, ramp(3), frame, Config{Window: 4, Hop: 2, Confirm: 1})
		if len(ends) != 0 {
			t.Errorf("frame %d: short stream scored %v", frame, ends)
		}
	}
}

func TestDebounce(t *testing.T) {
	for _, c := range []struct {
		name   string
		config Config
		accept []int64
		want   []int64
	}{
		{"every accept", Config{Window: 4, Hop: 2, Confirm: 1}, []int64{6, 8, 20}, []int64{6, 8, 20}},
		{"refractory", Config{Window: 4, Hop: 2, Confirm: 1, Refractory: 4}, []int64{6, 8, 10, 12, 20}, []int64{6, 10, 20}},
		{"confirm", Config{Window: 4, Hop: 2, Confirm: 2}, []int64{6, 10, 12, 14, 16, 30}, []int64{12, 16}},
		// Accepts inside the refractory period do not count toward the streak.
		{"confirm after refractory", Config{Window: 4, Hop: 2, Confirm: 2, Refractory: 6}, []int64{6, 8, 10, 12, 14, 16, 18}, []int64{8, 16}},
	} {
		for _, frame := range frames {
			if _, got := run(t, ramp(40), frame, c.config, c.accept...); !reflect.DeepEqual(got, c.want) {
				t.Errorf("%s, frame %d: detections at %v, want %v", c.name, frame, got, c.want)
			}
		}
	}
}

func TestStops(t *testing.T) {
	score := func(context.Context, Window) (int, bool, error) { return 0, false, nil }
	emit := func(Event[int]) error { return nil }
	c := Config{Window: 4, Hop: 2, Confirm: 1}
	newListener := func(score Score[int]) *Listener[int] {
		l, err := New(c, score)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := newListener(score).Write(ctx, ramp(10), emit); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
	stop := errors.New("stop")
	if err := newListener(score).Write(context.Background(), ramp(10), func(Event[int]) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("emit error: %v", err)
	}
	failing := func(context.Context, Window) (int, bool, error) { return 0, false, stop }
	if err := newListener(failing).Write(context.Background(), ramp(10), emit); !errors.Is(err, stop) {
		t.Errorf("score error: %v", err)
	}
	for _, bad := range []Config{{Window: 0, Hop: 1, Confirm: 1}, {Window: 4, Hop: 5, Confirm: 1}, {Window: 4, Hop: 2}, {Window: 4, Hop: 2, Confirm: 1, Refractory: -1}} {
		if _, err := New(bad, score); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if _, err := New[int](c, nil); err == nil {
		t.Error("accepted a nil score")
	}
}
