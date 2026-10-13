// Package wake slides a fixed window over 16 kHz audio, scores it every hop
// and debounces accepted windows into wake-word detections.
//
// It is phonmatchnet-run's internal/listen, made push-driven: the caller
// writes audio frames of any size as they arrive, instead of the listener
// reading a stream on its own goroutine. Window placement and debouncing are
// unchanged.
package wake

import (
	"context"
	"errors"
	"fmt"
)

// Config sets the window geometry and debouncing, in samples.
type Config struct {
	Window int // samples scored per window
	Hop    int // samples between window ends
	// Confirm is how many consecutive accepted windows make a detection.
	Confirm int
	// Refractory is how long after a detection, measured from its window's
	// end, no window counts toward another one. A refractory period of at
	// least Window keeps one utterance from firing twice.
	Refractory int
}

// Validate checks the geometry.
func (c Config) Validate() error {
	switch {
	case c.Window < 1:
		return errors.New("window must be positive")
	case c.Hop < 1 || c.Hop > c.Window:
		return fmt.Errorf("hop must be between 1 and the window (%d samples)", c.Window)
	case c.Confirm < 1:
		return errors.New("confirm must be at least 1")
	case c.Refractory < 0:
		return errors.New("refractory period must be nonnegative")
	}
	return nil
}

// Window is one scored span of the stream. Start and End are sample offsets
// from the start of the stream, End exclusive. Samples is reused by the next
// window; copy it to keep it.
type Window struct {
	Start, End int64
	Samples    []float32
}

// Event reports one scored window. Detected marks the window that completed
// a detection.
type Event[T any] struct {
	Window   Window
	Result   T
	Accepted bool
	Detected bool
}

// Score scores one window and reports whether it is accepted.
type Score[T any] func(context.Context, Window) (T, bool, error)

// Listener holds the most recent Window samples and the debounce state. It is
// not safe for concurrent use.
type Listener[T any] struct {
	config  Config
	score   Score[T]
	buffer  []float32
	total   int64 // samples written
	pending int   // samples since the last hop boundary
	streak  int   // consecutive accepted windows
	last    int64 // End of the last detection, or -1
}

// New returns a listener at stream offset zero.
func New[T any](c Config, score Score[T]) (*Listener[T], error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if score == nil {
		return nil, errors.New("score is required")
	}
	return &Listener[T]{config: c, score: score, buffer: make([]float32, 0, c.Window), last: -1}, nil
}

// Position returns how many samples have been written.
func (l *Listener[T]) Position() int64 { return l.total }

// Write appends samples, which continue the stream, and scores a window at
// every hop boundary once Window samples have arrived, calling emit for each.
// A frame that crosses several boundaries scores several windows. Errors
// from score or emit stop Write; the samples up to that window have been
// consumed.
func (l *Listener[T]) Write(ctx context.Context, samples []float32, emit func(Event[T]) error) error {
	c := l.config
	for len(samples) > 0 {
		n := min(len(samples), c.Hop-l.pending)
		l.buffer = append(l.buffer, samples[:n]...)
		if len(l.buffer) > c.Window {
			l.buffer = l.buffer[:copy(l.buffer, l.buffer[len(l.buffer)-c.Window:])]
		}
		samples = samples[n:]
		l.total += int64(n)
		l.pending += n
		if l.pending < c.Hop {
			continue
		}
		l.pending = 0
		if l.total < int64(c.Window) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		w := Window{Start: l.total - int64(c.Window), End: l.total, Samples: l.buffer}
		result, accepted, err := l.score(ctx, w)
		if err != nil {
			return err
		}
		event := Event[T]{Window: w, Result: result, Accepted: accepted}
		switch {
		case l.last >= 0 && w.End-l.last < int64(c.Refractory):
			l.streak = 0
		case accepted:
			l.streak++
		default:
			l.streak = 0
		}
		if l.streak >= c.Confirm {
			event.Detected = true
			l.last, l.streak = w.End, 0
		}
		if err := emit(event); err != nil {
			return err
		}
	}
	return nil
}
