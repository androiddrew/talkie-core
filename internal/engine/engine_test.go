package engine

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/androiddrew/talkie-core/internal/audio"
)

// Scenarios ported from wonky-talkie's tests/test_engine.py: real speech and
// the real Silero VAD, a scripted wake word and a fake transcriber. wonky's
// v1 cases, where the agent announced its own speech, play real audio here
// through a headset (no echo).

const wakeT = 1.95 // end of the wake phrase in the streams below

func wakeThenCommand(h *harness) {
	h.feed(h.silence(1.0))
	h.feed(wakePhrase(h.t))
	h.feed(h.silence(0.1))
	h.feed(command(h.t))
	h.feed(h.silence(1.5))
}

func TestWakeThenCommandSameBreath(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}, stt: replies("Hey Jarvis, turn on the office lights."),
		edit: func(c *Config) { c.Wake.StripPhrases = []string{"hey jarvis", "jarvis"} }})
	wakeThenCommand(h)
	tr := h.find(Transcript, nil)
	if tr.Text != "Turn on the office lights." || tr.Trigger != TriggerWake || tr.EndReason != "silence" {
		t.Errorf("transcript %q trigger %s end %s", tr.Text, tr.Trigger, tr.EndReason)
	}
	want := []EventType{SessionStarted, WakeDetected, ListeningStarted, SpeechStarted, SilenceDetected, Transcript}
	if !reflect.DeepEqual(h.types(), want) {
		t.Errorf("events %v, want %v", h.types(), want)
	}
	if want := []State{Listening, Capturing, Processing, Waiting}; !reflect.DeepEqual(h.states(), want) {
		t.Errorf("states %v, want %v", h.states(), want)
	}
	// The utterance starts at the wake point minus a little preroll, so the
	// wake phrase itself is not transcribed again.
	if tr.AudioLength >= 3300*time.Millisecond {
		t.Errorf("utterance audio %v includes the wake phrase", tr.AudioLength)
	}
}

func TestWakeThenPauseThenCommand(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}, stt: replies("turn on the office lights")})
	h.feed(h.silence(1.0))
	h.feed(wakePhrase(t))
	h.feed(h.silence(2.0)) // the user pauses after the wake word
	if h.e.State() != Listening {
		t.Fatalf("state %s after the pause", h.e.State())
	}
	h.feed(command(t))
	h.feed(h.silence(1.5))
	if tr := h.find(Transcript, nil); tr.Text != "turn on the office lights" {
		t.Errorf("transcript %q", tr.Text)
	}
	if n := h.count(SpeechStarted); n != 1 {
		t.Errorf("%d utterances, want 1", n)
	}
}

func TestNoSpeechAfterWakeTimesOut(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}, edit: func(c *Config) { c.Session.AfterWakeTimeout = 2 * time.Second }})
	h.feed(h.silence(1.0))
	h.feed(wakePhrase(t))
	h.feed(h.silence(2.5))
	h.find(ListenTimeout, nil)
	if ended := h.find(SessionEnded, nil); ended.Reason != "timeout" || h.e.State() != Idle {
		t.Errorf("ended %q, state %s", ended.Reason, h.e.State())
	}
}

func TestSpeechWithoutWakeIsIgnored(t *testing.T) {
	h := newHarness(t, options{})
	h.feed(h.silence(0.5))
	h.feed(command(t))
	h.feed(h.silence(1.5))
	if len(h.types()) != 0 || h.e.State() != Idle {
		t.Errorf("events %v, state %s", h.types(), h.e.State())
	}
}

func TestEmptyTranscriptKeepsListening(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}, stt: replies("Hey Jarvis."),
		edit: func(c *Config) { c.Wake.StripPhrases = []string{"hey jarvis"} }})
	wakeThenCommand(h)
	if d := h.find(UtteranceDiscarded, nil); d.Reason != "no_text" || d.RawText != "Hey Jarvis." {
		t.Errorf("discarded %+v", d)
	}
	h.feed(h.silence(0.1))
	if h.e.State() != Listening || h.has(Transcript) {
		t.Errorf("state %s, events %v", h.e.State(), h.types())
	}
}

