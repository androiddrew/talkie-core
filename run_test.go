package talkie

import (
	"bytes"
	"context"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/androiddrew/talkie-core/internal/audio"
	"github.com/androiddrew/talkie-core/internal/engine"
	"github.com/androiddrew/talkie-core/internal/sim"
)

// TestRun drives the owner loop from a simulated duplex device: its own
// goroutine calls the audio-thread callback each 20 ms period, as a real
// device does, while the loop runs the conversation, transcriptions and the
// agent's turn concurrently.
func TestRun(t *testing.T) {
	asked := newTurns()
	agent := AgentFunc(func(ctx context.Context, turn Turn, reply *Reply) error {
		asked.add(turn)
		_, err := reply.WriteString("Sure. Anything else?")
		return err
	})
	var mu sync.Mutex
	var log []engine.Event
	player := audio.NewPlayer(sim.Rate, 15*time.Millisecond)
	conv, err := newConversation(t.Context(), conversationConfig{
		engine: engine.Defaults(),
		vad:    sim.VAD(t),
		wake:   sim.NewWake(wakeT),
		player: player,
		agent:  agent,
		voice:  &fakeVoice{speech: sim.AgentVoice(t, 8.0)},
		stt:    func(context.Context, []float32) (string, error) { return "Lights on.", nil },
		onEvent: func(ev engine.Event) {
			mu.Lock()
			log = append(log, ev)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The user: the wake word and a command, then silence for the reply.
	noise := sim.NewNoise()
	var user []float32
	for _, part := range [][]float32{noise.Silence(1.0), sim.WakePhrase(t), noise.Silence(0.1), sim.Command(t), noise.Silence(6.0)} {
		user = append(user, part...)
	}
	ctx, cancel := context.WithCancel(t.Context())
	blocks := make(chan block, 4)
	callback := pump(player, sim.Rate, blocks, nil)
	deviceDone := make(chan struct{})
	go func() { // the device
		defer close(deviceDone)
		const frames = sim.Rate / 50
		for start := 0; start+frames <= len(user); start += frames {
			out := make([]float32, frames)
			callback(user[start:start+frames], out)
		}
		cancel()
	}()
	if err := conv.run(ctx, blocks); err != nil {
		t.Fatal(err)
	}
	<-deviceDone // it cancelled after its last callback, so nothing is blocked
	close(blocks)
	if tr := conv.last; tr != nil {
		<-tr.done
	}

	if turn := asked.next(t); turn.Text != "Lights on." || turn.Trigger != TriggerWake {
		t.Errorf("turn %+v", turn)
	}
	mu.Lock()
	defer mu.Unlock()
	var finished *engine.Event
	for i, ev := range log {
		if ev.Type == engine.PlaybackFinished {
			finished = &log[i]
		}
	}
	if finished == nil || finished.Playback.Heard != "Sure. Anything else?" {
		t.Fatalf("the reply did not play: %+v", finished)
	}
	if conv.eng.State() != engine.Listening {
		t.Errorf("state %s after the reply", conv.eng.State())
	}
}

// TestPumpCleansMicrophone: samples past full scale and NaN from a device
// would make the VAD fail; the callback clips them.
func TestPumpCleansMicrophone(t *testing.T) {
	blocks := make(chan block, 1)
	cb := pump(audio.NewPlayer(sim.Rate, time.Millisecond), sim.Rate, blocks, nil)
	in := []float32{0.5, 1.5, -2, float32(nanValue()), 0}
	cb(in, make([]float32, len(in)))
	b := <-blocks
	want := []float32{0.5, 1, -1, 0, 0}
	for i := range want {
		if b.mic[i] != want[i] {
			t.Fatalf("mic %v, want %v", b.mic, want)
		}
	}
}

func nanValue() float64 {
	zero := 0.0
	return zero / zero
}

// TestPumpStaysInRange: a full-scale square wave overshoots in the
// resampler's lowpass filter. The microphone and the speaker reference must
// still reach the engine within [-1, 1], or the VAD and the echo canceller
// reject them and Run stops.
func TestPumpStaysInRange(t *testing.T) {
	for _, rate := range []int{48000, sim.Rate} {
		pumpStaysInRange(t, rate)
	}
}

func pumpStaysInRange(t *testing.T, rate int) {
	player := audio.NewPlayer(rate, time.Millisecond)
	square := make([]float32, rate)
	for i := range square {
		square[i] = 1
		if (i/40)%2 == 1 {
			square[i] = -1
		}
	}
	player.Start("loud")
	player.Add("loud", square, "Loud.")
	player.End("loud", audio.ThenListen, 0)
	blocks := make(chan block, 100)
	cb := pump(player, rate, blocks, nil)
	period := rate / 50
	for start := 0; start+period <= len(square); start += period {
		out := make([]float32, period)
		cb(square[start:start+period], out)
		clear(out) // the device reuses its buffer
		b := <-blocks
		for _, x := range [][]float32{b.mic, b.ref} {
			for _, s := range x {
				if !(s >= -1 && s <= 1) {
					t.Fatalf("%d Hz: sample %v reached the engine", rate, s)
				}
			}
		}
		if start > rate/10 && rms(b.ref) < 0.5 {
			t.Fatalf("%d Hz: the reference lost what was played (rms %.3f)", rate, rms(b.ref))
		}
	}
}

func rms(x []float32) float64 {
	var sum float64
	for _, s := range x {
		sum += float64(s) * float64(s)
	}
	return math.Sqrt(sum / float64(max(1, len(x))))
}

// TestReadyChime runs Run on simulated devices: the ready chime measures the
// session's latency, which the device's one period adds to the room's delay,
// and a slow session doesn't restore a canceller trained at another
// latency. Without an echo, as with a headset, or with the chime turned off,
// the latency comes from the profile.
func TestReadyChime(t *testing.T) {
	dir := t.TempDir()
	trained := 120 * time.Millisecond
	p := Profile{Input: "Sim Mic", Output: "Sim Speaker", Latency: LatencyProfile{
		Runs: []time.Duration{trained}, Min: trained, Median: trained, Max: trained,
	}}
	if err := SaveProfile(ProfilePath(dir, p.Input, p.Output), p); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		echo    bool
		delayMS int
		off     bool
		want    time.Duration
		log     string
	}{
		{"near trained", true, 105, false, 125 * time.Millisecond, "msg=\"ready chime\""},
		{"slow open", true, 160, false, 180 * time.Millisecond, "learns the room again"},
		{"headset", false, 100, false, trained, "did not hear the ready chime"},
		{"turned off", true, 160, true, trained, "using room profile"},
	} {
		var logs syncBuffer
		b := newBench(t, AgentFunc(func(context.Context, Turn, *Reply) error { return nil }), benchOptions{})
		open, _ := simOpener(c.echo, c.delayMS, -6)
		ctx, cancel := context.WithCancel(t.Context())
		tk := &Talkie{cfg: Config{RoomProfileDir: dir, DisableReadyChime: c.off, Logger: slog.New(slog.NewTextHandler(&logs, nil))},
			conv: b.conv, player: b.player, open: open, started: cancel, chimeWait: time.Hour}
		err := tk.Run(ctx)
		cancel()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := b.conv.eng.IOLatency(); got < c.want-3*time.Millisecond || got > c.want+3*time.Millisecond {
			t.Errorf("%s: latency %v, want %v", c.name, got, c.want)
		}
		if !strings.Contains(logs.String(), c.log) {
			t.Errorf("%s: log %q", c.name, logs.String())
		}
		if c.off && strings.Contains(logs.String(), "chime") {
			t.Errorf("%s: chimed: %q", c.name, logs.String())
		}
	}
}

// syncBuffer is a bytes.Buffer safe for a logger on another goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
