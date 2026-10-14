package talkie

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/androiddrew/talkie-core/internal/audio"
	"github.com/androiddrew/talkie-core/internal/engine"
	"github.com/androiddrew/talkie-core/internal/sim"
)

// The simulated device adds one 20 ms period to its room's delay, so a
// conversation's room must be 20 ms longer to match what training heard.
const trainRoomDelay, trainRoomGain = 80, -6.0

func trainedProfile(t *testing.T) Profile {
	t.Helper()
	open, _ := simOpener(true, trainRoomDelay, trainRoomGain)
	speak := func(rate int) ([]float32, error) {
		if rate != sim.Rate {
			t.Fatalf("speech at %d Hz", rate)
		}
		return append([]float32(nil), sim.AgentVoice(t, 10)...), nil
	}
	var steps []string
	p, err := train(t.Context(), Config{OutputRate: sim.Rate}, func(_ context.Context, s Step) error {
		steps = append(steps, s.Name)
		return nil
	}, open, speak, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(steps, ",") != "noise,echo,canceller" {
		t.Fatalf("steps %v", steps)
	}
	return p
}

func TestTrainLearnsCanceller(t *testing.T) {
	p := trainedProfile(t)
	if p.Canceller == nil || len(p.Canceller.Snapshot) == 0 {
		t.Fatal("no canceller in the profile")
	}
	t.Logf("trained canceller: %.1f dB of echo removed, %d-byte snapshot", p.Canceller.ERLEDB, len(p.Canceller.Snapshot))
	if p.Canceller.ERLEDB < 10 {
		t.Errorf("the trained canceller removed %.1f dB", p.Canceller.ERLEDB)
	}
	path := ProfilePath(t.TempDir(), p.Input, p.Output)
	if err := SaveProfile(path, p); err != nil {
		t.Fatal(err)
	}
	got, err := LoadProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Canceller == nil || !bytes.Equal(got.Canceller.Snapshot, p.Canceller.Snapshot) || got.Canceller.ERLEDB != p.Canceller.ERLEDB {
		t.Error("the canceller did not survive saving and loading")
	}
}

// firstReply plays the agent's first 6 s reply alone in the training room
// and returns the echo removed and the duck-and-verify suspicions.
func firstReply(t *testing.T, snapshot []byte) (erle float64, suspicions int) {
	b := newBench(t, AgentFunc(func(context.Context, Turn, *Reply) error { return nil }),
		benchOptions{room: sim.NewRoomDelay(true, trainRoomDelay+20, trainRoomGain)})
	if snapshot != nil {
		if err := b.conv.eng.RestoreEcho(snapshot); err != nil {
			t.Fatal(err)
		}
	}
	b.silence(0.5)
	b.player.Start("r1")
	b.player.Add("r1", sim.Clip(t, "office_lights.wav", 12, 18), "A reply.")
	b.player.End("r1", audio.ThenEnd, 0)
	b.silence(7)
	fin, ok := b.find(engine.PlaybackFinished)
	if !ok {
		t.Fatal("the reply did not finish")
	}
	for _, e := range b.log {
		if e.Type == engine.InterruptionSuspected {
			suspicions++
		}
	}
	return fin.ERLE, suspicions
}

// TestRestoredCancellerFirstReply: the first reply of a session that
// restored the trained canceller has the echo removed from the start, where
// a cold canceller is still learning.
func TestRestoredCancellerFirstReply(t *testing.T) {
	p := trainedProfile(t)
	coldERLE, coldSuspicions := firstReply(t, nil)
	warmERLE, warmSuspicions := firstReply(t, p.Canceller.Snapshot)
	t.Logf("first reply: cold %.1f dB removed, %d suspicions; restored %.1f dB, %d suspicions", coldERLE, coldSuspicions, warmERLE, warmSuspicions)
	if warmERLE < coldERLE+5 || warmSuspicions > coldSuspicions {
		t.Errorf("restored %.1f dB / %d suspicions, cold %.1f dB / %d", warmERLE, warmSuspicions, coldERLE, coldSuspicions)
	}
}

// TestRunRestoresCanceller: Run's profile restores the canceller, unless
// echo cancellation is off.
func TestRunRestoresCanceller(t *testing.T) {
	p := trainedProfile(t)
	dir := t.TempDir()
	if err := SaveProfile(ProfilePath(dir, p.Input, p.Output), p); err != nil {
		t.Fatal(err)
	}
	for _, off := range []bool{false, true} {
		var logs bytes.Buffer
		b := newBench(t, AgentFunc(func(context.Context, Turn, *Reply) error { return nil }), benchOptions{})
		tk := &Talkie{cfg: Config{RoomProfileDir: dir, DisableEchoCancellation: off, Logger: slog.New(slog.NewTextHandler(&logs, nil))}, conv: b.conv}
		tk.useProfile(p.Input, p.Output)
		want := "echo_canceller_restored=" + map[bool]string{false: "true", true: "false"}[off]
		if !strings.Contains(logs.String(), want) {
			t.Errorf("echo cancellation off %v: log %q", off, logs.String())
		}
	}
}
