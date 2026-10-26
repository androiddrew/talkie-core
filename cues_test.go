package talkie

import (
	"bytes"
	"encoding/binary"
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestEngineCues: the cues reach the engine at the speaker's rate, a nil
// one stays off, and bad ones are refused.
func TestEngineCues(t *testing.T) {
	d := DefaultCues()
	c, err := engineCues(d, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Listening, d.Listening.Samples) || c.ThinkingDelay != 500*time.Millisecond {
		t.Errorf("at the sounds' own rate: %d samples, delay %v", len(c.Listening), c.ThinkingDelay)
	}

	c, err = engineCues(Cues{Thinking: d.Thinking, Idle: d.Idle, ThinkingDelay: time.Second}, 16000)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listening != nil || len(c.Thinking) != 25600 || c.ThinkingDelay != time.Second {
		t.Errorf("at 16 kHz: listening %d, thinking %d samples (want 25600), delay %v", len(c.Listening), len(c.Thinking), c.ThinkingDelay)
	}
	// The idle cue keeps its whole length, plus a little of the filter's tail.
	if n := len(c.Idle); n < len(d.Idle.Samples)/3 || n > len(d.Idle.Samples)/3+160 {
		t.Errorf("idle at 16 kHz: %d samples, from %d at 48 kHz", n, len(d.Idle.Samples))
	}
	// The resampled loop still joins up: the step from its last sample to
	// its first is like any other step.
	loop := c.Thinking
	var worst float64
	for i := 1; i < len(loop); i++ {
		worst = math.Max(worst, math.Abs(float64(loop[i]-loop[i-1])))
	}
	if seam := math.Abs(float64(loop[0] - loop[len(loop)-1])); seam > worst {
		t.Errorf("loop seam %v, largest step inside %v", seam, worst)
	}

	for _, bad := range []struct {
		cues Cues
		want string
	}{
		{Cues{Idle: &Sound{Samples: []float32{0.1}}}, "Cues.Idle: sample rate 0"},
		{Cues{Listening: &Sound{Rate: 48000}}, "Cues.Listening: no samples"},
		{Cues{Thinking: &Sound{Samples: []float32{0, 1.5}, Rate: 48000}}, "sample 1 is 1.5"},
		{Cues{Idle: &Sound{Samples: []float32{float32(math.NaN())}, Rate: 48000}}, "sample 0 is NaN"},
		{Cues{ThinkingDelay: -time.Second}, "negative"},
	} {
		if _, err := engineCues(bad.cues, 48000); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%+v: %v, want %q", bad.cues, err, bad.want)
		}
	}
}

// wav builds a WAV file: format 1 (PCM) or 3 (float), or 0xFFFE
// (extensible) wrapping sub.
func wav(format uint16, sub uint16, channels uint16, rate uint32, bits uint16, pcm []byte) []byte {
	var b bytes.Buffer
	fmtSize := uint32(16)
	if format == 0xFFFE {
		fmtSize = 40
	}
	put := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	put(uint32(4 + 8 + fmtSize + 8 + 8 + uint32(len(pcm))))
	b.WriteString("WAVE")
	b.WriteString("fmt ")
	put(fmtSize)
	put(format)
	put(channels)
	put(rate)
	put(rate * uint32(channels) * uint32(bits/8))
	put(channels * bits / 8)
	put(bits)
	if format == 0xFFFE {
		put(uint16(22))
		put(bits)
		put(uint32(0))
		put(sub)
		b.Write(make([]byte, 14)) // the rest of the subformat GUID
	}
	b.WriteString("LIST") // a chunk to skip
	put(uint32(0))
	b.WriteString("data")
	put(uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}

func TestReadWAV(t *testing.T) {
	le := binary.LittleEndian
	pcm16 := make([]byte, 8) // two stereo frames: (0.5, -0.5), (0.25, 0.75)
	for i, v := range []int16{16384, -16384, 8192, 24576} {
		le.PutUint16(pcm16[2*i:], uint16(v))
	}
	float := make([]byte, 8)
	le.PutUint32(float, math.Float32bits(0.5))
	le.PutUint32(float[4:], math.Float32bits(-0.25))
	pcm24 := []byte{0x00, 0x00, 0x40, 0x00, 0x00, 0xc0} // 0.5, -0.5
	for _, c := range []struct {
		name string
		file []byte
		rate int
		want []float32
	}{
		{"16-bit stereo", wav(1, 0, 2, 22050, 16, pcm16), 22050, []float32{0, 0.5}},
		{"32-bit float", wav(3, 0, 1, 48000, 32, float), 48000, []float32{0.5, -0.25}},
		{"24-bit", wav(1, 0, 1, 16000, 24, pcm24), 16000, []float32{0.5, -0.5}},
		{"8-bit", wav(1, 0, 1, 8000, 8, []byte{128, 192}), 8000, []float32{0, 0.5}},
		{"extensible float", wav(0xFFFE, 3, 1, 44100, 32, float), 44100, []float32{0.5, -0.25}},
	} {
		s, err := ReadWAV(bytes.NewReader(c.file))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if s.Rate != c.rate || !slices.Equal(s.Samples, c.want) {
			t.Errorf("%s: %d Hz %v, want %d Hz %v", c.name, s.Rate, s.Samples, c.rate, c.want)
		}
	}
	for _, c := range []struct {
		name string
		file []byte
		want string
	}{
		{"not a WAV", []byte("hello, world"), "not a WAV file"},
		{"12-bit", wav(1, 0, 1, 8000, 12, []byte{0, 0}), "isn't supported"},
		{"no data", wav(1, 0, 1, 8000, 16, nil)[:36], "no format or data"},
	} {
		if _, err := ReadWAV(bytes.NewReader(c.file)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}
