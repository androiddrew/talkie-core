package talkie

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/androiddrew/talkie-core/internal/audio"
	"github.com/androiddrew/talkie-core/internal/engine"
	"github.com/androiddrew/talkie-core/internal/sim"
)

// The bench runs a conversation on the simulated room: real speech, the real
// Silero VAD, a scripted wake word, an instant transcriber and a fake voice.
// The test goroutine is the owner goroutine; agent turns run on their own.

const wakeT = 1.95 // end of the wake phrase in wakeThenCommand

// fakeVoice "synthesizes" text as real speech, 60 ms per character, so the
// VAD and the room treat the agent's voice like a voice. It delivers a
// quarter second at a time, like a model run.
type fakeVoice struct {
	speech []float32
	mu     sync.Mutex
	spoken []string
}

func (v *fakeVoice) Speak(ctx context.Context, text string, yield func([]float32) error) error {
	v.mu.Lock()
	v.spoken = append(v.spoken, text)
	v.mu.Unlock()
	n := len(text) * sim.Rate * 60 / 1000
	const piece = sim.Rate / 4
	for start := 0; start < n; start += piece {
		if err := ctx.Err(); err != nil {
			return err
		}
		out := make([]float32, min(piece, n-start))
		for i := range out {
			out[i] = v.speech[(start+i)%len(v.speech)]
		}
		if err := yield(out); err != nil {
			return err
		}
	}
	return nil
}

type bench struct {
	t      *testing.T
	conv   *conversation
	player *audio.Player
	room   *sim.Room
	noise  *sim.Noise
	log    []engine.Event
}

type benchOptions struct {
	wakeAt []float64
	stt    []string // what the user says, utterance by utterance
	echo   bool
	room   *sim.Room // instead of the default room
}

func newBench(t *testing.T, agent Agent, o benchOptions) *bench {
	t.Helper()
	b := &bench{t: t, player: audio.NewPlayer(sim.Rate, 15*time.Millisecond), room: sim.NewRoom(o.echo), noise: sim.NewNoise()}
	if o.room != nil {
		b.room = o.room
	}
	said := o.stt
	var err error
	b.conv, err = newConversation(t.Context(), conversationConfig{
		engine: engine.Defaults(),
		vad:    sim.VAD(t),
		wake:   sim.NewWake(o.wakeAt...),
		player: b.player,
		agent:  agent,
		voice:  &fakeVoice{speech: sim.AgentVoice(t, 8.0)},
		stt: func(context.Context, []float32) (string, error) {
			if len(said) == 0 {
				return "", nil
			}
			text := said[0]
			said = said[1:]
			return text, nil
		},
		onEvent: func(ev engine.Event) { b.log = append(b.log, ev) },
	})
	if err != nil {
		t.Fatal(err)
	}
	b.attach()
	return b
}

// attach makes transcription synchronous and cancels the last turn at the
// end of the test.
func (b *bench) attach() {
	b.conv.background = func(f func()) { f() }
	b.t.Cleanup(func() {
		if tr := b.conv.last; tr != nil {
			tr.cancel()
			<-tr.done
		}
	})
}

// feed plays the user's audio into the mic, 32 ms at a time, in lock-step
// with the player.
func (b *bench) feed(user []float32) {
	b.t.Helper()
	for start := 0; start < len(user); start += 512 {
		block := user[start:min(start+512, len(user))]
		ref := make([]float32, len(block))
		played := b.player.Read(ref)
		if err := b.conv.audio(b.room.Mix(block, ref), ref, played); err != nil {
			b.t.Fatal(err)
		}
	}
}

func (b *bench) silence(seconds float64) { b.feed(b.noise.Silence(seconds)) }

func (b *bench) wakeThenCommand() {
	b.silence(1.0)
	b.feed(sim.WakePhrase(b.t))
	b.silence(0.1)
	b.feed(sim.Command(b.t))
	b.silence(1.5)
}

// spoken waits until the latest turn's reply is entirely in the player.
func (b *bench) spoken() {
	b.t.Helper()
	tr := b.conv.last
	if tr == nil {
		b.t.Fatal("no turn yet")
	}
	select {
	case <-tr.done:
	case <-time.After(10 * time.Second):
		b.t.Fatal("the reply was never spoken")
	}
}

func (b *bench) find(typ engine.EventType) (engine.Event, bool) {
	for _, e := range b.log {
		if e.Type == typ {
			return e, true
		}
	}
	return engine.Event{}, false
}

// turns records the turns an agent receives.
type turns struct {
	mu   sync.Mutex
	got  []Turn
	seen chan Turn
}

func newTurns() *turns { return &turns{seen: make(chan Turn, 10)} }

