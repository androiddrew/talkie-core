package talkie

import (
	"reflect"
	"strings"
	"testing"
)

func TestSentences(t *testing.T) {
	for _, c := range []struct {
		text     string
		final    bool
		complete []string
		rest     string
	}{
		{"Hello there. How are", false, []string{"Hello there."}, " How are"},
		{"Hello there.", false, nil, "Hello there."}, // the next delta may be "5"
		{"Hello there.", true, []string{"Hello there."}, ""},
		{"Wait! Really? Yes. ", false, []string{"Wait!", "Really?", "Yes."}, " "},
		{`He said "stop." Then left`, false, []string{`He said "stop."`}, " Then left"},
		{"It costs 3.5 dollars. Ok", false, []string{"It costs 3.5 dollars."}, " Ok"},
		{"Ask Dr. Smith at 3 p.m. today. Done", false, []string{"Ask Dr. Smith at 3 p.m. today."}, " Done"},
		{"Use e.g. this. Fine", false, []string{"Use e.g. this."}, " Fine"},
		{"Steps\n- one\n- two", false, []string{"Steps", "- one"}, "- two"},
		{"   ", true, nil, ""},
	} {
		complete, rest := sentences(c.text, c.final)
		if !reflect.DeepEqual(complete, c.complete) || rest != c.rest {
			t.Errorf("sentences(%q, %v) = %q, %q; want %q, %q", c.text, c.final, complete, rest, c.complete, c.rest)
		}
	}
}

// TestSentencesStreaming feeds a reply in arbitrary deltas: the sentences
// must not depend on where the deltas split.
func TestSentencesStreaming(t *testing.T) {
	reply := "Once upon a time, there was a robot. It cost $3.50 in 1999! Did it listen? Yes.\nThe end"
	want, _ := sentences(reply, true)
	for size := 1; size <= len(reply); size++ {
		var got []string
		buf := ""
		for i := 0; i < len(reply); i += size {
			buf += reply[i:min(i+size, len(reply))]
			var complete []string
			complete, buf = sentences(buf, false)
			got = append(got, complete...)
		}
		complete, _ := sentences(buf, true)
		got = append(got, complete...)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("delta size %d: %q, want %q", size, got, want)
		}
	}
	if strings.Join(want, "|") != "Once upon a time, there was a robot.|It cost $3.50 in 1999!|Did it listen?|Yes.|The end" {
		t.Errorf("sentences %q", want)
	}
}
