package talkie

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/androiddrew/talkie-core/internal/audio"
	"github.com/androiddrew/talkie-core/internal/engine"
)

// voice synthesizes text, delivering audio at the player's rate as it is
// made. It stops when ctx is cancelled or yield returns an error.
type voice interface {
	Speak(ctx context.Context, text string, yield func(samples []float32) error) error
}

// transcriber turns an utterance's 16 kHz audio into text.
type transcriber func(ctx context.Context, samples []float32) (string, error)

// conversation connects the engine to the agent: transcripts become turns,
// the agent's replies become speech, and barge-in cancels them.
//
// Its owner goroutine calls audio, and nothing else touches the engine:
// transcriptions and turns, which run on their own goroutines, post their
// results to the owner with post.
type conversation struct {
	ctx    context.Context
	eng    *engine.Engine
	player *audio.Player
	agent  Agent
	voice  voice
	stt    transcriber
	// async runs blocking work off the owner goroutine: agent turns and
	// their speech.
	async func(func())
	// background runs transcriptions; tests make it synchronous.
	background func(func())
	onEvent    func(engine.Event)

	mu    sync.Mutex
	inbox []func() // work posted for the owner goroutine
	wake  chan struct{}

	// Owner goroutine only.
	turn        *turn // the reply in progress
	last        *turn // the most recent reply, in progress or not
	turns       int
	interrupted *Interruption // for the next turn
}

type conversationConfig struct {
	engine engine.Config
	vad    engine.VAD
	wake   engine.Wake
	player *audio.Player
	agent  Agent
	voice  voice
	stt    transcriber
	// onEvent sees every engine event, on the owner goroutine.
	onEvent func(engine.Event)
}

func newConversation(ctx context.Context, c conversationConfig) (*conversation, error) {
	if c.agent == nil || c.voice == nil || c.stt == nil {
		return nil, errors.New("agent, voice and transcriber are required")
	}
	conv := &conversation{
		ctx: ctx, player: c.player, agent: c.agent, voice: c.voice, stt: c.stt,
		async: func(f func()) { go f() }, background: func(f func()) { go f() },
		onEvent: c.onEvent, wake: make(chan struct{}, 1),
	}
	var err error
	conv.eng, err = engine.New(c.engine, engine.Deps{
		VAD: c.vad, Wake: c.wake, Player: c.player,
		Transcribe: conv.transcribe, Emit: conv.event,
	})
	if err != nil {
		return nil, err
	}
	return conv, nil
}

