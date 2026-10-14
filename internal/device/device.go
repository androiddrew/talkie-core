// Package device opens a full-duplex audio device with miniaudio (malgo):
// one callback per period receives the microphone samples and fills the
// speaker samples, so what was played and what was heard stay aligned for
// echo cancellation. miniaudio picks the platform backend at run time
// (PipeWire or PulseAudio, ALSA, Core Audio, WASAPI) and converts the
// device's own format, channels and rate to mono float32 at Config.Rate.
package device

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unsafe"

	"github.com/gen2brain/malgo"
)

// Config selects the devices and the stream format.
type Config struct {
	Rate   int           // sample rate of both directions, in Hz
	Period time.Duration // audio per callback
	// Input and Output select devices by a case-insensitive substring of
	// their names; empty selects the system default.
	Input, Output string
}

// Callback handles one period: in is the microphone, out is to be filled
// with the speaker's next samples. It runs on the audio thread: it must not
// block for long, and it must not keep in or out after returning.
type Callback func(in, out []float32)

// Duplex is an open full-duplex device.
type Duplex struct {
	ctx  *malgo.AllocatedContext
	dev  *malgo.Device
	rate int
}

// Open initializes the device; Start begins calling cb.
func Open(c Config, cb Callback) (_ *Duplex, err error) {
	if c.Rate <= 0 || c.Period <= 0 || cb == nil {
		return nil, errors.New("device: rate, period and callback are required")
	}
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("device: init audio: %w", err)
	}
	d := &Duplex{ctx: ctx, rate: c.Rate}
	defer func() {
		if err != nil {
			_ = d.Close()
		}
	}()
	cfg := malgo.DefaultDeviceConfig(malgo.Duplex)
	cfg.SampleRate = uint32(c.Rate)
	cfg.PeriodSizeInMilliseconds = uint32(c.Period / time.Millisecond)
	cfg.Capture.Format, cfg.Capture.Channels = malgo.FormatF32, 1
	cfg.Playback.Format, cfg.Playback.Channels = malgo.FormatF32, 1
	if cfg.Capture.DeviceID, err = find(ctx, malgo.Capture, c.Input); err != nil {
		return nil, err
	}
	if cfg.Playback.DeviceID, err = find(ctx, malgo.Playback, c.Output); err != nil {
		return nil, err
	}
	data := func(out, in []byte, frames uint32) {
		cb(floats(in, frames), floats(out, frames))
	}
	if d.dev, err = malgo.InitDevice(ctx.Context, cfg, malgo.DeviceCallbacks{Data: data}); err != nil {
		return nil, fmt.Errorf("device: open: %w", err)
	}
	return d, nil
}

// floats views a callback buffer of mono float32 frames.
func floats(b []byte, frames uint32) []float32 {
	if len(b) == 0 {
		return nil
	}
	return unsafe.Slice((*float32)(unsafe.Pointer(&b[0])), frames)
}

// find returns the ID of the first device of kind whose name contains name,
// or nil for the default.
func find(ctx *malgo.AllocatedContext, kind malgo.DeviceType, name string) (unsafe.Pointer, error) {
	if name == "" {
		return nil, nil
	}
	infos, err := ctx.Devices(kind)
	if err != nil {
		return nil, fmt.Errorf("device: list: %w", err)
	}
	var names []string
	for _, info := range infos {
		if strings.Contains(strings.ToLower(info.Name()), strings.ToLower(name)) {
			return info.ID.Pointer(), nil
		}
		names = append(names, info.Name())
	}
	return nil, fmt.Errorf("device: no device matches %q; have %q", name, names)
}

// Start begins audio.
func (d *Duplex) Start() error { return d.dev.Start() }

// Rate returns the stream's sample rate.
func (d *Duplex) Rate() int { return d.rate }

// Close stops audio and releases the device.
func (d *Duplex) Close() error {
	if d.dev != nil {
		d.dev.Uninit() // stops it first
		d.dev = nil
	}
	if d.ctx != nil {
		err := d.ctx.Uninit()
		d.ctx.Free()
		d.ctx = nil
		return err
	}
	return nil
}

// List returns the names of the capture and playback devices.
func List() (inputs, outputs []string, err error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = ctx.Uninit(); ctx.Free() }()
	for _, k := range []struct {
		kind malgo.DeviceType
		into *[]string
	}{{malgo.Capture, &inputs}, {malgo.Playback, &outputs}} {
		infos, err := ctx.Devices(k.kind)
		if err != nil {
			return nil, nil, err
		}
		for _, info := range infos {
			*k.into = append(*k.into, info.Name())
		}
	}
	return inputs, outputs, nil
}
