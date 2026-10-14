package engine

import (
	"math"
	"time"

	"github.com/androiddrew/go-speexdsp/echo"
	"github.com/androiddrew/go-speexdsp/preprocess"
)

// echoFrame is the echo stage's frame: 16 ms, two per VAD chunk.
const echoFrame = 256

// EchoConfig sets echo cancellation of the agent's own voice, wonky-talkie's
// settings for SpeexDSP.
type EchoConfig struct {
	Enabled bool
	// Filter is the longest speaker-to-mic delay plus reverb the canceller
	// can model. The audio device's round trip counts too.
	Filter time.Duration
	// Hold keeps cancelling this long after the speaker falls silent, for
	// the echo's tail.
	Hold time.Duration
	// NoiseSuppressDB is the denoiser's maximum attenuation. SpeexDSP
	// applies residual echo suppression through the denoiser's gains, so it
	// runs while cancelling; keep it mild so the user's voice survives.
	NoiseSuppressDB int
	// EchoSuppressDB and EchoSuppressActiveDB are the maximum residual echo
	// attenuation without and with the user talking.
	EchoSuppressDB, EchoSuppressActiveDB int
}

// echoStage sits in front of the engine. It cuts the microphone into 16 ms
// frames aligned with what the speaker played and, while the speaker is
// playing or within Hold after, cancels the echo and suppresses what is
// left. Otherwise the microphone passes through untouched.
//
// The preprocessor's output lags its input by one frame, so the stage
// delays everything by one frame: passed-through audio and the speaker
// reference too. The timeline stays continuous when cancelling starts or
// stops, and the engine's echo energies stay aligned.
type echoStage struct {
	canceller *echo.Canceller
	pre       *preprocess.Preprocessor
	hold      int64

	mic, ref          []float32 // input not yet framed
	prevMic, prevRef  []float32 // the previous frame, one frame of delay
	sinceRef          int64     // samples since the reference had signal
	active, wasActive bool

	// Echo return loss enhancement since resetStats: energy into the
	// canceller over energy out, both of the same frame.
	in, out float64
}

// NewCanceller returns an echo canceller configured as the engine's, for
// training one whose Snapshot the engine can restore. Feed it 16 kHz
// microphone and speaker frames of EchoFrame samples, aligned as Audio
// receives them.
func NewCanceller(c EchoConfig) (*echo.Canceller, error) {
	return echo.New(echoFrame, int(samples(c.Filter)), chunkRate)
}

// EchoFrame is the echo canceller's frame length in 16 kHz samples.
const EchoFrame = echoFrame

func newEchoStage(c EchoConfig) (*echoStage, error) {
	canceller, err := NewCanceller(c)
	if err != nil {
		return nil, err
	}
	pre, err := preprocess.New(preprocess.Config{
		FrameSize: echoFrame, SampleRate: chunkRate,
		NoiseSuppressDB: c.NoiseSuppressDB, EchoSuppressDB: c.EchoSuppressDB, EchoSuppressActiveDB: c.EchoSuppressActiveDB,
		Echo: canceller,
	})
	if err != nil {
		return nil, err
	}
	return &echoStage{
		canceller: canceller, pre: pre, hold: samples(c.Hold),
		prevMic: make([]float32, echoFrame), prevRef: make([]float32, echoFrame),
		sinceRef: math.MaxInt64 / 2,
	}, nil
}

// referenceFloor is the level below which the reference counts as silence:
// wonky's 8 in 16-bit units.
const referenceFloor = 8.0 / 32768

// process returns the cleaned microphone and its matching reference for
// every whole frame of input, one frame late.
func (s *echoStage) process(mic, ref []float32) ([]float32, []float32, error) {
	s.mic = append(s.mic, mic...)
	s.ref = append(s.ref, ref...)
	n := len(s.mic) / echoFrame * echoFrame
	outMic, outRef := make([]float32, 0, n), make([]float32, 0, n)
	for i := 0; i < n; i += echoFrame {
		m, r := s.mic[i:i+echoFrame], s.ref[i:i+echoFrame]
		if peak(r) > referenceFloor {
			s.sinceRef = 0
		} else {
			s.sinceRef += echoFrame
		}
		s.wasActive, s.active = s.active, s.sinceRef <= s.hold
		var out []float32
		if s.active {
			cleaned := make([]float32, echoFrame)
			if err := s.canceller.Cancel(m, r, cleaned); err != nil {
				return nil, nil, err
			}
			if _, err := s.pre.Run(cleaned); err != nil {
				return nil, nil, err
			}
			// cleaned now holds the previous frame, processed. Right after
			// cancelling starts, the preprocessor still holds a frame from
			// long ago: pass the previous frame through instead.
			out = cleaned
			if !s.wasActive {
				out = s.prevMic
			} else {
				s.in += energy(s.prevMic)
				s.out += energy(cleaned)
			}
		} else {
			out = s.prevMic
		}
		outMic = append(outMic, out...)
		outRef = append(outRef, s.prevRef...)
		s.prevMic = append(s.prevMic[:0:0], m...)
		s.prevRef = append(s.prevRef[:0:0], r...)
	}
	s.mic = append(s.mic[:0:0], s.mic[n:]...)
	s.ref = append(s.ref[:0:0], s.ref[n:]...)
	return outMic, outRef, nil
}

func peak(x []float32) float32 {
	var p float32
	for _, v := range x {
		p = max(p, v, -v)
	}
	return p
}

func (s *echoStage) resetStats() { s.in, s.out = 0, 0 }

// erle is the echo removed since resetStats, in dB, or zero if nothing
// was cancelled.
func (s *echoStage) erle() float64 {
	if s.in <= 0 || s.out <= 0 {
		return 0
	}
	return round1(10 * math.Log10(s.in/s.out))
}
