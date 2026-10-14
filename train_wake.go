package talkie

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	phonmatchnet "github.com/androiddrew/go-phonmatchnet"
	silerovad "github.com/androiddrew/go-silero-vad"

	"github.com/androiddrew/talkie-core/internal/audio"
	"github.com/androiddrew/talkie-core/internal/device"
	"github.com/androiddrew/talkie-core/internal/wake"
)

// Wake word training settings.
const (
	wakeRepeats   = 5
	wakeRecording = 15 * time.Second // five times with pauses
	readRecording = 12 * time.Second
	// wakePad is audio kept around each repetition VAD finds, as the live
	// listener's windows would include it.
	wakePad = 500 * time.Millisecond
)

// readingSentence is ordinary speech for the user to read aloud: what
// talkie must not wake on.
const readingSentence = "The weather today is mild, with a light breeze from the west and a chance of rain later this evening, so you may want to bring a coat."

// wakeConfig is the live listener's: a 1.5 s window every 250 ms, as in
// phonmatchnet-run's listen.
func wakeConfig() wake.Config {
	const window = 3 * phonmatchnet.SampleRate / 2
	return wake.Config{Window: window, Hop: phonmatchnet.SampleRate / 4, Confirm: 1, Refractory: window}
}

// wakeScorer scores recordings for the wake word as the live listener
// would hear them.
type wakeScorer struct {
	keyword string
	far     float64 // the operating point the live listener would use
	vad     *silerovad.Engine
	pm      *phonmatchnet.Engine
	kw      phonmatchnet.Keyword
}

func newWakeScorer(ctx context.Context, cfg Config) (_ *wakeScorer, err error) {
	s := &wakeScorer{keyword: cfg.WakeWord, far: cfg.WakeFAR}
	if s.far == 0 {
		s.far = phonmatchnet.DefaultFAR
	}
	if s.vad, err = silerovad.New(silerovad.Config{ORTLibrary: cfg.ORTLibrary}); err != nil {
		return nil, err
	}
	if s.pm, err = phonmatchnet.New(phonmatchnet.Config{ORTLibrary: cfg.ORTLibrary}); err != nil {
		s.close()
		return nil, err
	}
	if s.kw, err = s.pm.Keyword(ctx, cfg.WakeWord); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

func (s *wakeScorer) close() {
	if s.pm != nil {
		s.pm.Close()
	}
	if s.vad != nil {
		s.vad.Close()
	}
}

// peak is the highest score of any window the live listener would score
// in samples, which are 16 kHz: padded with silence to a whole window plus
// a hop if shorter.
func (s *wakeScorer) peak(ctx context.Context, samples []float32) (float64, error) {
	c := wakeConfig()
	if n := c.Window + c.Hop; len(samples) < n {
		samples = append(append([]float32(nil), samples...), make([]float32, n-len(samples))...)
	}
	best := 0.0
	l, err := wake.New(c, func(ctx context.Context, w wake.Window) (float64, bool, error) {
		r, err := s.pm.Detect(ctx, phonmatchnet.Request{Samples: w.Samples, SampleRate: phonmatchnet.SampleRate, Keyword: s.kw})
		return r.Score, false, err
	})
	if err != nil {
		return 0, err
	}
	err = l.Write(ctx, samples, func(e wake.Event[float64]) error {
		best = max(best, e.Result)
		return nil
	})
	return best, err
}

// repetitions scores each utterance VAD finds in a recording of the user
// saying the wake word.
func (s *wakeScorer) repetitions(ctx context.Context, samples []float32) ([]float64, error) {
	r, err := s.vad.Speech(ctx, silerovad.Request{
		Samples: samples, SampleRate: silerovad.SampleRate,
		Segmenter: silerovad.SegmenterConfig{MinSilence: 300 * time.Millisecond},
	})
	if err != nil {
		return nil, err
	}
	pad := int64(wakePad.Seconds() * audio.SampleRate)
	var scores []float64
	for _, seg := range r.Segments {
		from, to := max(0, seg.Start-pad), min(int64(len(samples)), seg.End+pad)
		score, err := s.peak(ctx, samples[from:to])
		if err != nil {
			return nil, err
		}
		scores = append(scores, score)
	}
	return scores, nil
}

// trainWake asks the user to say the wake word and then read a sentence,
// and chooses the threshold between them.
func trainWake(ctx context.Context, prompt Prompt, open opener, dc device.Config, s *wakeScorer) (*WakeProfile, error) {
	listen := func(step Step, d time.Duration) ([]float32, error) {
		if err := prompt(ctx, step); err != nil {
			return nil, err
		}
		heard, _, _, _, err := record(ctx, open, dc, nil, int(d.Seconds()*float64(dc.Rate)))
		if err != nil {
			return nil, err
		}
		return clip(audio.NewResampler(dc.Rate, audio.SampleRate).Process(heard)), nil
	}
	said, err := listen(Step{Name: "wake", Instruction: fmt.Sprintf(
		"After you press Enter, say %q %d times, pausing about a second between each. You have %d seconds.",
		s.keyword, wakeRepeats, int(wakeRecording.Seconds()))}, wakeRecording)
	if err != nil {
		return nil, err
	}
	positives, err := s.repetitions(ctx, said)
	if err != nil {
		return nil, err
	}
	// Keep the strongest wakeRepeats: VAD may also catch a cough or a word.
	if len(positives) > wakeRepeats {
		positives = topN(positives, wakeRepeats)
	}
	read, err := listen(Step{Name: "speech", Instruction: fmt.Sprintf(
		"After you press Enter, read this aloud at a normal pace:\n\n    %s", readingSentence)}, readRecording)
	if err != nil {
		return nil, err
	}
	negativeMax, err := s.peak(ctx, read)
	if err != nil {
		return nil, err
	}
	w := chooseWakeThreshold(s.keyword, positives, negativeMax, s.pm.Thresholds()[s.far])
	return &w, nil
}

func topN(xs []float64, n int) []float64 {
	sorted := slices.Sorted(slices.Values(xs))
	slices.Reverse(sorted)
	return sorted[:n]
}

// sameWakeWord reports whether two wake words are the same once normalized,
// as PhonMatchNet compares them.
func sameWakeWord(a, b string) bool {
	na, errA := phonmatchnet.NormalizeKeyword(a)
	nb, errB := phonmatchnet.NormalizeKeyword(b)
	if errA != nil || errB != nil {
		return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
	}
	return na == nb
}