func TestMaxUtteranceLength(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}, edit: func(c *Config) { c.Session.MaxUtterance = time.Second }})
	wakeThenCommand(h)
	if s := h.find(SilenceDetected, nil); s.Reason != "max_duration" {
		t.Errorf("utterance ended by %q", s.Reason)
	}
}

func TestPushToTalkListen(t *testing.T) {
	h := newHarness(t, options{stt: replies("what's the weather")})
	h.feed(h.silence(0.5))
	if err := h.e.Listen(5*time.Second, 0); err != nil {
		t.Fatal(err)
	}
	if s := h.find(SessionStarted, nil); s.Trigger != TriggerCommand {
		t.Errorf("session trigger %s", s.Trigger)
	}
	h.feed(command(t))
	h.feed(h.silence(1.5))
	if tr := h.find(Transcript, nil); tr.Trigger != TriggerCommand {
		t.Errorf("transcript trigger %s", tr.Trigger)
	}
	if err := h.e.Listen(0, 0); err != nil {
		t.Errorf("Listen while waiting: %v", err)
	}
}

// ------------------------------------------------------------ conversation

func TestFollowupWithoutWakeWord(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}, stt: replies("Turn on the lights.", "The office.")})
	wakeThenCommand(h)
	h.play("q1", agentVoice(t, 1.0), "Which room?", audio.ThenListen, 6*time.Second)
	h.feed(h.silence(0.1))
	if h.e.State() != Speaking {
		t.Fatalf("state %s while the agent asks", h.e.State())
	}
	h.feed(h.silence(1.4))
	if h.e.State() != Listening {
		t.Fatalf("state %s after the question", h.e.State())
	}
	h.feed(shortReply(t))
	h.feed(h.silence(1.5))
	if tr := h.find(Transcript, text("The office.")); tr.Trigger != TriggerFollowup {
		t.Errorf("follow-up trigger %s", tr.Trigger)
	}
	if n := h.count(SessionStarted); n != 1 {
		t.Errorf("%d sessions", n)
	}
	h.e.EndSession("client")
	if ended := h.find(SessionEnded, nil); ended.Reason != "client" {
		t.Errorf("ended %q", ended.Reason)
	}
}

func TestFollowupWindowTimesOut(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}})
	wakeThenCommand(h)
	h.play("q1", agentVoice(t, 0.5), "Anything else?", audio.ThenListen, 1500*time.Millisecond)
	h.feed(h.silence(2.5))
	if ended := h.find(SessionEnded, nil); ended.Reason != "timeout" {
		t.Errorf("ended %q", ended.Reason)
	}
}

func TestThenEndClosesSessionAfterAudio(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}})
	wakeThenCommand(h)
	h.play("a1", agentVoice(t, 2.0), "It's three o'clock.", audio.ThenEnd, 0)
	h.feed(h.silence(1.0))
	if h.e.State() != Speaking {
		t.Fatalf("state %s mid-reply", h.e.State())
	}
	h.feed(h.silence(1.5))
	if ended := h.find(SessionEnded, nil); ended.Reason != "agent" {
		t.Errorf("ended %q", ended.Reason)
	}
}

func TestAgentStartsSessionBySpeaking(t *testing.T) {
	h := newHarness(t, options{})
	h.play("t1", agentVoice(t, 1.0), "Your timer is done.", audio.ThenEnd, 0)
	h.feed(h.silence(0.1))
	if s := h.find(SessionStarted, nil); s.Trigger != TriggerAgent || h.e.State() != Speaking {
		t.Errorf("session trigger %s, state %s", s.Trigger, h.e.State())
	}
}

