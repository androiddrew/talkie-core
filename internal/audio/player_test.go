package audio

import (
	"math"
	"reflect"
	"slices"
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
	p.Start("a")
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
	p.End("a", ThenListen, 5*time.Second)
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
	p.Start("a")
	p.Add("a", ones(50), "One.")
	p.Start("b") // ends "a"
	if p.Add("a", ones(10), "") {
		t.Error("a new item did not end the previous one")
	}
	p.Add("b", ones(50), "Two.")
	p.End("b", ThenEnd, 0)
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
	p.Start("a")
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
	p.Start("a")
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
	p.Start("b")
	p.Add("b", ones(10), "y")
	p.Read(out)
	if out[0] != 1 {
		t.Errorf("Stop did not restore full volume: %v", out[0])
	}
}

func TestPlayerRecent(t *testing.T) {
	p := NewPlayer(1000, time.Millisecond)
	p.Start("a")
	p.Add("a", make([]float32, 1000), "One.")
	p.Add("a", make([]float32, 1000), "Two.")
	p.Add("a", make([]float32, 1000), "Three.")
	p.End("a", ThenListen, 0)
	if got := p.Recent(time.Second); got != nil {
		t.Errorf("before playing: %v", got)
	}
	p.Read(make([]float32, 1500))
	if got := p.Recent(200 * time.Millisecond); !slices.Equal(got, []string{"Two."}) {
		t.Errorf("1.5 s in, last 0.2 s: %v", got)
	}
	if got := p.Recent(time.Second); !slices.Equal(got, []string{"One.", "Two."}) {
		t.Errorf("1.5 s in, last 1 s: %v", got)
	}
}

// TestPlayerCues: a cue mixes over the agent's audio, at full volume
// however it is ducked, and doesn't count as the agent playing.
func TestPlayerCues(t *testing.T) {
	p := NewPlayer(rate, time.Millisecond)
	p.Start("a")
	p.Add("a", ones(100), "Hi.")
	p.Duck(-20)
	out := make([]float32, 50)
	p.Read(out) // ramp down to the duck
	p.Cue([]float32{0.5, 0.5, 0.5})
	p.Read(out)
	if got := out[0] - 0.1; math.Abs(float64(got-0.5)) > 1e-6 || out[3] != out[4] {
		t.Errorf("cue over ducked audio: %v", out[:5])
	}
	stopped := p.Stop()
	if len(stopped) != 1 {
		t.Fatalf("stop: %+v", stopped)
	}
	p.Cue(ones(30))
	if p.Active() {
		t.Error("a cue made the player active")
	}
	// Two cues at once add up; one ending mid-read leaves the rest silent.
	p.Cue(ones(10))
	p.Read(out)
	if out[0] != 2 || out[9] != 2 || out[10] != 1 || out[29] != 1 || out[30] != 0 {
		t.Errorf("two cues: %v", out)
	}
	if p.Read(out); sum(out) != 0 {
		t.Errorf("after the cues: %v", out)
	}
}

// TestPlayerLoop: a loop repeats seamlessly across reads until StopLoop,
// then fades out over the ramp.
func TestPlayerLoop(t *testing.T) {
	p := NewPlayer(rate, 4*time.Millisecond) // a 4-sample ramp
	loop := []float32{1, 2, 3}
	p.Loop(loop)
	p.Loop([]float32{9}) // already looping: no restart
	var got []float32
	for _, n := range []int{2, 5, 1} {
		out := make([]float32, n)
		p.Read(out)
		got = append(got, out...)
	}
	if want := []float32{1, 2, 3, 1, 2, 3, 1, 2}; !slices.Equal(got, want) {
		t.Errorf("loop %v, want %v", got, want)
	}
	if !p.Looping() {
		t.Error("not looping")
	}
	p.StopLoop()
	if p.Looping() {
		t.Error("looping after StopLoop")
	}
	out := make([]float32, 8)
	p.Read(out)
	if want := []float32{3, 0.75, 1, 0.75, 0, 0, 0, 0}; !slices.Equal(out, want) { // 3, 1, 2, 3 at 4/4 to 1/4
		t.Errorf("fade %v, want %v", out, want)
	}
	// Loop while fading starts afresh.
	p.Loop(loop)
	p.StopLoop()
	p.Loop([]float32{5})
	p.Read(out[:2])
	if out[0] != 5 || out[1] != 5 {
		t.Errorf("restart during fade: %v", out[:2])
	}
}
