package talkie

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	kokoro "github.com/androiddrew/go-kokoro"
	"github.com/androiddrew/go-kokoro/text"
	parakeet "github.com/androiddrew/go-parakeet"
	phonmatchnet "github.com/androiddrew/go-phonmatchnet"
	silerovad "github.com/androiddrew/go-silero-vad"

	"github.com/androiddrew/talkie-core/internal/audio"
	"github.com/androiddrew/talkie-core/internal/engine"
	"github.com/androiddrew/talkie-core/internal/wake"
)

// Config selects the models. Every model runs in-process on one ONNX Runtime
// environment.
type Config struct {
	// ORTLibrary is the ONNX Runtime library, a path or OS-loader name,
	// shared by every model. A CUDA build is needed for CUDA.
	ORTLibrary string
	// CUDA runs speech-to-text and text-to-speech on GPU DeviceID. The wake
	// word and VAD always run on the CPU.
	CUDA     bool
	DeviceID int

	Parakeet ParakeetConfig
	Kokoro   KokoroConfig

	// WakeWord is the phrase that starts a session, such as "hey computer".
	// Empty disables it: sessions start with Listen or when the agent speaks.
	WakeWord string
	// WakeFAR selects PhonMatchNet's operating point by its target
	// false-accept rate; zero selects 0.01.
	WakeFAR float64

	// OutputRate is the speaker's sample rate; zero selects 48 kHz.
	OutputRate int

	// DisableEchoCancellation turns off the built-in echo canceller
	// (go-speexdsp), for a headset or a microphone that is already
	// echo-cancelled, such as PipeWire's echo-cancel source. Barge-in still
	// verifies interruptions against the predicted echo.
	DisableEchoCancellation bool
}

// ParakeetConfig locates the Parakeet TDT 0.6B v3 ONNX files.
type ParakeetConfig struct {
	// Dir holds nemo128.onnx, encoder-model.onnx (with its .onnx.data) and
	// decoder_joint-model.onnx.
	Dir string
	// INT8 uses encoder-model.int8.onnx and decoder_joint-model.int8.onnx,
	// smaller and faster on CPU.
	INT8 bool
}

// KokoroConfig locates the Kokoro v1.0 files and sets the voice.
type KokoroConfig struct {
	ModelPath  string // kokoro-v1.0.onnx
	VoicesPath string // voices-v1.0.bin
	// Voice is a US (a*) or UK (b*) English voice; empty selects af_heart.
	Voice string
	// Speed is 0.5 to 2; zero selects 1.
	Speed float32
	// FirstChunkPhonemes and MaxChunkPhonemes limit each model run. A run in
	// progress can't be interrupted, so shorter runs make barge-in quicker
	// and start the first sentence sooner. Zero selects 80 and 160.
	FirstChunkPhonemes, MaxChunkPhonemes int
}

// Talkie is a loaded voice front end for an agent.
type Talkie struct {
	vad    *silerovad.Engine
	asr    *parakeet.Engine
	tts    *text.Pipeline
	wake   *phonmatchnet.Engine
	player *audio.Player
	conv   *conversation
}

