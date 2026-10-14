package talkie

import (
	"time"

	"github.com/androiddrew/talkie-core/internal/audio"
	"github.com/androiddrew/talkie-core/internal/engine"
)

// Event is something that happened in the conversation, for an application
// to show or measure: Config.OnEvent receives them. Type says which fields
// are set.
type Event struct {
	Type EventType
	// Time is when it happened; At is the same moment in the audio stream,
	// since Run started.
	Time time.Time
	At   time.Duration
	// Session is the session it belongs to, empty outside one.
	Session string

	// State and From are the new and previous state, for EventState.
	State, From State
	// Trigger is what opened the session, listening window, utterance or
	// transcript, for EventSessionStarted, EventListening,
	// EventSpeechStarted and EventTranscript.
	Trigger Trigger
	// Reason says why, for EventSessionEnded, EventSpeechDiscarded and
	// EventReplyStopped.
	Reason string
	// Timeout is how long the listening window stays open, for
	// EventListening.
	Timeout time.Duration

	// Text is what the user said, without the wake phrase, for
	// EventTranscript; STT is how long transcribing it took, and Speech how
	// long the user spoke.
	Text        string
	STT, Speech time.Duration

	// Reply identifies the agent's reply, for the EventReply* events and
	// EventInterrupted; Heard, Cut, Unspoken and Played are how far it
	// played, as in Interruption.
	Reply                string
	Heard, Cut, Unspoken string
	Played               time.Duration
	// InterruptedBy and While are what interrupted the agent and what it
	// was doing, StateSpeaking or StateThinking, for EventInterrupted.
	InterruptedBy InterruptedBy
	While         State

	// Latency is the speaker-to-microphone round trip, for EventLatency
	// and EventRoomChanged; LatencySource says how it was measured, and
	// EchoGainDB is the echo's level.
	Latency       time.Duration
	LatencySource LatencySource
	EchoGainDB    float64

	// Err is what failed, for EventError.
	Err error
}

// EventType names an event.
type EventType string

const (
	EventSessionStarted  EventType = "session_started"  // Trigger
	EventSessionEnded    EventType = "session_ended"    // Reason: "timeout", "agent_timeout", "client", "agent"
	EventState           EventType = "state"            // State, From
	EventWake            EventType = "wake"             // the wake word was heard
	EventListening       EventType = "listening"        // Trigger, Timeout
	EventSpeechStarted   EventType = "speech_started"   // Trigger
	EventSpeechDiscarded EventType = "speech_discarded" // Reason: "too_short", "no_text", "stt_error", "session_ended"
	EventTranscript      EventType = "transcript"       // Text, Trigger, STT, Speech
	EventReplyStarted    EventType = "reply_started"    // Reply
	EventReplyFinished   EventType = "reply_finished"   // Reply, Heard, Played
	EventReplyStopped    EventType = "reply_stopped"    // Reply, Reason, Heard, Cut, Unspoken, Played
	EventInterrupted     EventType = "interrupted"      // InterruptedBy, While, and the reply's progress
	EventLatency         EventType = "latency"          // Latency, LatencySource, EchoGainDB
	EventRoomChanged     EventType = "room_changed"     // Latency: the room profile no longer fits
	EventError           EventType = "error"            // Err
)

// State is where the conversation is.
type State string

const (
	StateIdle         State = "idle"         // no session: only the wake word is listened for
	StateListening    State = "listening"    // waiting for the user to talk
	StateHearing      State = "hearing"      // the user is talking
	StateTranscribing State = "transcribing" // the user stopped; transcribing
	StateThinking     State = "thinking"     // the agent has the transcript and hasn't spoken yet
	StateSpeaking     State = "speaking"     // the agent's reply is playing
)

// InterruptedBy is what interrupted the agent.
type InterruptedBy string

const (
	InterruptedBySpeech InterruptedBy = "speech"    // the user talked over it
	InterruptedByWake   InterruptedBy = "wake_word" // the user said the wake word
	InterruptedByApp    InterruptedBy = "app"       // Talkie.Interrupt, as from a key
)

// LatencySource is how a latency was measured.
type LatencySource string

const (
	LatencyFromChime LatencySource = "chime" // the ready chime, before the first reply
	LatencyFromReply LatencySource = "reply" // the echo of a reply
)

var states = map[engine.State]State{
	engine.Idle: StateIdle, engine.Listening: StateListening, engine.Capturing: StateHearing,
	engine.Processing: StateTranscribing, engine.Waiting: StateThinking, engine.Speaking: StateSpeaking,
}

var interrupters = map[engine.Trigger]InterruptedBy{
	engine.TriggerSpeech: InterruptedBySpeech, engine.TriggerWake: InterruptedByWake, engine.TriggerManual: InterruptedByApp,
}

// publicEvent converts an engine event, reporting false for the engine's
// internal ones: barge-in suspicions, ignored wake words, silence detection
// (EventState covers it) and listen timeouts (EventSessionEnded follows).
func publicEvent(ev engine.Event) (Event, bool) {
	out := Event{At: time.Duration(ev.At) * time.Second / audio.SampleRate, Session: ev.Session}
	progress := func(p audio.Progress) {
		out.Reply, out.Heard, out.Cut, out.Unspoken, out.Played = p.ID, p.Heard, p.Cut, p.Unspoken, p.Played
	}
	switch ev.Type {
	case engine.SessionStarted:
		out.Type, out.Trigger = EventSessionStarted, Trigger(ev.Trigger)
	case engine.SessionEnded:
		out.Type, out.Reason = EventSessionEnded, ev.Reason
	case engine.StateChanged:
		out.Type, out.State, out.From = EventState, states[ev.To], states[ev.From]
	case engine.WakeDetected:
		out.Type = EventWake
	case engine.ListeningStarted:
		out.Type, out.Trigger, out.Timeout = EventListening, Trigger(ev.Trigger), ev.Timeout
	case engine.SpeechStarted:
		out.Type, out.Trigger = EventSpeechStarted, Trigger(ev.Trigger)
	case engine.UtteranceDiscarded:
		out.Type, out.Reason = EventSpeechDiscarded, ev.Reason
	case engine.Transcript:
		out.Type, out.Text, out.Trigger, out.STT, out.Speech = EventTranscript, ev.Text, Trigger(ev.Trigger), ev.STTTime, ev.AudioLength
	case engine.PlaybackStarted:
		out.Type = EventReplyStarted
		progress(ev.Playback)
	case engine.PlaybackFinished:
		out.Type = EventReplyFinished
		progress(ev.Playback)
	case engine.PlaybackStopped:
		out.Type, out.Reason = EventReplyStopped, ev.Reason
		progress(ev.Playback)
	case engine.InterruptionDetected:
		out.Type, out.InterruptedBy, out.While = EventInterrupted, interrupters[ev.Trigger], states[ev.AgentState]
		progress(ev.Playback)
	case engine.EchoMeasured:
		out.Type, out.Latency, out.LatencySource, out.EchoGainDB = EventLatency, ev.EchoDelay, LatencyFromReply, ev.EchoGainDB
	case engine.Error:
		out.Type, out.Err = EventError, ev.Err
	default:
		return Event{}, false
	}
	return out, true
}

// publish hands an event to Config.OnEvent, stamped with the time.
func (t *Talkie) publish(ev Event) {
	if t.cfg.OnEvent == nil {
		return
	}
	ev.Time = time.Now()
	t.cfg.OnEvent(ev)
}