func TestStreamedAudioWithUnderrun(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}})
	wakeThenCommand(h)
	v := agentVoice(t, 3.0)
	h.player.Start("s1")
	h.player.Add("s1", v[:rate], "The first part.")
	h.feed(h.silence(1.5)) // half a second of underrun
	if h.e.State() != Speaking {
		t.Fatalf("state %s during the underrun", h.e.State())
	}
	h.player.Add("s1", v[rate:], "The rest.")
	h.player.End("s1", audio.ThenWait, 0)
	h.feed(h.silence(2.5))
	fin := h.find(PlaybackFinished, nil)
	if d := fin.Playback.Played - 3*time.Second; d < -50*time.Millisecond || d > 50*time.Millisecond {
		t.Errorf("played %v, want 3s", fin.Playback.Played)
	}
	if fin.Playback.Heard != "The first part. The rest." || h.e.State() != Waiting {
		t.Errorf("heard %q, state %s", fin.Playback.Heard, h.e.State())
	}
}

func TestAgentStopPlayback(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}})
	wakeThenCommand(h)
	h.play("a1", agentVoice(t, 6.0), "A long answer.", audio.ThenListen, 0)
	h.feed(h.silence(1.0))
	h.e.StopPlayback("client")
	if s := h.find(PlaybackStopped, nil); s.Reason != "client" || h.e.State() != Waiting {
		t.Errorf("stopped %q, state %s", s.Reason, h.e.State())
	}
}

// ---------------------------------------------------------------- barge-in

// longAnswer is the agent's 8 s reply in three sentences, so an
// interruption splits it into heard, cut and unspoken text.
func longAnswer(h *harness, id string) {
	v := agentVoice(h.t, 8.0)
	h.player.Start(id)
	h.player.Add(id, v[:2*rate], "Once upon a time,")
	h.player.Add(id, v[2*rate:5*rate], "there was a little robot who loved to listen.")
	h.player.Add(id, v[5*rate:], "It listened to the rain every night.")
	h.player.End(id, audio.ThenListen, 0)
}

func TestSpeechInterruptsAgent(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}, stt: replies("Tell me a story.", "Stop, that's enough.")})
	wakeThenCommand(h)
	longAnswer(h, "s1")
	h.feed(h.silence(2.6))
	h.feed(command(t)) // the user talks over the agent
	ev := h.find(InterruptionDetected, nil)
	if ev.Trigger != TriggerSpeech || ev.AgentState != Speaking || ev.Playback.ID != "s1" {
		t.Errorf("interruption %+v", ev)
	}
	// About 2.6 s plus the time to confirm had played: the first sentence
	// was heard, the second cut off, the third never spoken.
	if p := ev.Playback; p.Heard != "Once upon a time," || p.Cut != "there was a little robot who loved to listen." ||
		p.Unspoken != "It listened to the rain every night." {
		t.Errorf("interrupted at %v: heard %q, cut %q, unspoken %q", p.Played, p.Heard, p.Cut, p.Unspoken)
	}
	h.feed(h.silence(1.5))
	tr := h.find(Transcript, text("Stop, that's enough."))
	if tr.Trigger != TriggerBargeIn || tr.AudioLength <= 2*time.Second {
		t.Errorf("interruption transcript trigger %s, audio %v", tr.Trigger, tr.AudioLength)
	}
	if h.player.Active() || h.has(PlaybackFinished) {
		t.Error("the agent's audio kept playing")
	}
}

func TestFastBargeInCapturesWholeInterruption(t *testing.T) {
	// Barge-in fires before the endpointer would register an onset; the
	// capture must still run until the user stops.
	h := newHarness(t, options{wakeAt: []float64{wakeT}, stt: replies("Tell me a story.", "Stop."),
		edit: func(c *Config) { c.BargeIn.MinSpeech = 96 * time.Millisecond; c.VAD.MinSpeech = 400 * time.Millisecond }})
	wakeThenCommand(h)
	longAnswer(h, "s1")
	h.feed(h.silence(0.6))
	h.feed(command(t))
	h.feed(h.silence(1.5))
	if tr := h.find(Transcript, text("Stop.")); tr.AudioLength <= 2*time.Second {
		t.Errorf("interruption audio %v", tr.AudioLength)
	}
}