func (r *turns) add(t Turn) {
	r.mu.Lock()
	r.got = append(r.got, t)
	r.mu.Unlock()
	r.seen <- t
}

func (r *turns) next(t *testing.T) Turn {
	t.Helper()
	select {
	case turn := <-r.seen:
		return turn
	case <-time.After(10 * time.Second):
		t.Fatal("the agent was never asked")
		return Turn{}
	}
}

func TestConversationTurns(t *testing.T) {
	asked := newTurns()
	agent := AgentFunc(func(ctx context.Context, turn Turn, reply *Reply) error {
		asked.add(turn)
		if turn.Trigger == TriggerWake {
			_, err := reply.WriteString("Which room? The office or the den?")
			return err
		}
		reply.Then(ThenEnd)
		_, err := reply.WriteString("Done.")
		return err
	})
	b := newBench(t, agent, benchOptions{wakeAt: []float64{wakeT}, stt: []string{"Turn on the lights.", "The office."}})
	b.wakeThenCommand()
	first := asked.next(t)
	if first.Text != "Turn on the lights." || first.Trigger != TriggerWake || first.Session == "" || first.Interrupted != nil {
		t.Fatalf("first turn %+v", first)
	}
	b.spoken()
	b.silence(3.0) // the question plays, then a follow-up window opens
	fin, ok := b.find(engine.PlaybackFinished)
	if !ok || fin.Playback.Heard != "Which room? The office or the den?" || b.conv.eng.State() != engine.Listening {
		t.Fatalf("after the question: finished %+v, state %s", fin.Playback, b.conv.eng.State())
	}
	b.feed(sim.ShortReply(t))
	b.silence(1.5)
	second := asked.next(t)
	if second.Text != "The office." || second.Trigger != TriggerFollowup || second.Session != first.Session {
		t.Fatalf("second turn %+v", second)
	}
	// The question played to the end, so nothing was interrupted.
	if second.Interrupted != nil {
		t.Errorf("the second turn says the first reply was interrupted: %+v", *second.Interrupted)
	}
	b.spoken()
	b.silence(1.0)
	if ended, ok := b.find(engine.SessionEnded); !ok || ended.Reason != "agent" {
		t.Errorf("session not ended by the agent: %+v", ended)
	}
}

