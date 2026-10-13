package talkie

import (
	"context"
	"time"
)

// Agent answers what the user says. talkie calls Respond once per
// transcript, on its own goroutine, and speaks what the agent writes to the
// reply as it is written.
//
// ctx is cancelled when the user interrupts the reply (barge-in) or the
// session ends: stop generating and return. The next Turn's Interrupted then
// says how much of this reply the user heard.
type Agent interface {
	Respond(ctx context.Context, turn Turn, reply *Reply) error
}

// AgentFunc adapts a function to the Agent interface.
type AgentFunc func(ctx context.Context, turn Turn, reply *Reply) error

// Respond calls f.
func (f AgentFunc) Respond(ctx context.Context, turn Turn, reply *Reply) error {
	return f(ctx, turn, reply)
}

// Turn is one thing the user said.
type Turn struct {
	Session string  // the conversation it belongs to
	Text    string  // what the user said, with any leading wake phrase removed
	Trigger Trigger // what opened the utterance
	// Interrupted is set when the user cut off the previous reply to say
	// this. Agents that keep a history should replace the reply they wrote
	// with what was heard.
	Interrupted *Interruption
}

// Trigger is what opened an utterance.
type Trigger string

const (
	TriggerWake         Trigger = "wake_word"    // the user said the wake word
	TriggerFollowup     Trigger = "followup"     // the user answered after a reply
	TriggerCommand      Trigger = "command"      // push-to-talk
	TriggerBargeIn      Trigger = "barge_in"     // the user interrupted the agent
	TriggerContinuation Trigger = "continuation" // the user kept talking after a pause
)

// Interruption is how much of a reply the user heard before cutting it off.
// The three texts are the agent's own words, split at sentence boundaries.
type Interruption struct {
	Heard    string // sentences that played completely
	Cut      string // the sentence playing when the user interrupted
	Unspoken string // what the agent wrote that never played
	// Played is how long the reply played; zero when the user spoke while
	// the agent was still thinking.
	Played time.Duration
}

// Then is what the conversation does after a reply has played.
type Then string

const (
	// ThenListen opens a follow-up window: the user can answer without the
	// wake word. It is the default.
	ThenListen Then = "listen"
	// ThenEnd ends the session; the user needs the wake word again.
	ThenEnd Then = "end"
	// ThenWait waits for the agent to speak again, for example after "let
	// me check".
	ThenWait Then = "wait"
)