func TestGracePeriodIgnoresSpeechRightAfterAgentStarts(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}, edit: func(c *Config) { c.BargeIn.Grace = 5 * time.Second }})
	wakeThenCommand(h)
	longAnswer(h, "s1")
	h.feed(command(t))
	h.feed(h.silence(1.0))
	if h.has(InterruptionDetected) || h.e.State() != Speaking {
		t.Errorf("events %v, state %s", h.types(), h.e.State())
	}
}

func TestWakeWordOnlyBargeIn(t *testing.T) {
	speakStart := 1.0 + 0.952 + 0.1 + 2.7 + 1.5 // after wakeThenCommand
	wake2 := speakStart + 0.3 + 2.7 + 0.2 + 0.952
	h := newHarness(t, options{wakeAt: []float64{wakeT, wake2}, stt: replies("Tell me a story.", "Stop."),
		edit: func(c *Config) { c.BargeIn.Speech = false }})
	wakeThenCommand(h)
	longAnswer(h, "s1")
	h.feed(h.silence(0.3))
	h.feed(command(t)) // speech alone must not interrupt
	h.feed(h.silence(0.2))
	if h.has(InterruptionDetected) {
		t.Fatal("speech interrupted with only the wake word enabled")
	}
	h.feed(wakePhrase(t))
	h.feed(h.silence(0.3))
	if ev := h.find(InterruptionDetected, nil); ev.Trigger != TriggerWake || h.player.Active() {
		t.Errorf("interruption %s, player active %v", ev.Trigger, h.player.Active())
	}
	h.feed(shortReply(t))
	h.feed(h.silence(1.5))
	if tr := h.find(Transcript, text("Stop.")); tr.Trigger != TriggerBargeIn {
		t.Errorf("transcript trigger %s", tr.Trigger)
	}
}

func TestBargeInDisabledIsHalfDuplex(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}, edit: func(c *Config) { c.BargeIn.Enabled = false }})
	wakeThenCommand(h)
	longAnswer(h, "s1")
	h.feed(h.silence(0.5))
	h.feed(command(t))
	h.feed(h.silence(1.0))
	if h.has(InterruptionDetected) {
		t.Error("interrupted with barge-in disabled")
	}
}

// TestAgentSayingWakeWordIsIgnored: a wake detection while the agent says
// the wake word is its own echo; once it has moved on, the wake word
// interrupts again.
func TestAgentSayingWakeWordIsIgnored(t *testing.T) {
	speakStart := 1.0 + 0.952 + 0.1 + 2.7 + 1.5 // after wakeThenCommand
	h := newHarness(t, options{wakeAt: []float64{wakeT, speakStart + 3.5, speakStart + 7.5},
		edit: func(c *Config) { c.Wake.StripPhrases = []string{"hey computer"} }})
	wakeThenCommand(h)
	v := agentVoice(t, 8.0)
	h.player.Start("s1")
	h.player.Add("s1", v[:2*rate], "Once upon a time,")
	h.player.Add("s1", v[2*rate:5*rate], "Just say hey computer to wake me.")
	h.player.Add("s1", v[5*rate:], "It listened to the rain every night.")
	h.player.End("s1", audio.ThenListen, 0)
	h.feed(h.silence(5.0))
	if h.has(InterruptionDetected) || h.find(WakeIgnored, nil).Reason != "agent_said_it" {
		t.Fatalf("events %v", h.types())
	}
	h.feed(h.silence(3.0))
	if ev := h.find(InterruptionDetected, nil); ev.Trigger != TriggerWake {
		t.Errorf("events %v", h.types())
	}
}

// TestBargeInDisabledWhileThinking: with barge-in off, neither speech nor
// the wake word interrupts the agent while it thinks.
func TestBargeInDisabledWhileThinking(t *testing.T) {
	waiting := 1.0 + 0.952 + 0.1 + 2.7 + 1.5 // after wakeThenCommand
	h := newHarness(t, options{wakeAt: []float64{wakeT, waiting + 1.0}, edit: func(c *Config) { c.BargeIn.Enabled = false }})
	wakeThenCommand(h)
	h.feed(h.silence(0.5))
	h.feed(shortReply(t))
	h.feed(h.silence(1.0))
	if h.has(InterruptionDetected) || h.e.State() != Waiting {
		t.Errorf("events %v, state %s", h.types(), h.e.State())
	}
}

