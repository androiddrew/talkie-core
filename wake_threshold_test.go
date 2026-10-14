package talkie

import (
	"math"
	"slices"
	"strings"
	"testing"
)

// def is PhonMatchNet's threshold at its default 1% false-accept target.
const def = 0.7149204015731812

func TestChooseWakeThreshold(t *testing.T) {
	for _, c := range []struct {
		name        string
		positives   []float64
		negativeMax float64
		threshold   float64 // 0: none; otherwise checked to 1e-3
		advice      string  // a substring, or empty for none
	}{
		// The usual case: the wake word clears the default, ordinary speech
		// scores near zero. Halfway would be about 0.06; the default stays.
		{"default kept", []float64{0.993, 0.995, 0.989, 0.992, 0.993}, 1e-5, def, ""},
		// A voice PhonMatchNet hears weakly: lowered, one log-odds below the
		// weakest.
		{"lowered for the user", []float64{0.4, 0.6, 0.5}, 0.001, sigmoid(logit(0.4) - wakeHeadroom), ""},
		// Lowered no further than halfway to the ordinary speech.
		{"lowered to halfway", []float64{0.4, 0.6, 0.5}, 0.2, sigmoid((logit(0.4) + logit(0.2)) / 2), "a little above"},
		// Ordinary speech that scores high raises it above the default.
		{"raised", []float64{0.98, 0.99, 0.995}, 0.8, sigmoid((logit(0.98) + logit(0.8)) / 2), ""},
		{"bounded below", []float64{0.015, 0.02, 0.03}, 1e-9, 0.01, ""},
		{"bounded above", []float64{0.99999, 0.999995, 0.999999}, 0.9, 0.95, ""},
		// The bound would fall below the ordinary speech: the data wins.
		{"bound gives way", []float64{0.9999, 0.99999, 0.99995}, 0.98, sigmoid((logit(0.9999) + logit(0.98)) / 2), ""},
		{"overlapping", []float64{0.5, 0.9, 0.95}, 0.7, 0, "default threshold"},
		{"too few found", []float64{0.9, 0.95}, 0.1, 0, "only 2 times"},
	} {
		w := chooseWakeThreshold("hey computer", c.positives, c.negativeMax, def)
		if math.Abs(w.Threshold-c.threshold) > 1e-3 {
			t.Errorf("%s: threshold %.4f, want %.4f", c.name, w.Threshold, c.threshold)
		}
		if w.Threshold > 0 && (w.Threshold <= c.negativeMax || w.Threshold >= slices.Min(c.positives)) {
			t.Errorf("%s: threshold %.4f is not between the speech (%.4f) and the wake word (%.4f)",
				c.name, w.Threshold, c.negativeMax, slices.Min(c.positives))
		}
		if (c.advice == "") != (w.Advice == "") || !strings.Contains(w.Advice, c.advice) {
			t.Errorf("%s: advice %q", c.name, w.Advice)
		}
		if w.Keyword != "hey computer" || w.NegativeMax != c.negativeMax || len(w.Positives) != len(c.positives) {
			t.Errorf("%s: %+v", c.name, w)
		}
	}
}
