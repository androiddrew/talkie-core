package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	silerovad "github.com/androiddrew/go-silero-vad"

	"github.com/androiddrew/talkie-core/internal/audio"
)

// The harness drives an Engine with audio built from real speech clips, the
// real Silero VAD, a scripted wake word and a fake transcriber, in lock-step
// like an audio device: each 32 ms block, the player is read (what the
// speaker plays), the room mixes the user's voice with the agent's echo (what
// the mic hears), and the engine processes both. Ported from wonky-talkie's
// tests/harness.py.

const rate = audio.SampleRate

var (
	vadOnce   sync.Once
	vadEngine *silerovad.Engine
	vadErr    error
)

func vadStream(t *testing.T) *silerovad.Stream {
	t.Helper()
	library := os.Getenv("TALKIE_TEST_ORT")
	if library == "" {
		t.Skip("set TALKIE_TEST_ORT to an ONNX Runtime library for engine scenarios")
	}
	vadOnce.Do(func() { vadEngine, vadErr = silerovad.New(silerovad.Config{ORTLibrary: library}) })
	if vadErr != nil {
		t.Fatal(vadErr)
	}
	return vadEngine.NewStream()
}

// ------------------------------------------------------------------ audio

var (
	clipOnce sync.Once
	clips    map[string][]float32
)

func clip(t *testing.T, name string, from, to float64) []float32 {
	t.Helper()
	clipOnce.Do(func() {
		clips = map[string][]float32{}
		for _, n := range []string{"hey_mycroft_test.wav", "office_lights.wav"} {
			data, err := os.ReadFile(filepath.Join("testdata", n))
			if err == nil {
				clips[n], err = decodeWAV(data)
			}
			if err != nil {
				panic(err)
			}
		}
	})
	a := clips[name]
	end := len(a)
	if to > 0 {
		end = int(to * rate)
	}
	return a[int(from*rate):end]
}

func wakePhrase(t *testing.T) []float32 { return clip(t, "hey_mycroft_test.wav", 0, 0) }

// command is about 2.7 s of speech, isolated by silence in the source clip.
func command(t *testing.T) []float32 { return clip(t, "office_lights.wav", 26.0, 28.7) }

func shortReply(t *testing.T) []float32 { return clip(t, "office_lights.wav", 31.0, 32.6) }

// agentVoice is continuous speech used as the agent's synthesized voice.
func agentVoice(t *testing.T, seconds float64) []float32 {
	return clip(t, "office_lights.wav", 2.0, 2.0+seconds)
}

func decodeWAV(data []byte) ([]float32, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, errors.New("not a WAV file")
	}
	var format struct {
		Tag, Channels uint16
		Rate          uint32
		_             uint32
		_             uint16
		Bits          uint16
	}
	for chunk := data[12:]; len(chunk) >= 8; {
		id, size := string(chunk[:4]), int(binary.LittleEndian.Uint32(chunk[4:8]))
		if size > len(chunk)-8 {
			return nil, errors.New("truncated chunk")
		}
		body := chunk[8 : 8+size]
		switch id {
		case "fmt ":
			if err := binary.Read(bytes.NewReader(body), binary.LittleEndian, &format); err != nil {
				return nil, err
			}
		case "data":
			if format.Tag != 1 || format.Channels != 1 || format.Rate != rate || format.Bits != 16 {
				return nil, errors.New("expected mono 16-bit PCM at 16 kHz")
			}
			out := make([]float32, size/2)
			for i := range out {
				out[i] = float32(int16(binary.LittleEndian.Uint16(body[2*i:]))) / 32768
			}
			return out, nil
		}
		chunk = chunk[8+size+size%2:]
	}
	return nil, errors.New("no data chunk")
}

// ------------------------------------------------------------------- room

// room is a simulated speaker -> room -> mic path: the agent's audio comes
// back delayed (80 ms), reverberant (300 ms RT60), slightly distorted and 6 dB
// down, plus room noise. With echo off it is a headset: the mic hears only
// the user. Ported from wonky-talkie's EchoRoom.
type room struct {
	echo       bool
	rir        []float64 // impulse response from the first nonzero tap
	delay      int
	distortion float64
	noise      float64
	rng        *rand.Rand
	played     []float64 // distorted speaker history, newest last
}

func newRoom(echo bool) *room {
	const delayMS, gainDB, rt60MS = 80, -6, 300
	rng := rand.New(rand.NewPCG(1, 2))
	g := math.Pow(10, gainDB/20.0)
	d := rate * delayMS / 1000
	n := rate * (delayMS + rt60MS) / 1000
	rir := make([]float64, n-d)
	rir[0] = g
	for i := 1; i < len(rir); i++ {
		rir[i] = rng.NormFloat64() * math.Exp(-float64(i-1)/(rate*rt60MS/6900.0)) * 0.16 * g
	}
	return &room{echo: echo, rir: rir, delay: d, distortion: 1.5, noise: math.Pow(10, -65/20.0), rng: rng}
}