// TestInterruptWhileSpeaking: the application's interrupt, as from a key,
// stops the agent even with barge-in off, and listens to the user without
// the wake word.
func TestInterruptWhileSpeaking(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}, echo: true, stt: replies("Tell me a story.", "Stop."),
		edit: func(c *Config) { c.BargeIn.Enabled = false }})
	wakeThenCommand(h)
	longAnswer(h, "s1")
	h.feed(h.silence(2.6))
	h.e.Interrupt()
	ev := h.find(InterruptionDetected, nil)
	if ev.Trigger != TriggerManual || ev.AgentState != Speaking || ev.Playback.Heard != "Once upon a time," {
		t.Errorf("interruption %+v", ev)
	}
	if h.player.Active() || h.e.State() != Listening {
		t.Errorf("player active %v, state %s", h.player.Active(), h.e.State())
	}
	h.feed(h.silence(0.5))
	if h.e.State() != Listening {
		t.Fatalf("state %s before the user spoke", h.e.State())
	}
	h.feed(shortReply(t))
	h.feed(h.silence(1.5))
	if tr := h.find(Transcript, text("Stop.")); tr.Trigger != TriggerBargeIn {
		t.Errorf("transcript trigger %s", tr.Trigger)
	}
}

func TestInterruptWhileThinking(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}})
	wakeThenCommand(h)
	h.e.Interrupt()
	if ev := h.find(InterruptionDetected, nil); ev.Trigger != TriggerManual || ev.AgentState != Waiting || h.e.State() != Listening {
		t.Errorf("interruption %+v, state %s", ev, h.e.State())
	}
}

// TestInterruptIdle: with nothing to interrupt, it listens, as push-to-talk
// does; while the user talks, it does nothing.
func TestInterruptIdle(t *testing.T) {
	h := newHarness(t, options{stt: replies("Lights on.")})
	h.feed(h.silence(0.5))
	h.e.Interrupt()
	if !h.has(SessionStarted) || h.e.State() != Listening {
		t.Fatalf("events %v, state %s", h.types(), h.e.State())
	}
	h.feed(command(t)[:rate])
	h.e.Interrupt()
	if h.e.State() != Capturing {
		t.Errorf("state %s while the user talks", h.e.State())
	}
	h.feed(command(t)[rate:])
	h.feed(h.silence(1.5))
	if tr := h.find(Transcript, text("Lights on.")); tr.Trigger != TriggerCommand {
		t.Errorf("transcript trigger %s", tr.Trigger)
	}
}

func TestUserSpeaksWhileAgentThinks(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}, stt: replies("Turn on the lights.", "Actually, never mind.")})
	wakeThenCommand(h)
	if h.e.State() != Waiting {
		t.Fatalf("state %s", h.e.State())
	}
	h.feed(h.silence(0.5))
	h.feed(shortReply(t))
	if ev := h.find(InterruptionDetected, nil); ev.AgentState != Waiting {
		t.Errorf("interrupted while %s", ev.AgentState)
	}
	h.feed(h.silence(1.5))
	if tr := h.find(Transcript, text("Actually, never mind.")); tr.Trigger != TriggerBargeIn {
		t.Errorf("transcript trigger %s", tr.Trigger)
	}
}

func TestUserKeepsTalkingWhileTranscribing(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}, stt: replies("first part", "second part"), sttDelay: time.Second})
	h.feed(h.silence(1.0))
	h.feed(wakePhrase(t))
	h.feed(h.silence(0.1))
	h.feed(command(t))
	h.feed(h.silence(0.9))
	h.find(SilenceDetected, nil)
	h.feed(shortReply(t)) // continues during transcription
	h.feed(h.silence(2.5))
	first, second := h.find(Transcript, text("first part")), h.find(Transcript, text("second part"))
	if first.Trigger != TriggerWake || second.Trigger != TriggerContinuation || h.e.State() != Waiting {
		t.Errorf("triggers %s, %s; state %s", first.Trigger, second.Trigger, h.e.State())
	}
}

