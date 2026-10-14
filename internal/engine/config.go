package engine

import (
	"errors"
	"time"
)

// Config tunes the conversation. Durations are rounded to whole samples or
// 32 ms VAD chunks. Start from Defaults.
type Config struct {
	VAD     VADConfig
	Session SessionConfig
	Wake    WakeConfig
	BargeIn BargeInConfig
	Echo    EchoConfig
	// IOLatency is the audio device's microphone-plus-speaker round trip. The
	// barge-in verify window and the post-speech holdoff wait it out.
	IOLatency time.Duration
}

// VADConfig sets endpointing.
type VADConfig struct {
	Threshold    float32 // probability at or above which a chunk is speech
	NegThreshold float32 // probability below which a chunk is silence
	MinSpeech    time.Duration
	EndSilence   time.Duration // silence that ends an utterance
	Preroll      time.Duration // audio kept from before the detected onset
	// MinUtterance discards utterances with less detected speech, unheard.
	MinUtterance time.Duration
}

// SessionConfig sets session timeouts.
type SessionConfig struct {
	AfterWakeTimeout time.Duration // wait for the command after a wake word
	FollowupTimeout  time.Duration // default listening window after the agent speaks
	MaxUtterance     time.Duration // hard cap on one utterance
	// AgentTimeout ends the session when the agent does nothing for this
	// long after a transcript; zero disables it.
	AgentTimeout time.Duration
	// PostSpeechHoldoff ignores speech onsets this long after the agent
	// stops, so the tail of its own voice doesn't open an utterance.
	PostSpeechHoldoff time.Duration
}

// WakeConfig sets wake word handling.
type WakeConfig struct {
	// CommandPreroll is how much audio before the detection point may start
	// the command, for a command spoken in the same breath.
	CommandPreroll time.Duration
	// StripPhrases are removed from the start of transcripts.
	StripPhrases []string
}

// BargeInConfig sets interruptions of the agent.
type BargeInConfig struct {
	Enabled bool
	Speech  bool // sustained speech while the agent talks interrupts it
	Wake    bool // the wake word while the agent talks interrupts it
	// DuringThinking treats speech while waiting for the agent as an
	// interruption too.
	DuringThinking bool
	VADThreshold   float32       // stricter speech threshold while the agent talks
	MinSpeech      time.Duration // sustained speech that raises a suspicion
	Grace          time.Duration // ignore speech this long after the agent starts
	// Verify ducks the agent on a suspicion and confirms only if the voice
	// stays louder than the agent's predicted echo.
	Verify            bool
	DuckDB            float64
	VerifyDelay       time.Duration // raised to IOLatency + 40 ms
	VerifyWindow      time.Duration
	VerifyMinFraction float64 // of chunks in the window that must be speech
	VerifyMinExcessDB float64 // mic level above the predicted echo
	// EchoDelay is the speaker-to-mic delay used to predict the echo; zero
	// uses IOLatency, or 100 ms.
	EchoDelay      time.Duration
	RejectCooldown time.Duration // after a rejected suspicion
}

// Defaults are wonky-talkie's tuned settings.
func Defaults() Config {
	return Config{
		VAD: VADConfig{
			Threshold: 0.5, NegThreshold: 0.35,
			MinSpeech: 200 * time.Millisecond, EndSilence: 800 * time.Millisecond,
			Preroll: 300 * time.Millisecond, MinUtterance: 250 * time.Millisecond,
		},
		Session: SessionConfig{
			AfterWakeTimeout: 5 * time.Second, FollowupTimeout: 8 * time.Second,
			MaxUtterance: 20 * time.Second, AgentTimeout: 120 * time.Second,
			PostSpeechHoldoff: 150 * time.Millisecond,
		},
		Wake: WakeConfig{CommandPreroll: 100 * time.Millisecond},
		BargeIn: BargeInConfig{
			Enabled: true, Speech: true, Wake: true, DuringThinking: true,
			VADThreshold: 0.6, MinSpeech: 300 * time.Millisecond, Grace: 250 * time.Millisecond,
			Verify: true, DuckDB: -20,
			VerifyDelay: 150 * time.Millisecond, VerifyWindow: 200 * time.Millisecond,
			VerifyMinFraction: 0.5, VerifyMinExcessDB: 6,
			RejectCooldown: 700 * time.Millisecond,
		},
		Echo: EchoConfig{
			Enabled: true, Filter: 250 * time.Millisecond, Hold: time.Second,
			NoiseSuppressDB: -10, EchoSuppressDB: -40, EchoSuppressActiveDB: -15,
		},
	}
}

func (c Config) validate() error {
	v := c.VAD
	if !(v.NegThreshold > 0 && v.NegThreshold <= v.Threshold && v.Threshold <= 1) {
		return errors.New("VAD thresholds need 0 < NegThreshold <= Threshold <= 1")
	}
	if c.Session.MaxUtterance <= 0 || c.Session.FollowupTimeout <= 0 || c.Session.AfterWakeTimeout <= 0 {
		return errors.New("session timeouts and MaxUtterance must be positive")
	}
	return nil
}
