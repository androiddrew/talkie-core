package talkie

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Profile is what room training learned about one microphone and speaker.
// Run loads the profile for its devices and tunes itself with it. Train
// creates one; SaveProfile and LoadProfile store it.
type Profile struct {
	Trained time.Time
	// Input and Output are the full names of the microphone and speaker, as
	// the system reports them; Rate is the rate they were trained at.
	Input, Output string
	Rate          int

	// Latency is the speaker-to-microphone round trip.
	Latency LatencyProfile
	// Echo is how loud the speaker is at the microphone.
	Echo EchoProfile
	// NoiseDBFS is the room's background level at the microphone, in dB
	// relative to full scale.
	NoiseDBFS float64
}

// LatencyProfile is the round trip measured on several opens of the
// devices, which can differ by tens of milliseconds between opens.
type LatencyProfile struct {
	Runs             []time.Duration
	Min, Median, Max time.Duration
}

// Assumed is the latency Run uses: the middle of the measured range, which
// keeps barge-in working across it. Timing barge-in for the longest or the
// shortest run fails at the other end of the range.
func (l LatencyProfile) Assumed() time.Duration { return (l.Min + l.Max) / 2 }

// EchoProfile is how loud the speaker's sound is when it reaches the
// microphone.
type EchoProfile struct {
	// Heard is false when the microphone didn't hear the speaker, as with a
	// headset; echo cancellation can then be turned off.
	Heard bool
	// GainDB is the echo's level relative to what the speaker was given,
	// direct path and reverb. At or below -3 dB barge-in works; above 0 dB
	// the echo is louder than the voice that made it, and the user can't be
	// heard over it.
	GainDB float64
	// Clipped reports that the microphone reached full scale during a quiet
	// chirp: its gain is far too high.
	Clipped bool
	// Advice says what to change in the room, or is empty.
	Advice string
}

// maxComfortableEchoDB is the echo level, as training measures it, above
// which it advises turning down. The simulated room in which barge-in works
// at every delay from 80 to 220 ms measures -3.3 dB; one 4 dB louder fails
// at 220 ms.
const maxComfortableEchoDB = -3

// adviseEcho fills in Advice.
func (e *EchoProfile) adviseEcho() {
	var advice []string
	if !e.Heard {
		advice = append(advice, "The microphone did not hear the speaker: with a headset, echo cancellation can be turned off.")
	}
	if e.Clipped {
		advice = append(advice, "The microphone overloaded on a quiet test sound: turn its input gain down.")
	}
	if e.Heard && e.GainDB > maxComfortableEchoDB {
		advice = append(advice, fmt.Sprintf("The speaker reaches the microphone at %+.0f dB of what it plays: turn the speaker or the microphone gain down by about %.0f dB, or move them apart, so talkie can hear you over itself.",
			e.GainDB, e.GainDB-maxComfortableEchoDB))
	}
	e.Advice = strings.Join(advice, " ")
}

// DefaultProfileDir is where Run looks for profiles unless
// Config.RoomProfileDir says otherwise: talkie/rooms in the user's
// configuration directory, such as ~/.config/talkie/rooms.
func DefaultProfileDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "talkie", "rooms"), nil
}

// ProfilePath is the file in dir for a microphone and speaker: a readable
// slug of their names plus a short hash, so similar names don't collide.
func ProfilePath(dir, input, output string) string {
	sum := sha256.Sum256([]byte(input + "\x00" + output))
	return filepath.Join(dir, slug(input)+"__"+slug(output)+"-"+hex.EncodeToString(sum[:4])+".json")
}

func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 40 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// profileVersion is the file format's version.
const profileVersion = 1

// profileFile is the JSON form: durations in milliseconds, for people
// reading the file.
type profileFile struct {
	Version int       `json:"version"`
	Trained time.Time `json:"trained"`
	Devices struct {
		Input  string `json:"input"`
		Output string `json:"output"`
		Rate   int    `json:"rate"`
	} `json:"devices"`
	Latency struct {
		RunsMS   []float64 `json:"runs_ms"`
		MinMS    float64   `json:"min_ms"`
		MedianMS float64   `json:"median_ms"`
		MaxMS    float64   `json:"max_ms"`
	} `json:"latency"`
	Echo struct {
		Heard   bool    `json:"heard"`
		GainDB  float64 `json:"gain_db"`
		Clipped bool    `json:"clipped"`
		Advice  string  `json:"advice,omitempty"`
	} `json:"echo"`
	Noise struct {
		FloorDBFS float64 `json:"floor_dbfs"`
	} `json:"noise"`
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func fromMS(v float64) time.Duration { return time.Duration(v * float64(time.Millisecond)) }

// SaveProfile writes p to path, creating its directory. It replaces any
// file there in one step, so a reader never sees half a profile.
func SaveProfile(path string, p Profile) error {
	var f profileFile
	f.Version, f.Trained = profileVersion, p.Trained
	f.Devices.Input, f.Devices.Output, f.Devices.Rate = p.Input, p.Output, p.Rate
	for _, r := range p.Latency.Runs {
		f.Latency.RunsMS = append(f.Latency.RunsMS, ms(r))
	}
	f.Latency.MinMS, f.Latency.MedianMS, f.Latency.MaxMS = ms(p.Latency.Min), ms(p.Latency.Median), ms(p.Latency.Max)
	f.Echo.Heard, f.Echo.GainDB, f.Echo.Clipped, f.Echo.Advice = p.Echo.Heard, p.Echo.GainDB, p.Echo.Clipped, p.Echo.Advice
	f.Noise.FloorDBFS = p.NoiseDBFS
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".profile-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ErrNoProfile is returned by LoadProfile when there is no profile file.
var ErrNoProfile = errors.New("talkie: no room profile")

// LoadProfile reads the profile at path.
func LoadProfile(path string) (Profile, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Profile{}, fmt.Errorf("%w: %s", ErrNoProfile, path)
	}
	if err != nil {
		return Profile{}, err
	}
	var f profileFile
	if err := json.Unmarshal(data, &f); err != nil {
		return Profile{}, fmt.Errorf("talkie: room profile %s: %w", path, err)
	}
	if f.Version != profileVersion {
		return Profile{}, fmt.Errorf("talkie: room profile %s is version %d, want %d: train again", path, f.Version, profileVersion)
	}
	p := Profile{
		Trained: f.Trained, Input: f.Devices.Input, Output: f.Devices.Output, Rate: f.Devices.Rate,
		Echo:      EchoProfile{Heard: f.Echo.Heard, GainDB: f.Echo.GainDB, Clipped: f.Echo.Clipped, Advice: f.Echo.Advice},
		NoiseDBFS: f.Noise.FloorDBFS,
	}
	for _, r := range f.Latency.RunsMS {
		p.Latency.Runs = append(p.Latency.Runs, fromMS(r))
	}
	p.Latency.Min, p.Latency.Median, p.Latency.Max = fromMS(f.Latency.MinMS), fromMS(f.Latency.MedianMS), fromMS(f.Latency.MaxMS)
	return p, nil
}