// ------------------------------------------------------------------- echo
// From wonky-talkie's tests/test_echo.py: the agent's own voice coming back
// through the speaker must not count as the user interrupting, and the user
// talking over the agent still must. The room delays the agent's audio
// 80 ms, adds reverb and a little distortion, and plays it 6 dB down into
// the mic. Each protection is tested alone and together: the echo canceller
// (go-speexdsp) and duck-and-verify.

func noCanceller(c *Config) { c.Echo.Enabled = false }

func startConversation(h *harness) {
	wakeThenCommand(h)
	h.find(Transcript, nil)
}

func TestEchoInterruptsWithoutProtection(t *testing.T) {
	// Echo, no cancellation and no verification: the agent interrupts itself,
	// the bug both protections exist for.
	h := newHarness(t, options{wakeAt: []float64{wakeT}, echo: true, stt: replies("Tell me a story.", "One second."),
		edit: func(c *Config) { c.BargeIn.Verify = false; noCanceller(c) }})
	startConversation(h)
	h.play("s1", agentVoice(t, 8.0), "Once upon a time...", audio.ThenListen, 0)
	h.feed(h.silence(8.5))
	if !h.has(InterruptionDetected) {
		t.Error("the agent's echo did not interrupt it; the room is too quiet to test verification")
	}
}

// TestVerifyRejectsEcho: without the canceller, duck-and-verify alone
// catches the echo.
func TestVerifyRejectsEcho(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}, echo: true, stt: replies("Tell me a story."), edit: noCanceller})
	startConversation(h)
	h.play("s1", agentVoice(t, 8.0), "Once upon a time...", audio.ThenListen, 0)
	h.feed(h.silence(9.0))
	if h.has(InterruptionDetected) {
		t.Fatalf("the agent interrupted itself: %v", h.types())
	}
	if !h.has(InterruptionSuspected) {
		t.Error("the echo was never suspected; the test exercises nothing")
	}
	if rej := h.find(InterruptionRejected, nil); rej.ExcessDB >= 6 {
		t.Errorf("rejected with %.1f dB excess", rej.ExcessDB)
	}
	fin := h.find(PlaybackFinished, nil)
	if d := fin.Playback.Played - 8*time.Second; d < -50*time.Millisecond || d > 50*time.Millisecond {
		t.Errorf("played %v of 8s", fin.Playback.Played)
	}
	if l := h.find(ListeningStarted, func(e Event) bool { return e.Trigger == TriggerFollowup }); l.Trigger != TriggerFollowup {
		t.Error("no follow-up window")
	}
}

func TestHeadsetNoSuspicion(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}, stt: replies("Tell me a story.")})
	startConversation(h)
	h.play("s1", agentVoice(t, 8.0), "Once upon a time...", audio.ThenListen, 0)
	h.feed(h.silence(9.0))
	if h.has(InterruptionSuspected) || h.has(InterruptionDetected) {
		t.Errorf("events %v", h.types())
	}
}

func TestUserInterruptsOverEcho(t *testing.T) {
	t.Run("canceller", func(t *testing.T) { userInterruptsOverEcho(t, nil) })
	t.Run("verify alone", func(t *testing.T) { userInterruptsOverEcho(t, noCanceller) })
}