// New loads the models and returns a front end that hands what the user says
// to agent. Close is mandatory.
func New(ctx context.Context, cfg Config, agent Agent) (_ *Talkie, err error) {
	if agent == nil {
		return nil, errors.New("talkie: agent is required")
	}
	if cfg.OutputRate == 0 {
		cfg.OutputRate = 48000
	}
	t := &Talkie{}
	defer func() {
		if err != nil {
			err = errors.Join(err, t.Close())
		}
	}()
	if t.vad, err = silerovad.New(silerovad.Config{ORTLibrary: cfg.ORTLibrary}); err != nil {
		return nil, fmt.Errorf("talkie: load VAD: %w", err)
	}
	if t.asr, err = loadParakeet(cfg); err != nil {
		return nil, fmt.Errorf("talkie: load Parakeet: %w", err)
	}
	if t.tts, err = loadKokoro(cfg); err != nil {
		return nil, fmt.Errorf("talkie: load Kokoro: %w", err)
	}
	ec := engine.Defaults()
	ec.Echo.Enabled = !cfg.DisableEchoCancellation
	var listener engine.Wake
	if cfg.WakeWord != "" {
		if listener, err = t.loadWake(ctx, cfg); err != nil {
			return nil, fmt.Errorf("talkie: load wake word: %w", err)
		}
		ec.Wake.StripPhrases = []string{cfg.WakeWord}
	}
	t.player = audio.NewPlayer(cfg.OutputRate, 15*time.Millisecond)
	stream := t.vad.NewStream()
	t.conv, err = newConversation(ctx, conversationConfig{
		engine: ec,
		vad:    func(c []float32) (float32, error) { return stream.Process(ctx, c) },
		wake:   listener,
		player: t.player,
		agent:  agent,
		voice:  newKokoroVoice(t.tts, cfg.Kokoro, cfg.OutputRate),
		stt: func(ctx context.Context, samples []float32) (string, error) {
			r, err := t.asr.Transcribe(ctx, parakeet.Request{Samples: samples, SampleRate: parakeet.SampleRate})
			return strings.TrimSpace(r.Text), err
		},
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

func loadParakeet(cfg Config) (*parakeet.Engine, error) {
	encoder, decoder := "encoder-model.onnx", "decoder_joint-model.onnx"
	if cfg.Parakeet.INT8 {
		encoder, decoder = "encoder-model.int8.onnx", "decoder_joint-model.int8.onnx"
	}
	pc := parakeet.Config{
		ORTLibrary:       cfg.ORTLibrary,
		PreprocessorPath: filepath.Join(cfg.Parakeet.Dir, "nemo128.onnx"),
		EncoderPath:      filepath.Join(cfg.Parakeet.Dir, encoder),
		DecoderPath:      filepath.Join(cfg.Parakeet.Dir, decoder),
		DeviceID:         cfg.DeviceID,
	}
	if cfg.CUDA {
		pc.Provider = parakeet.CUDA
	}
	return parakeet.New(pc)
}

func loadKokoro(cfg Config) (*text.Pipeline, error) {
	kc := kokoro.Config{ModelPath: cfg.Kokoro.ModelPath, VoicesPath: cfg.Kokoro.VoicesPath, DeviceID: cfg.DeviceID}
	if cfg.CUDA {
		kc.Provider = kokoro.CUDA
	}
	return text.New(text.Config{ORTLibrary: cfg.ORTLibrary, Synthesis: kc})
}

func (t *Talkie) loadWake(ctx context.Context, cfg Config) (engine.Wake, error) {
	var err error
	if t.wake, err = phonmatchnet.New(phonmatchnet.Config{ORTLibrary: cfg.ORTLibrary}); err != nil {
		return nil, err
	}
	keyword, err := t.wake.Keyword(ctx, cfg.WakeWord)
	if err != nil {
		return nil, err
	}
	// phonmatchnet-run's listen defaults: a 1.5 s window every 250 ms, and a
	// window's length before the next detection.
	const window = 3 * phonmatchnet.SampleRate / 2
	l, err := wake.New(wake.Config{Window: window, Hop: phonmatchnet.SampleRate / 4, Confirm: 1, Refractory: window},
		wake.PhonMatchNet(t.wake, keyword, cfg.WakeFAR))
	if err != nil {
		return nil, err
	}
	return wakeListener{l}, nil
}

// Close releases the models. The ONNX Runtime environment stays loaded until
// the process exits.
func (t *Talkie) Close() error {
	if t.conv != nil {
		if tr := t.conv.turn; tr != nil {
			tr.cancel()
			<-tr.done
		}
	}
	var errs []error
	if t.tts != nil {
		errs = append(errs, t.tts.Close())
	}
	if t.asr != nil {
		errs = append(errs, t.asr.Close())
	}
	if t.wake != nil {
		errs = append(errs, t.wake.Close())
	}
	if t.vad != nil {
		errs = append(errs, t.vad.Close())
	}
	return errors.Join(errs...)
}

// wakeListener adapts the sliding-window listener to the engine.
type wakeListener struct {
	l *wake.Listener[phonmatchnet.Result]
}

func (w wakeListener) Write(ctx context.Context, samples []float32) ([]int64, error) {
	var detections []int64
	err := w.l.Write(ctx, samples, func(e wake.Event[phonmatchnet.Result]) error {
		if e.Detected {
			detections = append(detections, e.Window.End)
		}
		return nil
	})
	return detections, err
}

// kokoroVoice speaks a sentence with go-kokoro/text, resampled from Kokoro's
// 24 kHz to the speaker's rate, with a short pause after it.
type kokoroVoice struct {
	tts     *text.Pipeline
	request text.Request
	rate    int
	pause   int // samples of silence after each sentence
}

func newKokoroVoice(tts *text.Pipeline, c KokoroConfig, rate int) *kokoroVoice {
	r := text.Request{
		Voice: c.Voice, Speed: c.Speed, Trim: true,
		Prep:     text.PrepOptions{Markdown: true, Normalize: true},
		Chunking: kokoro.StreamOptions{FirstChunkPhonemes: c.FirstChunkPhonemes, MaxChunkPhonemes: c.MaxChunkPhonemes},
	}
	if r.Voice == "" {
		r.Voice = "af_heart"
	}
	if r.Speed == 0 {
		r.Speed = 1
	}
	if r.Chunking.FirstChunkPhonemes == 0 {
		r.Chunking.FirstChunkPhonemes = 80
	}
	if r.Chunking.MaxChunkPhonemes == 0 {
		r.Chunking.MaxChunkPhonemes = 160
	}
	// kokoro-run's server.chunk_gap between sentences.
	return &kokoroVoice{tts: tts, request: r, rate: rate, pause: rate * 120 / 1000}
}

func (v *kokoroVoice) Speak(ctx context.Context, sentence string, yield func([]float32) error) error {
	if !strings.ContainsFunc(sentence, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		return nil // punctuation alone has nothing to say
	}
	r := v.request
	r.Text = sentence
	resampler := audio.NewResampler(text.SampleRate, v.rate)
	for chunk, err := range v.tts.Stream(ctx, r) {
		if err != nil {
			return err
		}
		if out := resampler.Process(chunk.PCM); len(out) > 0 {
			if err := yield(out); err != nil {
				return err
			}
		}
	}
	return yield(make([]float32, v.pause))
}
