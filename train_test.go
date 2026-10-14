package talkie

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/androiddrew/talkie-core/internal/device"
	"github.com/androiddrew/talkie-core/internal/engine"
	"github.com/androiddrew/talkie-core/internal/sim"
)

// simDuplex is a full-duplex device in a simulated room. Its goroutine
// calls the callback once a period, as fast as it can. The microphone
// hears the room's echo of the speaker one period later, as a real
// device's buffering delays it.
type simDuplex struct {
	cb     device.Callback
	room   *sim.Room
	noise  *sim.Noise
	period int
	stop   chan struct{}
	done   chan struct{}
}

// simOpener opens simulated devices in rooms with the given delay and echo
// gain, at sim.Rate.
func simOpener(echo bool, delayMS int, gainDB float64) (opener, *int) {
	opens := new(int)
	var mu sync.Mutex
	return func(c device.Config, cb device.Callback) (duplex, error) {
		mu.Lock()
		*opens++
		mu.Unlock()
		if c.Rate != sim.Rate {
			return nil, errors.New("simulated devices run at sim.Rate")
		}
		return &simDuplex{cb: cb, room: sim.NewRoomDelay(echo, delayMS, gainDB), noise: sim.NewNoise(),
			period: sim.Rate / 50, stop: make(chan struct{}), done: make(chan struct{})}, nil
	}, opens
}

func (d *simDuplex) Start() error {
	go func() {
		defer close(d.done)
		previous := make([]float32, d.period)
		for {
			select {
			case <-d.stop:
				return
			default:
			}
			in := d.room.Mix(d.noise.Silence(float64(d.period)/sim.Rate), previous)
			out := make([]float32, d.period)
			d.cb(in, out)
			previous = out
		}
	}()
	return nil
}

func (d *simDuplex) Close() error {
	select {
	case <-d.stop:
	default:
		close(d.stop)
		<-d.done
	}
	return nil
}

func (d *simDuplex) Names() (string, string) { return "Sim Mic", "Sim Speaker" }

func ready(context.Context, Step) error { return nil }

