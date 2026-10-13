package engine

import (
	"math"
	"testing"
)

// The cases from wonky-talkie's tests/test_units.py.
func TestStripWakePhrase(t *testing.T) {
	phrases := []string{"hey jarvis", "jarvis"}
	for text, want := range map[string]string{
		"Hey Jarvis, turn off the lights.": "Turn off the lights.",
		"Hey, Jervis. What time is it?":    "What time is it?",
		"Jarvis turn it up":                "Turn it up",
		"jarvis, jarvis, are you there?":   "Are you there?",
		"Hey Jarvis.":                      "",
		"Turn off the lights, Jarvis.":     "Turn off the lights, Jarvis.",
		"Hey there, how are you?":          "Hey there, how are you?",
		"Harvest the data":                 "Harvest the data",
		"  untouched  ":                    "untouched",
	} {
		if got := StripWakePhrase(text, phrases); got != want {
			t.Errorf("StripWakePhrase(%q) = %q, want %q", text, got, want)
		}
	}
	if got := StripWakePhrase("Hey Jarvis, hi", nil); got != "Hey Jarvis, hi" {
		t.Errorf("no phrases: %q", got)
	}
}

// Values from Python's difflib.SequenceMatcher(None, a, b).ratio().
func TestRatio(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want float64
	}{
		{"jervis", "jarvis", 0.8333333333333334},
		{"harvest", "hey", 0.4},
		{"abcd", "bcda", 0.75},
		{"tide", "diet", 0.25},
		{"", "", 1},
		{"a", "", 0},
	} {
		if got := ratio(c.a, c.b); math.Abs(got-c.want) > 1e-12 {
			t.Errorf("ratio(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