// mix returns what the mic hears while the speaker plays ref.
func (r *room) mix(user, ref []float32) []float32 {
	if !r.echo {
		return user
	}
	mic := make([]float32, len(user))
	for i, u := range user {
		x := float64(ref[i])
		if r.distortion != 0 { // speaker nonlinearity, which linear AEC can't remove
			x = math.Tanh(x*r.distortion) / r.distortion
		}
		r.played = append(r.played, x)
		// Echo: the speaker signal from delay samples ago through the room.
		var echo float64
		last := len(r.played) - 1 - r.delay
		for k := 0; k < len(r.rir) && last-k >= 0; k++ {
			if s := r.played[last-k]; s != 0 {
				echo += r.rir[k] * s
			}
		}
		// The converter clips at full scale.
		mic[i] = float32(max(-1, min(1, float64(u)+echo+r.rng.NormFloat64()*r.noise)))
	}
	if keep := r.delay + len(r.rir); len(r.played) > 2*keep {
		r.played = append(r.played[:0], r.played[len(r.played)-keep:]...)
	}
	return mic
}

// ---------------------------------------------------------------- harness

// scriptedWake fires at fixed stream positions.
type scriptedWake struct {
	pending []int64
	total   int64
}

func (w *scriptedWake) Write(_ context.Context, samples []float32) ([]int64, error) {
	w.total += int64(len(samples))
	var fired []int64
	for len(w.pending) > 0 && w.pending[0] <= w.total {
		fired = append(fired, w.pending[0])
		w.pending = w.pending[1:]
	}
	return fired, nil
}

type options struct {
	wakeAt   []float64              // seconds
	stt      func([]float32) string // the transcriber's text for an utterance
	sttDelay time.Duration          // stream time a transcription takes
	echo     bool                   // the speaker's audio reaches the mic
	edit     func(*Config)          // adjust the defaults
}

type harness struct {
	t      *testing.T
	e      *Engine
	player *audio.Player
	room   *room
	noise  *rand.Rand
	log    []Event
	stt    func([]float32) string
	delay  int64
	jobs   []pendingJob
}

type pendingJob struct {
	job Job
	due int64
}

func newHarness(t *testing.T, o options) *harness {
	t.Helper()
	stream := vadStream(t)
	cfg := Defaults()
	if o.edit != nil {
		o.edit(&cfg)
	}
	h := &harness{t: t, player: audio.NewPlayer(rate, 15*time.Millisecond), room: newRoom(o.echo),
		noise: rand.New(rand.NewPCG(3, 4)), stt: o.stt, delay: samples(o.sttDelay)}
	if h.stt == nil {
		h.stt = func([]float32) string { return "hello" }
	}
	wake := &scriptedWake{}
	for _, s := range o.wakeAt {
		wake.pending = append(wake.pending, int64(s*rate))
	}
	var err error
	h.e, err = New(cfg, Deps{
		VAD:    func(c []float32) (float32, error) { return stream.Process(context.Background(), c) },
		Wake:   wake,
		Player: h.player,
		Transcribe: func(j Job) {
			h.jobs = append(h.jobs, pendingJob{j, h.e.Position() + h.delay})
		},
		Emit: func(ev Event) { h.log = append(h.log, ev) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// noise returns room noise at about -65 dBFS.
func (h *harness) silence(seconds float64) []float32 {
	out := make([]float32, int(seconds*rate))
	amp := math.Pow(10, -65/20.0)
	for i := range out {
		out[i] = float32(h.noise.NormFloat64() * amp)
	}
	return out
}

// feed plays the user's audio into the mic, 32 ms at a time.
func (h *harness) feed(user []float32) {
	h.t.Helper()
	for start := 0; start < len(user); start += chunk {
		block := user[start:min(start+chunk, len(user))]
		ref := make([]float32, len(block))
		h.e.PlayerEvents(h.player.Read(ref))
		if err := h.e.Audio(context.Background(), h.room.mix(block, ref), ref); err != nil {
			h.t.Fatal(err)
		}
		h.deliver()
	}
}

// deliver hands finished transcriptions back to the engine.
func (h *harness) deliver() {
	for len(h.jobs) > 0 && h.jobs[0].due <= h.e.Position() {
		j := h.jobs[0]
		h.jobs = h.jobs[1:]
		h.e.Transcribed(j.job, h.stt(j.job.Audio), nil, duration(h.delay))
	}
}

// play queues the agent's audio, as speaking a reply does.
func (h *harness) play(id string, samples []float32, text string, then audio.Then, timeout time.Duration) {
	h.player.Start(id, then, timeout)
	h.player.Add(id, samples, text)
	h.player.End(id)
}

// types lists the events so far, without state changes.
func (h *harness) types() []EventType {
	var out []EventType
	for _, e := range h.log {
		if e.Type != StateChanged {
			out = append(out, e.Type)
		}
	}
	return out
}

func (h *harness) states() []State {
	var out []State
	for _, e := range h.log {
		if e.Type == StateChanged {
			out = append(out, e.To)
		}
	}
	return out
}

// find returns the first event of type typ that match accepts.
func (h *harness) find(typ EventType, match func(Event) bool) Event {
	h.t.Helper()
	for _, e := range h.log {
		if e.Type == typ && (match == nil || match(e)) {
			return e
		}
	}
	h.t.Fatalf("no matching %s in %v", typ, h.types())
	return Event{}
}

func (h *harness) has(typ EventType) bool { return slices.Contains(h.types(), typ) }

func (h *harness) count(typ EventType) int {
	n := 0
	for _, e := range h.types() {
		if e == typ {
			n++
		}
	}
	return n
}

func text(s string) func(Event) bool { return func(e Event) bool { return e.Text == s } }

// replies returns a transcriber that answers with each text in turn.
func replies(texts ...string) func([]float32) string {
	return func([]float32) string {
		if len(texts) == 0 {
			return ""
		}
		t := texts[0]
		texts = texts[1:]
		return t
	}
}