// TestBargeInCancelsTurn: the user cuts in during the second of three
// sentences. The agent's context is cancelled, and the next turn says what
// was heard, cut off and never spoken, in the agent's words.
func TestBargeInCancelsTurn(t *testing.T) {
	asked := newTurns()
	cancelled := make(chan error, 1)
	agent := AgentFunc(func(ctx context.Context, turn Turn, reply *Reply) error {
		asked.add(turn)
		if turn.Trigger != TriggerWake {
			return nil
		}
		// About 2.5 s of speech per sentence.
		reply.WriteString("Once upon a time, in a land far away, there lived a robot. ")
		reply.WriteString("It listened to the rain every single night without fail. ")
		reply.WriteString("One night the rain stopped, and the robot was very sad.")
		<-ctx.Done() // still "generating" when interrupted
		_, err := reply.WriteString("More.")
		cancelled <- err
		return ctx.Err()
	})
	b := newBench(t, agent, benchOptions{wakeAt: []float64{wakeT}, stt: []string{"Tell me a story.", "Stop, that's enough."}})
	b.wakeThenCommand()
	asked.next(t)
	b.silence(3.0) // the first sentence plays
	b.feed(sim.Command(t))
	b.silence(1.5)
	select {
	case err := <-cancelled:
		if err == nil {
			t.Error("Write succeeded after the interruption")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the agent's context was not cancelled")
	}
	next := asked.next(t)
	in := next.Interrupted
	if next.Text != "Stop, that's enough." || next.Trigger != TriggerBargeIn || in == nil {
		t.Fatalf("next turn %+v", next)
	}
	if in.Heard != "Once upon a time, in a land far away, there lived a robot." ||
		in.Cut != "It listened to the rain every single night without fail." ||
		in.Unspoken != "One night the rain stopped, and the robot was very sad." {
		t.Errorf("interruption %+v", *in)
	}
	if in.Played < 3*time.Second || in.Played > 5*time.Second {
		t.Errorf("played %v", in.Played)
	}
	if b.player.Active() {
		t.Error("the reply kept playing")
	}
}

// TestStreamedReply writes the reply in small deltas, as an LLM streams
// tokens: it is spoken sentence by sentence, in the agent's words.
func TestStreamedReply(t *testing.T) {
	agent := AgentFunc(func(ctx context.Context, turn Turn, reply *Reply) error {
		reply.Then(ThenWait)
		for _, delta := range []string{"It is", " 3.5 degr", "ees outsi", "de. Bring", " a coat!", " Anything", " else?"} {
			if _, err := reply.WriteString(delta); err != nil {
				return err
			}
			time.Sleep(5 * time.Millisecond)
		}
		return nil
	})
	b := newBench(t, agent, benchOptions{wakeAt: []float64{wakeT}, stt: []string{"What's the weather?"}})
	b.wakeThenCommand()
	b.spoken()
	b.silence(5.0)
	fin, ok := b.find(engine.PlaybackFinished)
	if !ok || fin.Playback.Heard != "It is 3.5 degrees outside. Bring a coat! Anything else?" {
		t.Fatalf("finished %+v", fin.Playback)
	}
	if v := b.conv.voice.(*fakeVoice); strings.Join(v.spoken, "|") != "It is 3.5 degrees outside.|Bring a coat!|Anything else?" {
		t.Errorf("synthesized %q", v.spoken)
	}
	if b.conv.eng.State() != engine.Waiting {
		t.Errorf("state %s after ThenWait", b.conv.eng.State())
	}
}

// TestInterruptWhileThinking: the user speaks before the agent says
// anything. Nothing was played, so nothing was heard.
func TestInterruptWhileThinking(t *testing.T) {
	asked := newTurns()
	agent := AgentFunc(func(ctx context.Context, turn Turn, reply *Reply) error {
		asked.add(turn)
		if turn.Trigger == TriggerWake {
			reply.WriteString("Let me think about") // no sentence yet
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})
	b := newBench(t, agent, benchOptions{wakeAt: []float64{wakeT}, stt: []string{"Turn on the lights.", "Actually, never mind."}})
	b.wakeThenCommand()
	asked.next(t)
	b.silence(0.5)
	b.feed(sim.ShortReply(t))
	b.silence(1.5)
	next := asked.next(t)
	if in := next.Interrupted; in == nil || in.Heard != "" || in.Cut != "" || in.Unspoken != "Let me think about" || in.Played != 0 {
		t.Fatalf("interruption %+v", next.Interrupted)
	}
	if next.Text != "Actually, never mind." {
		t.Errorf("next turn %q", next.Text)
	}
}

func TestSilentReply(t *testing.T) {
	for _, c := range []struct {
		then  Then
		state engine.State
	}{{ThenListen, engine.Listening}, {ThenEnd, engine.Idle}, {ThenWait, engine.Waiting}} {
		t.Run(string(c.then), func(t *testing.T) {
			agent := AgentFunc(func(ctx context.Context, turn Turn, reply *Reply) error {
				reply.Then(c.then)
				return nil
			})
			b := newBench(t, agent, benchOptions{wakeAt: []float64{wakeT}, stt: []string{"Lights off."}})
			b.wakeThenCommand()
			b.spoken()
			b.silence(0.1)
			if b.conv.eng.State() != c.state {
				t.Errorf("state %s", b.conv.eng.State())
			}
		})
	}
}

func TestAgentError(t *testing.T) {
	agent := AgentFunc(func(ctx context.Context, turn Turn, reply *Reply) error {
		return errors.New("model unavailable")
	})
	b := newBench(t, agent, benchOptions{wakeAt: []float64{wakeT}, stt: []string{"Hello?"}})
	b.wakeThenCommand()
	b.spoken()
	b.silence(0.1)
	ev, ok := b.find(engine.Error)
	if !ok || !strings.Contains(ev.Err.Error(), "model unavailable") {
		t.Errorf("error event %+v", ev)
	}
	if b.conv.eng.State() != engine.Listening {
		t.Errorf("state %s; the user should be able to try again", b.conv.eng.State())
	}
}

// TestEchoDoesNotInterruptReply plays a reply into a room that echoes it into
// the mic: duck-and-verify keeps the agent from interrupting itself.
func TestEchoDoesNotInterruptReply(t *testing.T) {
	agent := AgentFunc(func(ctx context.Context, turn Turn, reply *Reply) error {
		_, err := reply.WriteString("Once upon a time, in a land far away, there lived a robot. " +
			"It listened to the rain every single night without fail.")
		return err
	})
	b := newBench(t, agent, benchOptions{wakeAt: []float64{wakeT}, stt: []string{"Tell me a story."}, echo: true})
	b.wakeThenCommand()
	b.spoken()
	b.silence(7.0)
	if ev, ok := b.find(engine.InterruptionDetected); ok {
		t.Fatalf("the agent interrupted itself: %+v", ev)
	}
	fin, ok := b.find(engine.PlaybackFinished)
	if !ok || !strings.HasSuffix(fin.Playback.Heard, "without fail.") || b.conv.eng.State() != engine.Listening {
		t.Errorf("finished %+v, state %s", fin.Playback, b.conv.eng.State())
	}
}
