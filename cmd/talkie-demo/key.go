package main

import (
	"encoding/binary"
	"io"
)

// Linux input event constants, from linux/input-event-codes.h.
const (
	evKey    = 0x01
	keyPause = 119
	keyDown  = 1
)

// onPause reads Linux input events from a keyboard's event device and calls
// f each time the Pause key goes down, until the device fails.
func onPause(r io.Reader, f func()) error {
	// struct input_event on 64-bit Linux: a timeval (16 bytes), then type
	// and code (uint16) and value (int32).
	var ev [24]byte
	for {
		if _, err := io.ReadFull(r, ev[:]); err != nil {
			return err
		}
		typ := binary.NativeEndian.Uint16(ev[16:])
		code := binary.NativeEndian.Uint16(ev[18:])
		value := int32(binary.NativeEndian.Uint32(ev[20:]))
		if typ == evKey && code == keyPause && value == keyDown {
			f()
		}
	}
}
