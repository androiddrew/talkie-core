package talkie

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/androiddrew/talkie-core/internal/engine"
	"github.com/androiddrew/talkie-core/internal/sim"
)

// testConfig is a Config for the real models, from TALKIE_TEST_ORT,
// TALKIE_TEST_PARAKEET (the Parakeet directory), TALKIE_TEST_KOKORO (the
// directory with kokoro-v1.0.onnx and voices-v1.0.bin) and, optionally,
// TALKIE_TEST_CUDA=1.
func testConfig(t *testing.T) Config {
	t.Helper()
	library, asr, tts := os.Getenv("TALKIE_TEST_ORT"), os.Getenv("TALKIE_TEST_PARAKEET"), os.Getenv("TALKIE_TEST_KOKORO")
	if library == "" || asr == "" || tts == "" {
		t.Skip("set TALKIE_TEST_ORT, TALKIE_TEST_PARAKEET and TALKIE_TEST_KOKORO for the real models")
	}
	return Config{
		ORTLibrary: library,
		CUDA:       os.Getenv("TALKIE_TEST_CUDA") == "1",
		Parakeet:   ParakeetConfig{Dir: asr},
		Kokoro: KokoroConfig{
			ModelPath:  filepath.Join(tts, "kokoro-v1.0.onnx"),
			VoicesPath: filepath.Join(tts, "voices-v1.0.bin"),
		},
		OutputRate: sim.Rate, // the simulated room's speaker
	}
}

// TestRealModels runs a whole conversation on the real models: the user
// says "hey mycroft" and then speaks, into a room that echoes the speaker;
// PhonMatchNet hears the wake word, Silero ends the utterance, Parakeet
// transcribes it, the agent answers, Kokoro speaks the answer into the room,
// and its echo does not interrupt it.
func TestRealModels(t *testing.T) {
	cfg := testConfig(t)
	cfg.WakeWord = "hey mycroft"
	asked := newTurns()
	agent := AgentFunc(func(ctx context.Context, turn Turn, reply *Reply) error {
		asked.add(turn)
		_, err := reply.WriteString("Okay. The office lights are on now. Anything else?")
		return err
	})
	start := time.Now()
	tk, err := New(t.Context(), cfg, agent)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("models loaded in %v", time.Since(start).Round(time.Millisecond))
	defer func() {
		if err := tk.Close(); err != nil {
			t.Error(err)
		}
	}()
	b := &bench{t: t, conv: tk.conv, player: tk.player, room: sim.NewRoom(true), noise: sim.NewNoise()}
	tk.conv.onEvent = func(ev engine.Event) { b.log = append(b.log, ev) }
	b.attach()

	b.wakeThenCommand()
	if _, ok := b.find(engine.WakeDetected); !ok {
		t.Fatal("PhonMatchNet did not hear \"hey mycroft\"")
	}
	turn := asked.next(t)
	t.Logf("Parakeet heard %q", turn.Text)
	// The clip says "So if we turn that off, does it work with".
	if !strings.Contains(strings.ToLower(turn.Text), "turn that off") || turn.Trigger != TriggerWake {
		t.Errorf("turn %+v", turn)
	}
	start = time.Now()
	b.spoken()
	t.Logf("reply synthesized in %v", time.Since(start).Round(time.Millisecond))
	b.silence(5.0)
	if ev, ok := b.find(engine.InterruptionDetected); ok {
		t.Fatalf("the reply's echo interrupted it: %+v", ev)
	}
	fin, ok := b.find(engine.PlaybackFinished)
	if !ok || fin.Playback.Heard != "Okay. The office lights are on now. Anything else?" {
		t.Fatalf("finished %+v", fin.Playback)
	}
	t.Logf("Kokoro spoke %v; the canceller removed %.1f dB of echo", fin.Playback.Played.Round(time.Millisecond), fin.ERLE)
	if b.conv.eng.State() != engine.Listening {
		t.Errorf("state %s; want a follow-up window", b.conv.eng.State())
	}
}

// TestRealModelsBargeIn interrupts a long Kokoro reply, over its echo: the
// interruption is confirmed against the predicted echo, Parakeet transcribes
// what the user said, and the agent learns what was heard.
func TestRealModelsBargeIn(t *testing.T) {
	t.Run("canceller", func(t *testing.T) { realModelsBargeIn(t, false) })
	t.Run("verify alone", func(t *testing.T) { realModelsBargeIn(t, true) })
}

func realModelsBargeIn(t *testing.T, noCanceller bool) {
	cfg := testConfig(t)
	cfg.DisableEchoCancellation = noCanceller
	cfg.WakeWord = "hey mycroft"
	asked := newTurns()
	story := []string{
		"Once upon a time, in a small house at the edge of a quiet forest, there lived a little robot.",
		"Every night it listened to the rain on the roof, and counted the drops until morning.",
		"One night the rain stopped, and the robot did not know what to do.",
	}
	agent := AgentFunc(func(ctx context.Context, turn Turn, reply *Reply) error {
		asked.add(turn)
		if turn.Interrupted != nil {
			return nil
		}
		for _, s := range story {
			if _, err := reply.WriteString(s + " "); err != nil {
				return err
			}
		}
		return nil
	})
	tk, err := New(t.Context(), cfg, agent)
	if err != nil {
		t.Fatal(err)
	}
	defer tk.Close()
	b := &bench{t: t, conv: tk.conv, player: tk.player, room: sim.NewRoom(true), noise: sim.NewNoise()}
	tk.conv.onEvent = func(ev engine.Event) { b.log = append(b.log, ev) }
	b.attach()

	b.wakeThenCommand()
	asked.next(t)
	b.spoken()
	b.silence(7.0) // the first sentence and part of the second play
	b.feed(sim.ShortReply(t))
	b.silence(1.5)
	ev, ok := b.find(engine.InterruptionDetected)
	if !ok {
		t.Fatal("the user's interruption was not detected")
	}
	t.Logf("interrupted after %v, %.1f dB over the echo", ev.Playback.Played.Round(time.Millisecond), ev.ExcessDB)
	next := asked.next(t)
	in := next.Interrupted
	t.Logf("Parakeet heard %q; heard %q, cut %q", next.Text, in.Heard, in.Cut)
	if next.Trigger != TriggerBargeIn || in == nil || in.Heard != story[0] || in.Cut != story[1] || in.Unspoken != story[2] {
		t.Errorf("turn %+v, interruption %+v", next, in)
	}
	if next.Text == "" {
		t.Error("the interruption was not transcribed")
	}
}
