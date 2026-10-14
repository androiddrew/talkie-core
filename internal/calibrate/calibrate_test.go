package calibrate

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

// room plays played into a microphone delayed by delay samples, at gain
// gainDB, with a little reverb and noise at -60 dBFS.
func room(played []float32, delay int, gainDB float64, seed uint64) []float32 {
	rng := rand.New(rand.NewPCG(seed, 1))
	g := math.Pow(10, gainDB/20)
	heard := make([]float32, len(played))
	for i := range heard {
		var v float64
		if j := i - delay; j >= 0 {
			v += g * float64(played[j])
		}
		if j := i - delay - 700; j >= 0 { // a reflection 15 ms later at 48 kHz
			v += 0.3 * g * float64(played[j])
		}
		heard[i] = float32(v + rng.NormFloat64()*0.001)
	}
	return heard
}

func TestMeasure(t *testing.T) {
	const rate = 48000
	chirp := Chirp(rate, 300*time.Millisecond, 0.1)
	start := rate / 2
	played := make([]float32, start+len(chirp)+rate)
	copy(played[start:], chirp)
	for _, c := range []struct {
		delay  time.Duration
		gainDB float64
	}{{80 * time.Millisecond, -6}, {115 * time.Millisecond, 8}, {174 * time.Millisecond, 8}, {220 * time.Millisecond, -20}} {
		delay := int(c.delay.Seconds() * rate)
		e := Measure(played, room(played, delay, c.gainDB, 1), start, len(chirp), rate, 500*time.Millisecond)
		if !e.Heard {
			t.Fatalf("%v at %+.0f dB: not heard (correlation %.2f)", c.delay, c.gainDB, e.Correlation)
		}
		// Within one 16 kHz sample, and 1 dB.
		if d := e.Delay - c.delay; d < -63*time.Microsecond || d > 63*time.Microsecond {
			t.Errorf("delay %v, want %v", e.Delay, c.delay)
		}
		if math.Abs(e.GainDB-c.gainDB) > 1 {
			t.Errorf("%v: gain %.2f dB, want %.0f", c.delay, e.GainDB, c.gainDB)
		}
	}
}

// TestMeasureNothingHeard: a headset's microphone hears only noise.
func TestMeasureNothingHeard(t *testing.T) {
	const rate = 48000
	chirp := Chirp(rate, 300*time.Millisecond, 0.1)
	played := make([]float32, rate*2)
	copy(played[rate/2:], chirp)
	rng := rand.New(rand.NewPCG(2, 2))
	noise := make([]float32, len(played))
	for i := range noise {
		noise[i] = float32(rng.NormFloat64() * 0.01)
	}
	if e := Measure(played, noise, rate/2, len(chirp), rate, 500*time.Millisecond); e.Heard {
		t.Errorf("heard a chirp in noise: %+v", e)
	}
}

func TestLevels(t *testing.T) {
	sine := Chirp(16000, time.Second, 0.5)
	if p := Peak(sine); math.Abs(p-0.5) > 0.01 {
		t.Errorf("peak %.3f", p)
	}
	full := make([]float32, 1000)
	for i := range full {
		full[i] = 1
	}
	if l := LevelDBFS(full); math.Abs(l) > 1e-9 {
		t.Errorf("full scale %.3f dBFS", l)
	}
	if l := LevelDBFS(make([]float32, 10)); l != -120 {
		t.Errorf("silence %.1f dBFS", l)
	}
}
