package engine

import (
	"time"

	"github.com/androiddrew/talkie-core/internal/audio"
)

// State is where the conversation is.
type State string

const (
	Idle       State = "idle"       // no session; only the wake word is listened for
	Listening  State = "listening"  // session open, waiting for the user to talk; times out
	Capturing  State = "capturing"  // the user is talking
	Processing State = "processing" // utterance finished; transcription running
	Waiting    State = "waiting"    // transcript delivered; the agent is thinking
	Speaking   State = "speaking"   // the agent's audio is playing; barge-in armed
)

// Trigger is what opened a session or an utterance.
type Trigger string

const (
	TriggerWake         Trigger = "wake_word"
	TriggerFollowup     Trigger = "followup"     // listening after the agent spoke
	TriggerCommand      Trigger = "command"      // Listen, push-to-talk
	TriggerBargeIn      Trigger = "barge_in"     // the user interrupted the agent
	TriggerContinuation Trigger = "continuation" // the user kept talking during transcription
	TriggerAgent        Trigger = "agent"        // the agent spoke first
	TriggerSpeech       Trigger = "speech"       // an interruption by voice
)

// EventType names an event.
type EventType string

const (
	StateChanged          EventType = "state_changed"
	SessionStarted        EventType = "session_started"
	SessionEnded          EventType = "session_ended"
	WakeDetected          EventType = "wake_detected"
	ListeningStarted      EventType = "listening"
	SpeechStarted         EventType = "speech_started"
	SilenceDetected       EventType = "silence_detected"
	Transcript            EventType = "transcript"
	UtteranceDiscarded    EventType = "utterance_discarded"
	ListenTimeout         EventType = "listen_timeout"
	PlaybackStarted       EventType = "playback_started"
	PlaybackFinished      EventType = "playback_finished"
	PlaybackStopped       EventType = "playback_stopped"
	InterruptionSuspected EventType = "interruption_suspected"
	InterruptionRejected  EventType = "interruption_rejected"
	InterruptionDetected  EventType = "interruption_detected"
	Error                 EventType = "error"
)

// Event is something that happened. At is the stream position, in 16 kHz
// samples; Session the session it belongs to. Other fields are set by the
// event types that use them.
type Event struct {
	Type    EventType
	At      int64
	Session string

	From, To State // StateChanged

	Trigger Trigger // SessionStarted, ListeningStarted, SpeechStarted, Transcript, InterruptionDetected
	Reason  string  // SessionEnded, SilenceDetected, UtteranceDiscarded, PlaybackStopped, InterruptionRejected

	Utterance   string        // SpeechStarted .. Transcript, UtteranceDiscarded
	SpeechStart int64         // SpeechStarted: first speech sample
	Text        string        // Transcript: cleaned text
	RawText     string        // Transcript, UtteranceDiscarded: the transcriber's text
	Audio       []float32     // Transcript: the utterance audio
	AudioLength time.Duration // SilenceDetected, Transcript
	SpeechTime  time.Duration // SilenceDetected, InterruptionSuspected, InterruptionDetected
	EndReason   string        // Transcript: why the utterance ended
	STTTime     time.Duration // Transcript

	Timeout time.Duration // ListeningStarted, ListenTimeout

	Playback   audio.Progress // Playback*, InterruptionDetected
	AgentState State          // InterruptionDetected: speaking or waiting (thinking)
	ExcessDB   float64        // InterruptionDetected, InterruptionRejected
	Voiced     float64        // InterruptionRejected

	Err error // Error
}
