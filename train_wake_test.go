package talkie

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/androiddrew/talkie-core/internal/sim"
)

func ortLibrary(t *testing.T) string {
	t.Helper()
	library := os.Getenv("TALKIE_TEST_ORT")
	if library == "" {
		t.Skip("set TALKIE_TEST_ORT for wake word training")
	}
	return library
}

// TestTrainWake: a user says "hey computer" five times, then reads ordinary
// speech, into a simulated room. Training finds the five and puts the
// threshold between them and the speech.
func TestTrainWake(t *testing.T) {
	cfg := Config{OutputRate: sim.Rate, ORTLibrary: ortLibrary(t), WakeWord: "hey computer"}
	scorer, err := newWakeScorer(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer scorer.close()
	pause := make([]float32, sim.Rate)
	says := func(n int) []float32 {
		switch n {
		case 1 + trainChirps + 1: // the wake word step: after the noise and the chirps
			var said []float32
			for i := range wakeRepeats {
				said = append(append(said, pause...), sim.SaysWakeWord(t, sim.Voices[i%len(sim.Voices)])...)
			}
			return said
		case 1 + trainChirps + 2: // reading aloud
			return slices.Clone(sim.Clip(t, "office_lights.wav", 2, 13))
		}
		return nil
	}
	open, _ := simOpenerSaying(true, 80, -6, says)
	var steps []string
	p, err := train(t.Context(), cfg, func(_ context.Context, s Step) error {
		steps = append(steps, s.Name)
		return nil
	}, open, nil, scorer)
	if err != nil {
		t.Fatal(err)
	}
	w := p.Wake
	if w == nil {
		t.Fatal("no wake word in the profile")
	}
	t.Logf("steps %v; positives %.3f, speech max %.3f, threshold %.3f, advice %q", steps, w.Positives, w.NegativeMax, w.Threshold, w.Advice)
	if len(w.Positives) != wakeRepeats || w.Threshold <= w.NegativeMax || w.Threshold >= slices.Min(w.Positives) {
		t.Errorf("wake profile %+v", *w)
	}
	// "hey commuter" can score like "hey computer"; a few seconds of the
	// user's speech can't teach the difference. Recorded, not required.
	for _, v := range sim.Voices {
		score, err := scorer.peak(t.Context(), sim.SaysConfusable(t, v))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s %q: %.3f (wakes: %v)", v, "hey commuter", score, score >= w.Threshold)
	}
}

// TestWakeThresholdInListener: the live listener wakes by the trained
// threshold instead of PhonMatchNet's operating point.
func TestWakeThresholdInListener(t *testing.T) {
	library := ortLibrary(t)
	tk := &Talkie{}
	said := append(make([]float32, sim.Rate), sim.SaysWakeWord(t, "af_heart")...)
	said = append(said, make([]float32, sim.Rate)...)
	wakes := func(threshold float64) bool {
		tk.wakeThreshold = threshold
		listener, err := tk.loadWake(t.Context(), Config{ORTLibrary: library, WakeWord: "hey computer"})
		if err != nil {
			t.Fatal(err)
		}
		defer tk.wake.Close()
		at, err := listener.Write(t.Context(), said)
		if err != nil {
			t.Fatal(err)
		}
		return len(at) > 0
	}
	if !wakes(0) || !wakes(0.01) || wakes(0.999999) {
		t.Errorf("default %v, 0.01 %v, 0.999999 %v", wakes(0), wakes(0.01), wakes(0.999999))
	}
}

// TestRunUsesWakeThreshold: Run takes the profile's threshold only for the
// same wake word.
func TestRunUsesWakeThreshold(t *testing.T) {
	dir := t.TempDir()
	p := Profile{Input: "Sim Mic", Output: "Sim Speaker", Wake: &WakeProfile{Keyword: "Hey, Computer!", Threshold: 0.3}}
	if err := SaveProfile(ProfilePath(dir, p.Input, p.Output), p); err != nil {
		t.Fatal(err)
	}
	for word, want := range map[string]float64{"hey computer": 0.3, "okay robot": 0, "": 0} {
		b := newBench(t, AgentFunc(func(context.Context, Turn, *Reply) error { return nil }), benchOptions{})
		tk := &Talkie{cfg: Config{RoomProfileDir: dir, WakeWord: word}, conv: b.conv}
		tk.useProfile(p.Input, p.Output)
		if tk.wakeThreshold != want {
			t.Errorf("wake word %q: threshold %v, want %v", word, tk.wakeThreshold, want)
		}
	}
	if !sameWakeWord("Hey, Computer!", "hey computer") || sameWakeWord("hey computer", "hey commuter") {
		t.Error("sameWakeWord")
	}
}
