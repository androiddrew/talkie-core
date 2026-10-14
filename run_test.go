package talkie

import (
	"context"
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
	callback := pump(player, sim.Rate, blocks)
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
	cb := pump(audio.NewPlayer(sim.Rate, time.Millisecond), sim.Rate, blocks)
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