func userInterruptsOverEcho(t *testing.T, edit func(*Config)) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}, echo: true, stt: replies("Tell me a story.", "Stop, that's enough."), edit: edit})
	startConversation(h)
	longAnswer(h, "s1")
	h.feed(h.silence(3.0)) // the agent talks alone for 3 s
	h.feed(command(t))     // then the user talks over it
	ev := h.find(InterruptionDetected, nil)
	if ev.Trigger != TriggerSpeech || ev.Playback.ID != "s1" || ev.ExcessDB < 6 {
		t.Errorf("interruption %+v", ev)
	}
	if ev.Playback.Played <= 2900*time.Millisecond || ev.Playback.Played >= 4500*time.Millisecond {
		t.Errorf("interrupted after %v", ev.Playback.Played)
	}
	if s := h.find(PlaybackStopped, nil); s.Reason != "interrupted" || h.player.Active() {
		t.Errorf("stopped %q, player active %v", s.Reason, h.player.Active())
	}
	h.feed(h.silence(1.5))
	tr := h.find(Transcript, text("Stop, that's enough."))
	if tr.Trigger != TriggerBargeIn || tr.AudioLength <= 2*time.Second || h.has(PlaybackFinished) {
		t.Errorf("transcript trigger %s, audio %v; events %v", tr.Trigger, tr.AudioLength, h.types())
	}
	t.Logf("interrupted after %v with %.1f dB over the echo; heard %q, cut %q",
		ev.Playback.Played, ev.ExcessDB, ev.Playback.Heard, ev.Playback.Cut)
}

func TestWakeWordStopsPlaybackImmediately(t *testing.T) {
	wake2 := 1.0 + 0.952 + 0.1 + 2.7 + 1.5 + 2.0 + 0.952
	h := newHarness(t, options{wakeAt: []float64{wakeT, wake2}, echo: true, stt: replies("Tell me a story.", "Stop."),
		edit: func(c *Config) { c.BargeIn.Speech = false }})
	startConversation(h)
	longAnswer(h, "s1")
	h.feed(h.silence(2.0))
	h.feed(wakePhrase(t))
	h.feed(h.silence(0.2))
	if ev := h.find(InterruptionDetected, nil); ev.Trigger != TriggerWake || h.player.Active() {
		t.Errorf("interruption %s, player active %v", ev.Trigger, h.player.Active())
	}
	if !strings.HasPrefix(h.find(PlaybackStopped, nil).Playback.Heard, "Once upon a time,") {
		t.Error("the first sentence was not reported heard")
	}
}

// TestCancellerAndVerifyNoSelfInterruption is the default configuration:
// the agent talks alone in an echoing room for 8 s without interrupting
// itself, and the canceller reports how much echo it removed.
func TestCancellerAndVerifyNoSelfInterruption(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}, echo: true, stt: replies("Tell me a story.")})
	startConversation(h)
	h.play("s1", agentVoice(t, 8.0), "Once upon a time...", audio.ThenListen, 0)
	h.feed(h.silence(9.0))
	if h.has(InterruptionDetected) {
		t.Fatalf("the agent interrupted itself: %v", h.types())
	}
	fin := h.find(PlaybackFinished, nil)
	if fin.ERLE < 10 {
		t.Errorf("the canceller removed %.1f dB of echo, want at least 10", fin.ERLE)
	}
	t.Logf("echo removed: %.1f dB; suspicions: %d, all rejected", fin.ERLE, h.count(InterruptionSuspected))
}

// TestCancellerAloneIsNotEnoughAtFirst: the canceller needs time to learn
// the room, so without verification the start of the first reply still
// interrupts itself. This is why verification stays on.
func TestCancellerAloneIsNotEnoughAtFirst(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}, echo: true, stt: replies("Tell me a story.", "x"),
		edit: func(c *Config) { c.BargeIn.Verify = false }})
	startConversation(h)
	h.play("s1", agentVoice(t, 8.0), "Once upon a time...", audio.ThenListen, 0)
	h.feed(h.silence(8.5))
	ev := h.find(InterruptionDetected, nil)
	t.Logf("self-interrupted after %v of playback", ev.Playback.Played)
}

// TestInterruptRightAtStartOfReply: the canceller has not converged and the
// user speaks at once; it is still an interruption.
func TestInterruptRightAtStartOfReply(t *testing.T) {
	h := newHarness(t, options{wakeAt: []float64{wakeT}, echo: true, stt: replies("Tell me a story.", "No, wait.")})
	startConversation(h)
	longAnswer(h, "s1")
	h.feed(h.silence(0.4))
	h.feed(command(t))
	h.feed(h.silence(1.5))
	if tr := h.find(Transcript, text("No, wait.")); tr.Trigger != TriggerBargeIn {
		t.Errorf("transcript trigger %s", tr.Trigger)
	}
}
