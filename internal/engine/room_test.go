package engine

import (
	"fmt"
	"testing"
	"time"

	"github.com/androiddrew/talkie-core/internal/audio"
	"github.com/androiddrew/talkie-core/internal/sim"
)

// roomOutcome plays the agent's 8 s reply alone in a room, then has the
// user talk over a reply in the same room, with talkie assuming latency.
func roomOutcome(t *testing.T, delay int, gainDB float64, latency time.Duration) (selfInterrupted bool, suspicions int, userCaught bool, excess float64) {
	edit := func(c *Config) { c.IOLatency = latency }
	h := newHarness(t, options{wakeAt: []float64{wakeT}, echo: true, stt: replies("Tell me a story.", "x"), edit: edit})
	h.room = sim.NewRoomDelay(true, delay, gainDB)
	startConversation(h)
	h.play("s1", agentVoice(t, 8.0), "Once upon a time...", audio.ThenListen, 0)
	h.feed(h.silence(9.0))
	selfInterrupted, suspicions = h.has(InterruptionDetected), h.count(InterruptionSuspected)

	h = newHarness(t, options{wakeAt: []float64{wakeT}, echo: true, stt: replies("Tell me a story.", "Stop."), edit: edit})
	h.room = sim.NewRoomDelay(true, delay, gainDB)
	startConversation(h)
	longAnswer(h, "s1")
	h.feed(h.silence(3.0))
	h.feed(command(t))
	h.feed(h.silence(1.5))
	if userCaught = h.has(InterruptionDetected); userCaught {
		excess = h.find(InterruptionDetected, nil).ExcessDB
	}
	return
}

// TestRoomsWithKnownLatency: with the room's latency known, as a trained
// room profile gives it, the agent never interrupts itself, even with an
// echo louder than the voice that made it. 2026-10-13's desktop was +8 dB
// and 115-175 ms, and at talkie's 100 ms default the story interrupted
// itself.
//
// Catching the user over the reply needs a quieter echo. At +8 dB the
// speaker drives the microphone into clipping: the echo canceller removes
// about 1 dB instead of 10, and in a long-delay room the echo masks the
// user (220 ms: caught at -6 dB, missed from -2 dB up). The -6 dB room's
// echo, reverb included, measures -3.3 dB in room training, which advises
// turning down above -3 dB; so catching the user is required in the -6 dB
// rooms, and louder rooms are logged.
func TestRoomsWithKnownLatency(t *testing.T) {
	for _, gain := range []float64{-6, 8} {
		for _, delay := range []int{80, 115, 175, 220} {
			t.Run(fmt.Sprintf("%+.0fdB/%dms", gain, delay), func(t *testing.T) {
				self, sus, caught, excess := roomOutcome(t, delay, gain, time.Duration(delay)*time.Millisecond)
				t.Logf("self-interrupted %v, suspicions %d, user caught %v at %.1f dB", self, sus, caught, excess)
				if self {
					t.Error("the agent interrupted itself")
				}
				if gain <= -6 && !caught {
					t.Error("the user talking over the agent was not caught")
				}
			})
		}
	}
}

// TestProfileLatencyCoversSpread: the desktop's latency varied between 115
// and 175 ms across device opens. A profile assumes the middle of the
// measured range, which must keep the agent from interrupting itself at
// both ends.
func TestProfileLatencyCoversSpread(t *testing.T) {
	assumed := (115 + 175) * time.Millisecond / 2
	for _, delay := range []int{115, 175} {
		self, sus, caught, excess := roomOutcome(t, delay, 8, assumed)
		t.Logf("real %d ms, assumed %v: self-interrupted %v, suspicions %d, user caught %v at %.1f dB", delay, assumed, self, sus, caught, excess)
		if self {
			t.Errorf("real %d ms: the agent interrupted itself", delay)
		}
	}
}
