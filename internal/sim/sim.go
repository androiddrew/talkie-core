// Package sim is talkie's test bench: real speech clips, the real Silero VAD,
// a scripted wake word and a simulated room whose speaker echoes into the
// microphone. Tests drive an engine with it in lock-step, like an audio
// device but faster than real time. Ported from wonky-talkie's
// tests/harness.py and EchoRoom.
package sim

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	silerovad "github.com/androiddrew/go-silero-vad"
)

// Rate is the microphone and simulated speaker rate.
const Rate = 16000

var (
	vadOnce   sync.Once
	vadEngine *silerovad.Engine
	vadErr    error
)

// VAD returns a Silero VAD stream for one test, skipping the test unless
// TALKIE_TEST_ORT names an ONNX Runtime library.
func VAD(tb testing.TB) func([]float32) (float32, error) {
	tb.Helper()
	library := os.Getenv("TALKIE_TEST_ORT")
	if library == "" {
		tb.Skip("set TALKIE_TEST_ORT to an ONNX Runtime library for conversation scenarios")
	}
	vadOnce.Do(func() { vadEngine, vadErr = silerovad.New(silerovad.Config{ORTLibrary: library}) })
	if vadErr != nil {
		tb.Fatal(vadErr)
	}
	stream := vadEngine.NewStream()
	return func(c []float32) (float32, error) { return stream.Process(context.Background(), c) }
}

// ------------------------------------------------------------------ clips

var (
	clipOnce sync.Once
	clips    map[string][]float32
	clipErr  error
)

// Clip returns from..to seconds of a testdata clip; to 0 means its end.
func Clip(tb testing.TB, name string, from, to float64) []float32 {
	tb.Helper()
	clipOnce.Do(func() {
		_, file, _, _ := runtime.Caller(0)
		clips = map[string][]float32{}
		for _, n := range []string{"hey_mycroft_test.wav", "office_lights.wav"} {
			var data []byte
			if data, clipErr = os.ReadFile(filepath.Join(filepath.Dir(file), "testdata", n)); clipErr != nil {
				return
			}
			if clips[n], clipErr = DecodeWAV(data); clipErr != nil {
				return
			}
		}
	})
	if clipErr != nil {
		tb.Fatal(clipErr)
	}
	a := clips[name]
	end := len(a)
	if to > 0 {
		end = int(to * Rate)
	}
	return a[int(from*Rate):end]
}

// WakePhrase is "hey mycroft", 0.95 s.
func WakePhrase(tb testing.TB) []float32 { return Clip(tb, "hey_mycroft_test.wav", 0, 0) }

// Command is about 2.7 s of speech, isolated by silence in the source clip:
// "So if we turn that off, does it work with".
func Command(tb testing.TB) []float32 { return Clip(tb, "office_lights.wav", 26.0, 28.7) }

// ShortReply is about 1.6 s of speech: "Turn on the office lights."
func ShortReply(tb testing.TB) []float32 { return Clip(tb, "office_lights.wav", 31.0, 32.6) }

// AgentVoice is continuous speech used as the agent's synthesized voice.
func AgentVoice(tb testing.TB, seconds float64) []float32 {
	return Clip(tb, "office_lights.wav", 2.0, 2.0+seconds)
}

// DecodeWAV decodes mono 16-bit PCM at 16 kHz to float32.
func DecodeWAV(data []byte) ([]float32, error) {
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
			if format.Tag != 1 || format.Channels != 1 || format.Rate != Rate || format.Bits != 16 {
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

// Noise makes room noise at about -65 dBFS, reproducibly.
type Noise struct{ rng *rand.Rand }

// NewNoise returns a noise source with a fixed seed.
func NewNoise() *Noise { return &Noise{rand.New(rand.NewPCG(3, 4))} }

// Silence returns seconds of room noise.
func (n *Noise) Silence(seconds float64) []float32 {
	out := make([]float32, int(seconds*Rate))
	amp := math.Pow(10, -65/20.0)
	for i := range out {
		out[i] = float32(n.rng.NormFloat64() * amp)
	}
	return out
}

// ------------------------------------------------------------------- room

// Room is a simulated speaker -> room -> mic path: the agent's audio comes
// back delayed (80 ms), reverberant (300 ms RT60), slightly distorted and 6 dB
// down, plus room noise. Without echo it is a headset: the mic hears only the
// user.
type Room struct {
	echo       bool
	rir        []float64 // impulse response from the first nonzero tap
	delay      int
	distortion float64
	noise      float64
	rng        *rand.Rand
	played     []float64 // distorted speaker history, newest last
}

// NewRoom returns a room, with the speaker's echo reaching the mic or not.
func NewRoom(echo bool) *Room {
	const delayMS, gainDB, rt60MS = 80, -6, 300
	rng := rand.New(rand.NewPCG(1, 2))
	g := math.Pow(10, gainDB/20.0)
	d := Rate * delayMS / 1000
	n := Rate * (delayMS + rt60MS) / 1000
	rir := make([]float64, n-d)
	rir[0] = g
	for i := 1; i < len(rir); i++ {
		rir[i] = rng.NormFloat64() * math.Exp(-float64(i-1)/(Rate*rt60MS/6900.0)) * 0.16 * g
	}
	return &Room{echo: echo, rir: rir, delay: d, distortion: 1.5, noise: math.Pow(10, -65/20.0), rng: rng}
}

// Mix returns what the mic hears while the user says user and the speaker
// plays ref.
func (r *Room) Mix(user, ref []float32) []float32 {
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

// ------------------------------------------------------------------- wake

// Wake fires at fixed stream positions.
type Wake struct {
	pending []int64
	total   int64
}

// NewWake fires once at each of the given stream times, in seconds.
func NewWake(at ...float64) *Wake {
	w := &Wake{}
	for _, s := range at {
		w.pending = append(w.pending, int64(s*Rate))
	}
	return w
}

// Write implements the engine's Wake.
func (w *Wake) Write(_ context.Context, samples []float32) ([]int64, error) {
	w.total += int64(len(samples))
	var fired []int64
	for len(w.pending) > 0 && w.pending[0] <= w.total {
		fired = append(fired, w.pending[0])
		w.pending = w.pending[1:]
	}
	return fired, nil
}
