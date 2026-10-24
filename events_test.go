package talkie

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/androiddrew/talkie-core/internal/engine"
	"github.com/androiddrew/talkie-core/internal/sim"
)

// published runs the bench's conversation through Talkie.event and records
// what Config.OnEvent receives.
func published(b *bench) *[]Event {
	var got []Event
	tk := &Talkie{cfg: Config{OnEvent: func(ev Event) { got = append(got, ev) }}}
	log := b.conv.onEvent
	b.conv.onEvent = func(ev engine.Event) {
		log(ev)
		tk.event(ev)
	}
	return &got
}

func types(events []Event, skip ...EventType) []EventType {
	var out []EventType
	for _, ev := range events {
		if !slices.Contains(skip, ev.Type) {
			out = append(out, ev.Type)
		}
	}
	return out
}

func findEvent(events []Event, typ EventType) (Event, bool) {
	for _, ev := range events {
		if ev.Type == typ {
			return ev, true
		}
	}
	return Event{}, false
}

// TestEvents: a whole exchange, from the wake word to the reply played,
// reaches OnEvent in order, with the states a display would show.
func TestEvents(t *testing.T) {
	asked := newTurns()
	agent := AgentFunc(func(ctx context.Context, turn Turn, reply *Reply) error {
		asked.add(turn)
		_, err := reply.WriteString("Sure.")
		return err
	})
	b := newBench(t, agent, benchOptions{wakeAt: []float64{wakeT}, stt: []string{"Lights on."}})
	got := published(b)
	b.wakeThenCommand()
	b.spoken()
	b.silence(2.0)

	want := []EventType{EventSessionStarted, EventWake, EventListening, EventSpeechStarted, EventTranscript,
		EventReplyStarted, EventReplyFinished, EventListening}
	if g := types(*got, EventState, EventLatency); !slices.Equal(g, want) {
		t.Errorf("events %v\nwant %v", g, want)
	}
	var states []State
	for _, ev := range *got {
		if ev.Type == EventState {
			states = append(states, ev.State)
		}
	}
	if want := []State{StateListening, StateHearing, StateTranscribing, StateThinking, StateSpeaking, StateListening}; !slices.Equal(states, want) {
		t.Errorf("states %v\nwant %v", states, want)
	}
	turn := asked.next(t)
	tr, _ := findEvent(*got, EventTranscript)
	fin, _ := findEvent(*got, EventReplyFinished)
	if tr.Text != "Lights on." || tr.Trigger != TriggerWake || tr.Session == "" || tr.Session != turn.Session || tr.STT < 0 || tr.Speech <= 0 {
		t.Errorf("transcript %+v", tr)
	}
	if fin.Reply == "" || fin.Reply != turn.Reply || fin.Heard != "Sure." || fin.Played <= 0 {
		t.Errorf("finished %+v, turn reply %q", fin, turn.Reply)
	}
	for i, ev := range *got {
		if ev.Time.IsZero() || (i > 0 && ev.At < (*got)[i-1].At) {
			t.Errorf("event %d %s at %v, time %v", i, ev.Type, ev.At, ev.Time)
		}
	}
}

// TestInterruptionEvents: what interrupted the agent, and how far its reply
// had played.
func TestInterruptionEvents(t *testing.T) {
	for _, by := range []InterruptedBy{InterruptedBySpeech, InterruptedByApp} {
		agent := AgentFunc(func(ctx context.Context, turn Turn, reply *Reply) error {
			if turn.Trigger != TriggerWake {
				return nil
			}
			reply.WriteString("Once upon a time, in a land far away, there lived a robot. ")
			reply.WriteString("It listened to the rain every single night without fail. ")
			<-ctx.Done()
			return ctx.Err()
		})
		b := newBench(t, agent, benchOptions{wakeAt: []float64{wakeT}, stt: []string{"Tell me a story.", "Stop."}})
		got := published(b)
		b.wakeThenCommand()
		// The first sentence, 3.5 s, has played completely after 4 s, and
		// the second, 3.4 s more, hasn't yet.
		b.synthesized()
		b.silence(4.0)
		if by == InterruptedByApp {
			b.conv.eng.Interrupt()
		} else {
			b.feed(sim.Command(t))
		}
		b.silence(1.5)
		in, ok := findEvent(*got, EventInterrupted)
		if !ok || in.InterruptedBy != by || in.While != StateSpeaking || in.Reply == "" ||
			in.Heard != "Once upon a time, in a land far away, there lived a robot." || in.Played < 3*time.Second {
			t.Errorf("%s: interrupted %+v", by, in)
		}
		if st, ok := findEvent(*got, EventReplyStopped); !ok || st.Reason != "interrupted" || st.Reply != in.Reply {
			t.Errorf("%s: stopped %+v", by, st)
		}
	}
}

// TestChimeAndRoomEvents: the ready chime's latency, and the room profile no
// longer fitting, reach OnEvent too.
func TestChimeAndRoomEvents(t *testing.T) {
	var got []Event
	tk := &Talkie{cfg: Config{OnEvent: func(ev Event) { got = append(got, ev) }},
		drift: newDrift(LatencyProfile{Runs: []time.Duration{1}, Min: 115 * time.Millisecond, Max: 117 * time.Millisecond})}
	for range driftReplies {
		tk.event(engine.Event{Type: engine.EchoMeasured, EchoDelay: 200 * time.Millisecond})
	}
	if g := types(got); !slices.Equal(g, []EventType{EventLatency, EventLatency, EventLatency, EventRoomChanged}) {
		t.Errorf("events %v", g)
	}
	if got[0].LatencySource != LatencyFromReply || got[3].Latency != 200*time.Millisecond {
		t.Errorf("events %+v", got)
	}

	b := newBench(t, AgentFunc(func(context.Context, Turn, *Reply) error { return nil }), benchOptions{})
	open, _ := simOpener(true, 100, -6)
	got = nil
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tk = &Talkie{cfg: Config{IgnoreRoomProfile: true, OnEvent: func(ev Event) { got = append(got, ev) }},
		conv: b.conv, player: b.player, open: open, started: cancel, chimeWait: time.Hour}
	if err := tk.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if ev, ok := findEvent(got, EventLatency); !ok || ev.LatencySource != LatencyFromChime || ev.Latency < 110*time.Millisecond || ev.Latency > 130*time.Millisecond {
		t.Errorf("chime %+v", ev)
	}
}

// TestEveryEngineEventDecided: each engine event is published or on the
// list of internal ones, so a new engine event can't go missing silently.
func TestEveryEngineEventDecided(t *testing.T) {
	internal := []engine.EventType{engine.SilenceDetected, engine.ListenTimeout, engine.InterruptionSuspected,
		engine.InterruptionRejected, engine.WakeIgnored}
	for _, typ := range []engine.EventType{engine.StateChanged, engine.SessionStarted, engine.SessionEnded,
		engine.WakeDetected, engine.WakeIgnored, engine.ListeningStarted, engine.SpeechStarted, engine.SilenceDetected,
		engine.Transcript, engine.UtteranceDiscarded, engine.ListenTimeout, engine.PlaybackStarted,
		engine.PlaybackFinished, engine.PlaybackStopped, engine.InterruptionSuspected, engine.InterruptionRejected,
		engine.InterruptionDetected, engine.EchoMeasured, engine.Error} {
		_, ok := publicEvent(engine.Event{Type: typ})
		if ok == slices.Contains(internal, typ) {
			t.Errorf("%s: published %v", typ, ok)
		}
	}
}