// post queues f to run on the owner goroutine. It never blocks.
func (c *conversation) post(f func()) {
	c.mu.Lock()
	c.inbox = append(c.inbox, f)
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// drain runs the posted work, including work posted while it runs.
func (c *conversation) drain() {
	for {
		c.mu.Lock()
		work := c.inbox
		c.inbox = nil
		c.mu.Unlock()
		if len(work) == 0 {
			return
		}
		for _, f := range work {
			f()
		}
	}
}

// audio handles one block from the audio device: the player's notifications
// for the block it just played, then the microphone samples with what the
// speaker played as the echo reference.
func (c *conversation) audio(mic, ref []float32, played []audio.Event) error {
	c.drain()
	c.eng.PlayerEvents(played)
	if err := c.eng.Audio(c.ctx, mic, ref); err != nil {
		return err
	}
	c.drain()
	return nil
}

func (c *conversation) transcribe(job engine.Job) {
	c.background(func() {
		start := time.Now()
		text, err := c.stt(c.ctx, job.Audio)
		elapsed := time.Since(start)
		c.post(func() { c.eng.Transcribed(job, text, err, elapsed) })
	})
}

// event handles an engine event, on the owner goroutine.
func (c *conversation) event(ev engine.Event) {
	if c.onEvent != nil {
		c.onEvent(ev)
	}
	switch ev.Type {
	case engine.Transcript:
		c.startTurn(ev)
	case engine.InterruptionDetected:
		c.interrupt(ev.Playback)
	case engine.PlaybackFinished:
		// The reply played to the end: the turn is over. The speaker ends
		// the playback only after the agent has returned and every sentence
		// is in the player.
		if c.turn != nil && ev.Playback.ID == c.turn.id {
			c.turn = nil
		}
	case engine.SessionEnded:
		c.cancelTurn()
		c.interrupted = nil
	}
}

func (c *conversation) startTurn(ev engine.Event) {
	if c.turn != nil {
		// The user said more before the reply played, a continuation: the
		// new turn supersedes it.
		c.interrupt(audio.Progress{})
		if c.eng.State() == engine.Speaking {
			c.eng.StopPlayback("superseded")
		}
	}
	c.turns++
	ctx, cancel := context.WithCancel(c.ctx)
	t := &turn{
		id: fmt.Sprintf("reply_%d", c.turns), conv: c, ctx: ctx, cancel: cancel,
		then: ThenListen, more: make(chan struct{}, 1), done: make(chan struct{}),
	}
	c.turn, c.last = t, t
	c.player.Start(t.id)
	in := Turn{Session: ev.Session, Reply: t.id, Text: ev.Text, Trigger: Trigger(ev.Trigger), Interrupted: c.interrupted}
	c.interrupted = nil
	c.async(t.speak)
	c.async(func() { t.finish(c.agent.Respond(ctx, in, &Reply{turn: t})) })
}

// interrupt cancels the current turn and records how much of its reply was
// heard, from the playback progress when the user cut in.
func (c *conversation) interrupt(p audio.Progress) {
	t := c.turn
	if t == nil {
		return
	}
	t.cancel()
	c.turn = nil
	t.mu.Lock()
	unspoken := append([]string{p.Unspoken}, t.queue...)
	unspoken = append(unspoken, t.pending)
	t.mu.Unlock()
	c.interrupted = &Interruption{
		Heard: p.Heard, Cut: p.Cut, Unspoken: joinText(unspoken), Played: p.Played,
	}
}

func (c *conversation) cancelTurn() {
	if c.turn != nil {
		c.turn.cancel()
		c.turn = nil
	}
}

func joinText(parts []string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

// turn is one agent reply: the agent writes text, a speaker goroutine
// synthesizes it a sentence at a time into the player.
type turn struct {
	id     string
	conv   *conversation
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	pending  string   // text after the last complete sentence
	queue    []string // complete sentences not yet in the player
	then     Then
	finished bool // the agent returned
	spoke    bool // some audio reached the player
	more     chan struct{}
	done     chan struct{} // the speaker finished
}

// write adds a delta of the agent's reply.
func (t *turn) write(text string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ctx.Err(); err != nil {
		return err
	}
	if t.finished {
		return errors.New("talkie: write after Respond returned")
	}
	var complete []string
	complete, t.pending = sentences(t.pending+text, false)
	t.queue = append(t.queue, complete...)
	t.signal()
	return nil
}

func (t *turn) signal() {
	select {
	case t.more <- struct{}{}:
	default:
	}
}

// finish records that the agent returned; the rest of its text is a
// sentence.
func (t *turn) finish(err error) {
	t.mu.Lock()
	complete, _ := sentences(t.pending, true)
	t.queue = append(t.queue, complete...)
	t.pending = ""
	t.finished = true
	t.signal()
	t.mu.Unlock()
	if err != nil && !errors.Is(err, context.Canceled) {
		t.conv.post(func() {
			t.conv.event(engine.Event{Type: engine.Error, At: t.conv.eng.Position(), Session: t.conv.eng.Session(), Err: fmt.Errorf("agent: %w", err)})
		})
	}
}

// speak synthesizes the queued sentences in order until the agent is done
// or the turn is cancelled, then ends the playback item.
func (t *turn) speak() {
	defer close(t.done)
	for {
		t.mu.Lock()
		if len(t.queue) == 0 {
			finished := t.finished
			t.mu.Unlock()
			if finished {
				break
			}
			select {
			case <-t.more:
				continue
			case <-t.ctx.Done():
				return
			}
		}
		text := t.queue[0]
		t.mu.Unlock()
		if err := t.say(text); err != nil {
			return // cancelled, the player stopped, or the voice failed
		}
	}
	t.mu.Lock()
	then, spoke := t.then, t.spoke
	t.mu.Unlock()
	if spoke {
		t.conv.player.End(t.id, audio.Then(then), 0)
		return
	}
	// Nothing to play, so no playback will finish: act on then now.
	t.conv.post(func() {
		if t.conv.turn != t || t.conv.eng.State() != engine.Waiting {
			return
		}
		t.conv.turn = nil
		switch then {
		case ThenEnd:
			t.conv.eng.EndSession("agent")
		case ThenWait:
		default:
			_ = t.conv.eng.Listen(0, 0)
		}
	})
}

// say synthesizes one sentence into the player. The sentence leaves the
// queue when its first audio enters the player, under the turn's lock, so an
// interruption sees each sentence in exactly one place.
func (t *turn) say(text string) error {
	first := true
	err := t.conv.voice.Speak(t.ctx, text, func(samples []float32) error {
		t.mu.Lock()
		defer t.mu.Unlock()
		if err := t.ctx.Err(); err != nil {
			return err
		}
		label := ""
		if first {
			label = text
		}
		if !t.conv.player.Add(t.id, samples, label) {
			return errStopped
		}
		if first {
			first = false
			t.queue = t.queue[1:]
			t.spoke = true
		}
		return nil
	})
	if err == nil && first { // nothing to say, like "..."
		t.mu.Lock()
		t.queue = t.queue[1:]
		t.mu.Unlock()
	}
	return err
}

var errStopped = errors.New("playback stopped")

// Reply is where an agent writes its answer. Text is spoken as it arrives, a
// sentence at a time. Write returns an error once the reply is interrupted.
type Reply struct {
	turn *turn
}

// Write adds text to the reply.
func (r *Reply) Write(p []byte) (int, error) {
	if err := r.turn.write(string(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// WriteString adds text to the reply.
func (r *Reply) WriteString(s string) (int, error) {
	if err := r.turn.write(s); err != nil {
		return 0, err
	}
	return len(s), nil
}

// Then sets what the conversation does after the reply has played;
// ThenListen by default.
func (r *Reply) Then(then Then) {
	r.turn.mu.Lock()
	r.turn.then = then
	r.turn.mu.Unlock()
}
