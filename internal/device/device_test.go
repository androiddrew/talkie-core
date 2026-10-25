package device

import (
	"strings"
	"testing"
)

func TestChoose(t *testing.T) {
	// The capture devices PulseAudio lists on a Jetson with a USB headset.
	sources := []string{
		"Monitor of Plantronics P610 Analog Stereo",
		"Plantronics P610 Analog Stereo",
		"Monitor of Built-in Audio Analog Stereo",
		"Built-in Audio Analog Stereo",
	}
	for _, c := range []struct {
		names   []string
		name    string
		capture bool
		want    int
	}{
		{sources, "P610", true, 1},                           // not its monitor
		{sources, "plantronics p610 analog stereo", true, 1}, // exact, ignoring case
		{sources, "monitor of plantronics", true, 0},         // a monitor, asked for
		{sources, "Monitor of Built-in Audio Analog Stereo", true, 2},
		{sources, "built-in", true, 3},
		{sources, "P610", false, 0}, // playback names aren't filtered
		{[]string{"C922 Pro Stream Webcam Analog Stereo"}, "c922", true, 0},
		{[]string{"USB Audio Long Name", "USB Audio"}, "usb audio", false, 1}, // exact beats first substring
	} {
		got, err := choose(c.names, c.name, c.capture)
		if err != nil || got != c.want {
			t.Errorf("choose(%q, capture %v) = %d, %v; want %d", c.name, c.capture, got, err, c.want)
		}
	}
	if _, err := choose(sources, "Jabra", true); err == nil || !strings.Contains(err.Error(), `no device matches "Jabra"`) {
		t.Errorf("no match: %v", err)
	}
	// Only a monitor matches, and the name doesn't ask for one.
	if _, err := choose([]string{"Monitor of Speakers"}, "speakers", true); err == nil {
		t.Error("a monitor was chosen without being asked for")
	}
}