// TestTrain measures simulated rooms. The room's nominal gain is its direct
// path; training measures the whole echo, reverb included, which in the
// simulated room is about 2.5 dB more.
func TestTrain(t *testing.T) {
	measured := map[float64]float64{}
	for _, room := range []struct {
		delay   int
		gainDB  float64
		advised bool
	}{{80, -6, false}, {115, 8, true}, {175, -12, false}} {
		open, opens := simOpener(true, room.delay, room.gainDB)
		var steps []string
		p, err := train(t.Context(), Config{OutputRate: sim.Rate}, func(_ context.Context, s Step) error {
			steps = append(steps, s.Name)
			return nil
		}, open, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("room %d ms %+.0f dB: latency %v, echo %+.1f dB, clipped %v, noise %.1f dBFS", room.delay, room.gainDB, p.Latency.Median, p.Echo.GainDB, p.Echo.Clipped, p.NoiseDBFS)
		// The device adds one period (20 ms) to the room's delay.
		want := time.Duration(room.delay+20) * time.Millisecond
		if d := p.Latency.Median - want; d < -time.Millisecond || d > time.Millisecond {
			t.Errorf("room %d ms: latency %v, want %v", room.delay, p.Latency.Median, want)
		}
		if len(p.Latency.Runs) != trainChirps || p.Latency.Min > p.Latency.Median || p.Latency.Median > p.Latency.Max {
			t.Errorf("latency runs %v, min %v median %v max %v", p.Latency.Runs, p.Latency.Min, p.Latency.Median, p.Latency.Max)
		}
		if !p.Echo.Heard || p.Echo.GainDB < room.gainDB || p.Echo.GainDB > room.gainDB+4 {
			t.Errorf("room %+.0f dB: echo %+v", room.gainDB, p.Echo)
		}
		measured[room.gainDB] = p.Echo.GainDB
		if advised := p.Echo.Advice != ""; advised != room.advised {
			t.Errorf("room %+.0f dB: advice %q", room.gainDB, p.Echo.Advice)
		}
		if p.NoiseDBFS > -55 || p.NoiseDBFS < -75 {
			t.Errorf("noise floor %.1f dBFS; the room is at about -62", p.NoiseDBFS)
		}
		if p.Input != "Sim Mic" || p.Output != "Sim Speaker" || p.Rate != sim.Rate {
			t.Errorf("devices %q %q %d", p.Input, p.Output, p.Rate)
		}
		if strings.Join(steps, ",") != "noise,echo" || *opens != 1+trainChirps {
			t.Errorf("steps %v, %d device opens", steps, *opens)
		}
	}
	// Rooms 6 dB apart measure 6 dB apart.
	if d := measured[-6] - measured[-12]; math.Abs(d-6) > 1 {
		t.Errorf("rooms 6 dB apart measured %.1f dB apart", d)
	}
}

// TestTrainHeadset: the microphone never hears the speaker.
func TestTrainHeadset(t *testing.T) {
	open, _ := simOpener(false, 80, -6)
	p, err := train(t.Context(), Config{OutputRate: sim.Rate}, ready, open, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Echo.Heard || len(p.Latency.Runs) != 0 || !strings.Contains(p.Echo.Advice, "headset") {
		t.Errorf("headset profile %+v", p)
	}
}

func TestTrainStops(t *testing.T) {
	open, opens := simOpener(true, 80, -6)
	stop := errors.New("not now")
	if _, err := train(t.Context(), Config{OutputRate: sim.Rate}, func(context.Context, Step) error { return stop }, open, nil); !errors.Is(err, stop) || *opens != 0 {
		t.Errorf("err %v after %d opens", err, *opens)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := train(ctx, Config{OutputRate: sim.Rate}, ready, open, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	if _, err := Train(t.Context(), Config{}, nil); err == nil {
		t.Error("trained without a prompt")
	}
}

func TestProfileStorage(t *testing.T) {
	dir := t.TempDir()
	p := Profile{
		Trained: time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC),
		Input:   "C922 Pro Stream Webcam Analog Stereo", Output: "Starship/Matisse HD Audio Controller Analog Stereo", Rate: 48000,
		Latency: LatencyProfile{
			Runs: []time.Duration{174 * time.Millisecond, 115 * time.Millisecond, 115 * time.Millisecond},
			Min:  115 * time.Millisecond, Median: 115 * time.Millisecond, Max: 174 * time.Millisecond,
		},
		Echo:      EchoProfile{Heard: true, GainDB: 8.1, Advice: "turn it down"},
		NoiseDBFS: -46.5,
	}
	path := ProfilePath(dir, p.Input, p.Output)
	if base := filepath.Base(path); !strings.HasPrefix(base, "c922-pro-stream-webcam-analog-stereo__starship-matisse-hd-audio-controller") || !strings.HasSuffix(base, ".json") {
		t.Errorf("profile file %q", base)
	}
	if ProfilePath(dir, "a", "b") == ProfilePath(dir, "a b", "") {
		t.Error("different devices share a profile")
	}
	if err := SaveProfile(path, p); err != nil {
		t.Fatal(err)
	}
	got, err := LoadProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Latency.Assumed() != 144500*time.Microsecond || got.Input != p.Input || got.Echo != p.Echo ||
		got.NoiseDBFS != p.NoiseDBFS || !got.Trained.Equal(p.Trained) || len(got.Latency.Runs) != 3 {
		t.Errorf("loaded %+v", got)
	}
	if _, err := LoadProfile(filepath.Join(dir, "missing.json")); !errors.Is(err, ErrNoProfile) {
		t.Errorf("missing profile: %v", err)
	}
}

// TestRunUsesProfile: the profile for the devices sets the latency, unless
// Config.AudioLatency does, or the profile is ignored.
func TestRunUsesProfile(t *testing.T) {
	dir := t.TempDir()
	p := Profile{Input: "Sim Mic", Output: "Sim Speaker", Latency: LatencyProfile{
		Runs: []time.Duration{120 * time.Millisecond, 180 * time.Millisecond},
		Min:  120 * time.Millisecond, Median: 120 * time.Millisecond, Max: 180 * time.Millisecond,
	}}
	if err := SaveProfile(ProfilePath(dir, p.Input, p.Output), p); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		cfg  Config
		want time.Duration
	}{
		{"profile", Config{RoomProfileDir: dir}, 150 * time.Millisecond},
		{"explicit latency wins", Config{RoomProfileDir: dir, AudioLatency: 90 * time.Millisecond}, 90 * time.Millisecond},
		{"ignored", Config{RoomProfileDir: dir, IgnoreRoomProfile: true}, 0},
		{"no profile", Config{RoomProfileDir: t.TempDir()}, 0},
	} {
		b := newBench(t, AgentFunc(func(context.Context, Turn, *Reply) error { return nil }), benchOptions{})
		b.conv.eng.SetIOLatency(c.cfg.AudioLatency) // as New does
		tk := &Talkie{cfg: c.cfg, conv: b.conv}
		tk.useProfile("Sim Mic", "Sim Speaker")
		if got := b.conv.eng.IOLatency(); got != c.want {
			t.Errorf("%s: latency %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDrift(t *testing.T) {
	d := newDrift(LatencyProfile{Runs: []time.Duration{1}, Min: 115 * time.Millisecond, Max: 117 * time.Millisecond})
	ms := func(v int) time.Duration { return time.Duration(v) * time.Millisecond }
	var warnings []int
	for i, v := range []int{116, 174, 120, 175, 176, 174, 178, 180, 150} {
		if d.observe(ms(v)) {
			warnings = append(warnings, i)
		}
	}
	// 174 alone is an odd device open; 175, 176, 174 in a row is a change,
	// warned about once.
	if len(warnings) != 1 || warnings[0] != 5 {
		t.Errorf("warned at %v, want once at 5", warnings)
	}
	if newDrift(LatencyProfile{}) != nil {
		t.Error("drift checking without a trained latency")
	}
}

// TestDriftWarning: Talkie logs the drift warning from measured events.
func TestDriftWarning(t *testing.T) {
	var logs bytes.Buffer
	tk := &Talkie{cfg: Config{Logger: slog.New(slog.NewTextHandler(&logs, nil))},
		drift: newDrift(LatencyProfile{Runs: []time.Duration{1}, Min: 115 * time.Millisecond, Max: 117 * time.Millisecond})}
	for range driftReplies {
		tk.event(engine.Event{Type: engine.EchoMeasured, EchoDelay: 200 * time.Millisecond})
	}
	if !strings.Contains(logs.String(), "train again") {
		t.Errorf("log %q", logs.String())
	}
}
