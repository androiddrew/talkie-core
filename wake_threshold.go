package talkie

import (
	"fmt"
	"math"
	"slices"
)

// WakeProfile is the wake word tuned to the user's voice in the room.
type WakeProfile struct {
	// Keyword is the wake word it was trained for; Run uses Threshold only
	// for the same wake word.
	Keyword string
	// Threshold is the PhonMatchNet score at or above which the wake word
	// is heard, between the user saying it and the user's ordinary speech;
	// zero when training couldn't separate them, and PhonMatchNet's default
	// operating point is kept.
	Threshold float64
	// Positives are the scores of the times the user said the wake word;
	// NegativeMax is the highest score anywhere in their ordinary speech.
	Positives   []float64
	NegativeMax float64
	// Advice says what to change, or is empty.
	Advice string
}

// Wake threshold settings.
const (
	// minWakeHeard is how many of the wake word repetitions must be found
	// for the training to count.
	minWakeHeard = 3
	// minWakeThreshold and maxWakeThreshold bound the threshold where the
	// recordings allow. A few seconds of speech can't show how often a much
	// laxer threshold would wake on other speech, or a much stricter one
	// miss a tired voice.
	minWakeThreshold = 0.01
	maxWakeThreshold = 0.95
	// minWakeMargin is the gap, in log-odds, between the weakest wake word
	// and the strongest ordinary speech below which training warns: e^2,
	// about 7 to 1.
	minWakeMargin = 2.0
)

// wakeHeadroom is how far below the user's weakest wake word, in log-odds,
// a lowered threshold stays.
const wakeHeadroom = 1.0

// chooseWakeThreshold picks the threshold from the user's wake words and
// their ordinary speech, starting from PhonMatchNet's calibrated
// defaultThreshold, or returns zero when they can't be separated.
//
// Ordinary speech scores far below the wake word, so the point halfway
// between them, in log-odds, is usually much laxer than the default; and
// it says nothing about near misses such as "hey commuter", which the user
// didn't say. So the threshold stays at the default unless the user needs
// otherwise: it is lowered only as far as their weakest wake word requires,
// keeping wakeHeadroom below it, and raised to the halfway point when their
// ordinary speech scores high.
func chooseWakeThreshold(keyword string, positives []float64, negativeMax, defaultThreshold float64) WakeProfile {
	w := WakeProfile{Keyword: keyword, Positives: positives, NegativeMax: negativeMax}
	if len(positives) < minWakeHeard {
		w.Advice = fmt.Sprintf("talkie found %q only %d times: train again, saying it clearly with a pause between each time.", keyword, len(positives))
		return w
	}
	weakest := slices.Min(positives)
	gap := logit(weakest) - logit(negativeMax)
	if gap <= 0 {
		w.Advice = fmt.Sprintf("Your ordinary speech scored as high as %q (%.2f against %.2f), so talkie keeps PhonMatchNet's default threshold: a longer or more distinctive wake word would be more reliable.",
			keyword, negativeMax, weakest)
		return w
	}
	halfway := sigmoid((logit(weakest) + logit(negativeMax)) / 2)
	w.Threshold = max(halfway, min(defaultThreshold, sigmoid(logit(weakest)-wakeHeadroom)))
	// The bounds give way to what the user's own recordings show: the
	// threshold always stays between their speech and their wake word.
	if w.Threshold < minWakeThreshold && minWakeThreshold < weakest {
		w.Threshold = minWakeThreshold
	}
	if w.Threshold > maxWakeThreshold && maxWakeThreshold > negativeMax {
		w.Threshold = maxWakeThreshold
	}
	if gap < minWakeMargin {
		w.Advice = fmt.Sprintf("%q scored only a little above your ordinary speech (%.2f against %.2f): expect some missed or false wakes; a longer or more distinctive wake word would be more reliable.",
			keyword, weakest, negativeMax)
	}
	return w
}

func logit(p float64) float64 {
	p = max(1e-6, min(1-1e-6, p))
	return math.Log(p / (1 - p))
}

func sigmoid(x float64) float64 { return 1 / (1 + math.Exp(-x)) }
