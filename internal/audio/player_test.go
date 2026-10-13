package audio

import (
	"math"
	"reflect"
	"testing"
	"time"
)

// ones returns n samples of 1, so played audio is easy to count.
func ones(n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = 1
	}
	return out
}

func sum(samples []float32) (total float64) {
	for _, s := range samples {
		total += float64(s)
	}
	return total
}

const rate = 1000 // samples per second, so durations are easy to read

func TestPlayerLifecycle(t *testing.T) {
	p := NewPlayer(rate, time.Millisecond)
	out := make([]float32, 100)
	if events := p.Read(out); events != nil || sum(out) != 0 || p.Active() {
		t.Fatalf("idle player: events %v, sum %v", events, sum(out))
	}
	p.Start("a", ThenListen, 5*time.Second)
	p.Add("a", ones(150), "Hello there.")
	events := p.Read(out)
	if len(events) != 1 || events[0].Kind != Started || events[0].Progress.ID != "a" || sum(out) != 100 {
		t.Fatalf("first read: %+v, sum %v", events, sum(out))
	}
	// Underrun: 50 samples, then silence, and the item is still playing.
	if events := p.Read(out); events != nil || sum(out) != 50 || !p.Active() {
		t.Fatalf("underrun: %+v, sum %v", events, sum(out))
	}
	p.Add("a", ones(30), "")
	p.End("a")
	if p.Add("a", ones(10), "late") {
		t.Fatal("Add accepted audio after End")
	}
	events = p.Read(out)
	if sum(out) != 30 || len(events) != 1 || events[0].Kind != Finished {
		t.Fatalf("finish: %+v, sum %v", events, sum(out))
	}
	f := events[0]
	want := Progress{ID: "a", Played: 180 * time.Millisecond, Total: 180 * time.Millisecond, Complete: true, Heard: "Hello there."}
	if f.Progress != want || f.Then != ThenListen || f.Timeout != 5*time.Second || f.More {
		t.Errorf("finished %+v, want %+v", f, want)
	}
	if p.Active() {
		t.Error("active after finishing")
	}
}

func TestPlayerQueue(t *testing.T) {
	p := NewPlayer(rate, time.Millisecond)
	p.Start("a", ThenWait, 0)
	p.Add("a", ones(50), "One.")
	p.Start("b", ThenEnd, 0) // ends "a"
	if p.Add("a", ones(10), "") {
		t.Error("a new item did not end the previous one")
	}
	p.Add("b", ones(50), "Two.")
	p.End("b")
	events := p.Read(make([]float32, 200))
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, map[EventKind]string{Started: "started ", Finished: "finished "}[e.Kind]+e.Progress.ID)
	}
	if want := []string{"started a", "finished a", "started b", "finished b"}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("events %v, want %v", kinds, want)
	}
	if !events[1].More || events[3].More || events[3].Then != ThenEnd {
		t.Errorf("finish details: %+v", events)
	}
}

// TestPlayerStopProgress splits queued text into heard, cut and unspoken at
// the point playback stopped.
func TestPlayerStopProgress(t *testing.T) {
	p := NewPlayer(rate, time.Millisecond)
	p.Start("a", ThenListen, 0)
	p.Add("a", ones(100), "Once upon a time,")
	p.Add("a", ones(100), "there was a robot.")
	p.Add("a", ones(50), "") // the same sentence, synthesized in two runs
	p.Add("a", ones(100), "It loved rain.")
	p.Read(make([]float32, 180))
	current, ok := p.Current()
	want := Progress{ID: "a", Played: 180 * time.Millisecond, Total: 350 * time.Millisecond,
		Heard: "Once upon a time,", Cut: "there was a robot.", Unspoken: "It loved rain."}
	if !ok || current != want {
		t.Fatalf("current %+v, want %+v", current, want)
	}
	stopped := p.Stop()
	if len(stopped) != 1 || stopped[0] != want {
		t.Fatalf("stopped %+v", stopped)
	}
	if p.Active() || p.Add("a", ones(10), "") {
		t.Error("audio still queued after Stop")
	}
	out := make([]float32, 10)
	if events := p.Read(out); events != nil || sum(out) != 0 {
		t.Errorf("read after Stop: %+v", events)
	}
}

func TestPlayerDuck(t *testing.T) {
	p := NewPlayer(rate, 10*time.Millisecond) // 10-sample ramp
	p.Start("a", ThenListen, 0)
	p.Add("a", ones(100), "x")
	p.Duck(-20)
	out := make([]float32, 20)
	p.Read(out)
	// Gain falls by 0.1 per sample until it reaches -20 dB (0.1).
	if math.Abs(float64(out[0])-0.9) > 1e-6 || math.Abs(float64(out[19])-0.1) > 1e-6 {
		t.Fatalf("ducked ramp: %v", out)
	}
	p.Unduck()
	p.Read(out)
	if math.Abs(float64(out[19])-1) > 1e-6 || out[0] >= out[19] {
		t.Fatalf("unducked ramp: %v", out)
	}
	p.Duck(-20)
	p.Read(out)
	p.Stop()
	p.Start("b", ThenListen, 0)
	p.Add("b", ones(10), "y")
	p.Read(out)
	if out[0] != 1 {
		t.Errorf("Stop did not restore full volume: %v", out[0])
	}
}
