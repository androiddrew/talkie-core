package main

import (
	"context"
	"strings"
	"testing"
	"time"

	talkie "github.com/androiddrew/talkie-core"
)

func ask(t *testing.T, p *parrot, turn talkie.Turn) (string, talkie.Then) {
	t.Helper()
	var out strings.Builder
	then := talkie.ThenListen
	if err := p.answer(context.Background(), turn, &out, func(th talkie.Then) { then = th }); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out.String()), then
}

func TestParrot(t *testing.T) {
	p := &parrot{}
	if got, _ := ask(t, p, talkie.Turn{Text: "Turn on the lights."}); got != "You said: Turn on the lights. What else?" {
		t.Errorf("echo: %q", got)
	}
	if got, then := ask(t, p, talkie.Turn{Text: "Goodbye."}); got != "Goodbye!" || then != talkie.ThenEnd {
		t.Errorf("goodbye: %q, %s", got, then)
	}
	if got, _ := ask(t, p, talkie.Turn{Text: "Tell me a story."}); got != strings.Join(story, " ") {
		t.Errorf("story: %q", got)
	}
}

// TestParrotStoryInterrupted: cut off in the third sentence, it stops, and
// "continue" resumes from that sentence.
func TestParrotStoryInterrupted(t *testing.T) {
	p := &parrot{}
	in := &talkie.Interruption{
		Heard:    story[0] + " " + story[1],
		Cut:      story[2],
		Unspoken: strings.Join(story[3:], " "),
		Played:   9 * time.Second,
	}
	got, _ := ask(t, p, talkie.Turn{Text: "Wait a moment.", Interrupted: in})
	if got != "Okay, I'll stop. You said: Wait a moment. Say continue to hear the rest." {
		t.Errorf("after the interruption: %q", got)
	}
	if got, _ := ask(t, p, talkie.Turn{Text: "Continue."}); got != strings.Join(story[2:], " ") {
		t.Errorf("continued: %q", got)
	}
	if got, _ := ask(t, p, talkie.Turn{Text: "Continue."}); !strings.HasPrefix(got, "You said: Continue") {
		t.Errorf("nothing left to continue: %q", got)
	}
}

// TestParrotStops: a cancelled turn stops writing.
func TestParrotStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out strings.Builder
	err := (&parrot{}).answer(ctx, talkie.Turn{Text: "Tell me a story."}, &out, func(talkie.Then) {})
	if err == nil || strings.Count(out.String(), ".") > 1 {
		t.Errorf("err %v after %q", err, out.String())
	}
}
