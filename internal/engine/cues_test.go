package engine

import (
	"slices"
	"testing"
	"time"

	"github.com/androiddrew/talkie-core/internal/audio"
)

// withCues turns on the built-in cues, at the harness's 16 kHz.
func withCues(c *Config) {
	c.Cues = CuesConfig{
		Listening: audio.ListeningSound(rate), Idle: audio.IdleSound(rate),
		Thinking: audio.ThinkingSound(rate), ThinkingDelay: 500 * time.Millisecond,
	}
}

// at finds sound in what the speaker played, exactly: nothing else plays
// while a cue does in these tests. It returns the sample it starts at.
func (h *harness) at(sound []float32, from int) int {
	h.t.Helper()
	for i := max(0, from); i+len(sound) <= len(h.played); i++ {
		if slices.Equal(h.played[i:i+len(sound)], sound) {
			return i
		}
	}
	return -1
}

// TestCuesInAConversation: the listening cue plays when the wake word is
// heard, over the command spoken in the same breath, which is still heard
// whole; the thinking loop plays from half a second after the user stops
// until the reply starts; the idle cue plays when the session ends.
func TestCuesInAConversation(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{1.6}, echo: true, sttDelay: 300 * time.Millisecond,
		stt: replies("turn on the office lights"), edit: withCues})
	h.feed(h.silence(1.0))
	h.feed(wakePhrase(t))
	h.feed(command(t))
	h.feed(h.silence(4.5)) // the agent thinks, for two periods of the loop
	h.play("r1", shortReply(t), "Sure.", audio.ThenEnd, 0)
	h.feed(h.silence(3.0))

	wake := h.find(WakeDetected, nil)
	listening := h.at(h.e.cfg.Cues.Listening, 0)
	if listening < 0 || listening < int(wake.At)-chunk || listening > int(wake.At)+2*chunk {
		t.Errorf("listening cue at %d, wake word heard at %d", listening, wake.At)
	}
	if tr := h.find(Transcript, nil); tr.Text != "turn on the office lights" || h.has(UtteranceDiscarded) {
		t.Errorf("command: %+v, events %v", tr, h.types())
	}

	silence := h.find(SilenceDetected, nil)
	started := h.find(PlaybackStarted, nil)
	loop := h.e.cfg.Cues.Thinking
	first := h.at(loop, 0)
	wantFrom := int(silence.At) + int(samples(500*time.Millisecond))
	if first < 0 || first < wantFrom || first > wantFrom+2*chunk {
		t.Errorf("thinking loop starts at %d, want about %d (silence at %d)", first, wantFrom, silence.At)
	}
	// It loops seamlessly, and stops when the reply starts.
	if next := h.at(loop, first+1); next != first+len(loop) {
		t.Errorf("second period at %d, want %d", next, first+len(loop))
	}
	if h.at(loop, int(started.At)) >= 0 {
		t.Error("the thinking loop kept playing over the reply")
	}

	ended := h.find(SessionEnded, nil)
	idle := h.at(h.e.cfg.Cues.Idle, int(started.At))
	if ended.Reason != "agent" || idle < int(ended.At)-chunk || idle > int(ended.At)+2*chunk {
		t.Errorf("idle cue at %d, session ended at %d (%s)", idle, ended.At, ended.Reason)
	}
}

// TestListeningCueIsNotSpeech: the listening cue's echo, before the echo
// canceller has learned anything, doesn't open an utterance.
func TestListeningCueIsNotSpeech(t *testing.T) {
	for _, echo := range []bool{true, false} {
		h := newHarness(t, options{wakeAt: []float64{1.6}, echo: echo, edit: withCues})
		h.feed(h.silence(1.0))
		h.feed(wakePhrase(t))
		h.feed(h.silence(6.0))
		if h.has(SpeechStarted) || !h.has(ListenTimeout) {
			t.Errorf("echo %v: %v", echo, h.types())
		}
		if h.at(h.e.cfg.Cues.Listening, 0) < 0 || h.at(h.e.cfg.Cues.Idle, 0) < 0 {
			t.Errorf("echo %v: the cues didn't play", echo)
		}
	}
}

// TestThinkingLoopStopsWhenInterrupted: interrupting the agent while it
// thinks stops the loop and plays the listening cue.
func TestThinkingLoopStopsWhenInterrupted(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{1.6}, edit: withCues})
	h.feed(h.silence(1.0))
	h.feed(wakePhrase(t))
	h.feed(command(t))
	h.feed(h.silence(3.0))
	if !h.player.Looping() {
		t.Fatal("not looping while the agent thinks")
	}
	from := len(h.played)
	h.e.Interrupt()
	h.feed(h.silence(0.5))
	// The loop fades out over the player's 15 ms ramp under the start of the
	// listening cue; after that, the cue plays alone.
	cue, ramp := h.e.cfg.Cues.Listening, int(samples(15*time.Millisecond))
	if h.player.Looping() || !slices.Equal(h.played[from+ramp:from+len(cue)], cue[ramp:]) {
		t.Errorf("after Interrupt: looping %v, or the listening cue didn't follow", h.player.Looping())
	}
}

// TestNoCuesByDefault: without cues nothing plays but the agent.
func TestNoCuesByDefault(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{1.6}})
	h.feed(h.silence(1.0))
	h.feed(wakePhrase(t))
	h.feed(command(t))
	h.feed(h.silence(6.0))
	for i, s := range h.played {
		if s != 0 {
			t.Fatalf("sample %d played %v", i, s)
		}
	}
}
