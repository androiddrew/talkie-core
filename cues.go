package talkie

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/androiddrew/talkie-core/internal/audio"
	"github.com/androiddrew/talkie-core/internal/engine"
)

// Cues are short sounds that say what talkie is doing, for a device without
// a screen. Each is optional: a nil Sound plays nothing. talkie plays them
// through its own output, so the echo canceller removes them from the
// microphone like the agent's voice; they don't count as the agent speaking.
type Cues struct {
	// Listening plays when talkie starts listening at the user's request:
	// the wake word, Talkie.Listen opening a session, or Talkie.Interrupt.
	// Follow-up windows after a reply are silent.
	Listening *Sound
	// Thinking loops while the user waits: from ThinkingDelay after they
	// stop talking, through transcription and the agent's work, until the
	// reply starts playing, the user interrupts, or the session ends. It
	// should loop seamlessly. ThinkingDelay is zero for 500 ms, so a quick
	// reply doesn't blip.
	Thinking      *Sound
	ThinkingDelay time.Duration
	// Idle plays when the session ends and talkie waits for the wake word.
	Idle *Sound
}

// Sound is mono audio for a cue: Samples in [-1, 1] at Rate samples per
// second. talkie resamples it to Config.OutputRate.
type Sound struct {
	Samples []float32
	Rate    int
}

// DefaultCues returns talkie's built-in cues, at 48 kHz: two glassy notes a
// fifth up for Listening, the same down and a little quieter for Idle, and
// for Thinking a quiet fifth that swells and fades every 1.6 s.
func DefaultCues() Cues {
	const rate = 48000
	return Cues{
		Listening: &Sound{Samples: audio.ListeningSound(rate), Rate: rate},
		Thinking:  &Sound{Samples: audio.ThinkingSound(rate), Rate: rate},
		Idle:      &Sound{Samples: audio.IdleSound(rate), Rate: rate},
	}
}

// engineCues checks the cues and converts them to the player's rate.
func engineCues(c Cues, rate int) (engine.CuesConfig, error) {
	out := engine.CuesConfig{ThinkingDelay: c.ThinkingDelay}
	if out.ThinkingDelay == 0 {
		out.ThinkingDelay = 500 * time.Millisecond
	}
	if out.ThinkingDelay < 0 {
		return out, errors.New("talkie: Cues.ThinkingDelay is negative")
	}
	for _, s := range []struct {
		name  string
		sound *Sound
		loop  bool
		into  *[]float32
	}{
		{"Listening", c.Listening, false, &out.Listening},
		{"Thinking", c.Thinking, true, &out.Thinking},
		{"Idle", c.Idle, false, &out.Idle},
	} {
		if s.sound == nil {
			continue
		}
		samples, err := s.sound.at(rate, s.loop)
		if err != nil {
			return out, fmt.Errorf("talkie: Cues.%s: %w", s.name, err)
		}
		*s.into = samples
	}
	return out, nil
}

// at checks the sound and resamples it to rate. A loop is resampled from
// the middle of three periods, where the filter has settled, so it still
// loops seamlessly.
func (s *Sound) at(rate int, loop bool) ([]float32, error) {
	if s.Rate <= 0 {
		return nil, fmt.Errorf("sample rate %d", s.Rate)
	}
	if len(s.Samples) == 0 {
		return nil, errors.New("no samples")
	}
	for i, v := range s.Samples {
		if !(v >= -1 && v <= 1) {
			return nil, fmt.Errorf("sample %d is %v, outside [-1, 1]", i, v)
		}
	}
	if s.Rate == rate {
		return s.Samples, nil
	}
	n := int(math.Round(float64(len(s.Samples)) * float64(rate) / float64(s.Rate)))
	if n == 0 {
		return nil, errors.New("too short")
	}
	r := audio.NewResampler(s.Rate, rate)
	if loop {
		var out []float32
		for range 3 {
			out = append(out, r.Process(s.Samples)...)
		}
		return clip(out[len(out)-2*n : len(out)-n]), nil
	}
	out := r.Process(s.Samples)
	out = append(out, r.Process(make([]float32, s.Rate/20))...) // flush the filter: 50 ms
	return clip(out[:min(len(out), n+rate/100)]), nil           // and a little of its tail
}

// ReadWAV reads a WAV file's audio as a Sound for a cue: 8-, 16-, 24- or
// 32-bit integer PCM or 32-bit float, with any number of channels, which
// are averaged to mono.
func ReadWAV(r io.Reader) (*Sound, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, errors.New("talkie: not a WAV file")
	}
	var (
		format, channels, bits uint16
		rate                   uint32
		pcm                    []byte
		haveFormat             bool
	)
	for rest := data[12:]; len(rest) >= 8; {
		id, size := string(rest[:4]), int(binary.LittleEndian.Uint32(rest[4:8]))
		rest = rest[8:]
		if size > len(rest) {
			size = len(rest) // a truncated last chunk: use what is there
		}
		body := rest[:size]
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, errors.New("talkie: WAV format chunk too short")
			}
			format = binary.LittleEndian.Uint16(body[0:])
			channels = binary.LittleEndian.Uint16(body[2:])
			rate = binary.LittleEndian.Uint32(body[4:])
			bits = binary.LittleEndian.Uint16(body[14:])
			if format == 0xFFFE && size >= 26 { // WAVE_FORMAT_EXTENSIBLE: the subformat's first two bytes
				format = binary.LittleEndian.Uint16(body[24:])
			}
			haveFormat = true
		case "data":
			pcm = body
		}
		rest = rest[min(len(rest), size+size%2):] // chunks are padded to even sizes
	}
	if !haveFormat || pcm == nil {
		return nil, errors.New("talkie: WAV file has no format or data chunk")
	}
	if channels == 0 || rate == 0 {
		return nil, fmt.Errorf("talkie: WAV file has %d channels at %d Hz", channels, rate)
	}
	var decode func([]byte) float64
	switch {
	case format == 1 && bits == 8:
		decode = func(b []byte) float64 { return (float64(b[0]) - 128) / 128 }
	case format == 1 && bits == 16:
		decode = func(b []byte) float64 { return float64(int16(binary.LittleEndian.Uint16(b))) / 32768 }
	case format == 1 && bits == 24:
		decode = func(b []byte) float64 {
			return float64(int32(uint32(b[0])<<8|uint32(b[1])<<16|uint32(b[2])<<24)>>8) / (1 << 23)
		}
	case format == 1 && bits == 32:
		decode = func(b []byte) float64 { return float64(int32(binary.LittleEndian.Uint32(b))) / (1 << 31) }
	case format == 3 && bits == 32:
		decode = func(b []byte) float64 { return float64(math.Float32frombits(binary.LittleEndian.Uint32(b))) }
	default:
		return nil, fmt.Errorf("talkie: WAV format %d with %d bits isn't supported; use 16-bit PCM", format, bits)
	}
	width := int(bits / 8)
	frame := width * int(channels)
	samples := make([]float32, len(pcm)/frame)
	for i := range samples {
		var sum float64
		for c := range int(channels) {
			sum += decode(pcm[i*frame+c*width:])
		}
		samples[i] = float32(sum / float64(channels))
	}
	return &Sound{Samples: samples, Rate: int(rate)}, nil
}
