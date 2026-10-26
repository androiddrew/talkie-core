package engine

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/androiddrew/talkie-core/internal/audio"
	"github.com/androiddrew/talkie-core/internal/sim"
)

// The harness drives an Engine with audio built from real speech clips, the
// real Silero VAD, a scripted wake word and a fake transcriber, in lock-step
// like an audio device: each 32 ms block, the player is read (what the
// speaker plays), the room mixes the user's voice with the agent's echo (what
// the mic hears), and the engine processes both. Ported from wonky-talkie's
// tests/harness.py.

const rate = audio.SampleRate

func wakePhrase(t *testing.T) []float32                  { return sim.WakePhrase(t) }
func command(t *testing.T) []float32                     { return sim.Command(t) }
func shortReply(t *testing.T) []float32                  { return sim.ShortReply(t) }
func agentVoice(t *testing.T, seconds float64) []float32 { return sim.AgentVoice(t, seconds) }

type options struct {
	wakeAt   []float64              // seconds
	stt      func([]float32) string // the transcriber's text for an utterance
	sttDelay time.Duration          // stream time a transcription takes
	echo     bool                   // the speaker's audio reaches the mic
	edit     func(*Config)          // adjust the defaults
}

type harness struct {
	t      *testing.T
	e      *Engine
	player *audio.Player
	room   *sim.Room
	noise  *sim.Noise
	log    []Event
	played []float32 // everything the speaker played
	stt    func([]float32) string
	delay  int64
	jobs   []pendingJob
}

type pendingJob struct {
	job Job
	due int64
}

func newHarness(t *testing.T, o options) *harness {
	t.Helper()
	vad := sim.VAD(t)
	cfg := Defaults()
	if o.edit != nil {
		o.edit(&cfg)
	}
	h := &harness{t: t, player: audio.NewPlayer(rate, 15*time.Millisecond), room: sim.NewRoom(o.echo),
		noise: sim.NewNoise(), stt: o.stt, delay: samples(o.sttDelay)}
	if h.stt == nil {
		h.stt = func([]float32) string { return "hello" }
	}
	wake := sim.NewWake(o.wakeAt...)
	var err error
	h.e, err = New(cfg, Deps{
		VAD:    vad,
		Wake:   wake,
		Player: h.player,
		Transcribe: func(j Job) {
			h.jobs = append(h.jobs, pendingJob{j, h.e.Position() + h.delay})
		},
		Emit: func(ev Event) { h.log = append(h.log, ev) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// silence returns room noise.
func (h *harness) silence(seconds float64) []float32 { return h.noise.Silence(seconds) }

// feed plays the user's audio into the mic, 32 ms at a time.
func (h *harness) feed(user []float32) {
	h.t.Helper()
	for start := 0; start < len(user); start += chunk {
		block := user[start:min(start+chunk, len(user))]
		ref := make([]float32, len(block))
		h.e.PlayerEvents(h.player.Read(ref))
		h.played = append(h.played, ref...)
		if err := h.e.Audio(context.Background(), h.room.Mix(block, ref), ref); err != nil {
			h.t.Fatal(err)
		}
		h.deliver()
	}
}

// deliver hands finished transcriptions back to the engine.
func (h *harness) deliver() {
	for len(h.jobs) > 0 && h.jobs[0].due <= h.e.Position() {
		j := h.jobs[0]
		h.jobs = h.jobs[1:]
		h.e.Transcribed(j.job, h.stt(j.job.Audio), nil, duration(h.delay))
	}
}

// play queues the agent's audio, as speaking a reply does.
func (h *harness) play(id string, samples []float32, text string, then audio.Then, timeout time.Duration) {
	h.player.Start(id)
	h.player.Add(id, samples, text)
	h.player.End(id, then, timeout)
}

// types lists the events so far, without state changes.
func (h *harness) types() []EventType {
	var out []EventType
	for _, e := range h.log {
		if e.Type != StateChanged {
			out = append(out, e.Type)
		}
	}
	return out
}

func (h *harness) states() []State {
	var out []State
	for _, e := range h.log {
		if e.Type == StateChanged {
			out = append(out, e.To)
		}
	}
	return out
}

// find returns the first event of type typ that match accepts.
func (h *harness) find(typ EventType, match func(Event) bool) Event {
	h.t.Helper()
	for _, e := range h.log {
		if e.Type == typ && (match == nil || match(e)) {
			return e
		}
	}
	h.t.Fatalf("no matching %s in %v", typ, h.types())
	return Event{}
}

func (h *harness) has(typ EventType) bool { return slices.Contains(h.types(), typ) }

func (h *harness) count(typ EventType) int {
	n := 0
	for _, e := range h.types() {
		if e == typ {
			n++
		}
	}
	return n
}

func text(s string) func(Event) bool { return func(e Event) bool { return e.Text == s } }

// replies returns a transcriber that answers with each text in turn.
func replies(texts ...string) func([]float32) string {
	return func([]float32) string {
		if len(texts) == 0 {
			return ""
		}
		t := texts[0]
		texts = texts[1:]
		return t
	}
}
