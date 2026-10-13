package wake

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	phonmatchnet "github.com/androiddrew/go-phonmatchnet"
)

// The listener defaults talkie-core will use, from phonmatchnet-run listen:
// a 1.5 s window every 250 ms, one accepted window to fire, then a window's
// length of refractory time.
var defaults = Config{Window: 24000, Hop: 4000, Confirm: 1, Refractory: 24000}

func newEngine(t *testing.T) *phonmatchnet.Engine {
	t.Helper()
	library := os.Getenv("TALKIE_TEST_ORT")
	if library == "" {
		t.Skip("set TALKIE_TEST_ORT to an ONNX Runtime library for native tests")
	}
	engine, err := phonmatchnet.New(phonmatchnet.Config{ORTLibrary: library})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Error(err)
		}
	})
	return engine
}

// span is where a clip sits in a composed stream, in samples.
type span struct {
	name       string
	start, end int64
}

// compose joins clips from testdata with a second of silence before, between
// and after them, as a microphone stream would carry them.
func compose(t *testing.T, names ...string) ([]float32, []span) {
	t.Helper()
	silence := make([]float32, phonmatchnet.SampleRate)
	stream := append([]float32(nil), silence...)
	var spans []span
	for _, name := range names {
		clip := readWAV(t, name)
		spans = append(spans, span{name, int64(len(stream)), int64(len(stream) + len(clip))})
		stream = append(append(stream, clip...), silence...)
	}
	return stream, spans
}

// listen streams audio through a listener in 20 ms frames and returns the
// detections' window ends.
func listen(t *testing.T, engine *phonmatchnet.Engine, keyword phonmatchnet.Keyword, stream []float32) []int64 {
	t.Helper()
	l, err := New(defaults, PhonMatchNet(engine, keyword, 0))
	if err != nil {
		t.Fatal(err)
	}
	var detections []int64
	const frame = 320
	for start := 0; start < len(stream); start += frame {
		err := l.Write(context.Background(), stream[start:min(start+frame, len(stream))], func(e Event[phonmatchnet.Result]) error {
			if e.Detected {
				t.Logf("detected at %.2fs, score %.3f", float64(e.Window.End)/phonmatchnet.SampleRate, e.Result.Score)
				detections = append(detections, e.Window.End)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return detections
}

// TestWakeWord streams "hey computer" followed by an unrelated command: the
// wake word fires exactly once, while its window covers it, and the command
// does not fire.
func TestWakeWord(t *testing.T) {
	engine := newEngine(t)
	keyword, err := engine.Keyword(context.Background(), "hey computer")
	if err != nil {
		t.Fatal(err)
	}
	stream, spans := compose(t, "af_heart-hey-computer.wav", "af_heart-turn-on-the-lights.wav")
	detections := listen(t, engine, keyword, stream)
	if len(detections) != 1 {
		t.Fatalf("detections at %v, want one for %v", detections, spans[0])
	}
	// The detecting window ends after the clip starts and begins before it ends.
	if end := detections[0]; end <= spans[0].start || end-int64(defaults.Window) >= spans[0].end {
		t.Errorf("detection window ending %d does not overlap %v", end, spans[0])
	}
}

// TestConfusableWakeWord records how the default operating point treats
// "hey commuter", which PhonMatchNet's README warns can score up to 0.99.
// It documents the behavior rather than asserting it.
func TestConfusableWakeWord(t *testing.T) {
	engine := newEngine(t)
	keyword, err := engine.Keyword(context.Background(), "hey computer")
	if err != nil {
		t.Fatal(err)
	}
	stream, _ := compose(t, "af_heart-hey-commuter.wav")
	t.Logf("\"hey commuter\" for keyword \"hey computer\": %d detections", len(listen(t, engine, keyword, stream)))
}

func readWAV(t *testing.T, name string) []float32 {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	samples, err := decodeWAV(data)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return samples
}

// decodeWAV decodes mono 16-bit PCM at 16 kHz.
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
	seenFormat := false
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
			seenFormat = true
		case "data":
			if !seenFormat || format.Tag != 1 || format.Channels != 1 || format.Rate != phonmatchnet.SampleRate || format.Bits != 16 {
				return nil, errors.New("expected mono 16-bit PCM at 16 kHz")
			}
			samples := make([]float32, size/2)
			for i := range samples {
				samples[i] = float32(int16(binary.LittleEndian.Uint16(body[2*i:]))) / 32768
			}
			return samples, nil
		}
		chunk = chunk[8+size+size%2:]
	}
	return nil, errors.New("no data chunk")
}
