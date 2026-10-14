package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestOnPause(t *testing.T) {
	var events bytes.Buffer
	for _, e := range []struct {
		typ, code uint16
		value     int32
	}{
		{evKey, keyPause, keyDown},
		{evKey, keyPause, 0}, // up
		{evKey, 28, keyDown}, // Enter
		{0x04, keyPause, keyDown},
		{evKey, keyPause, 2}, // auto-repeat
		{evKey, keyPause, keyDown},
	} {
		var ev [24]byte
		binary.NativeEndian.PutUint16(ev[16:], e.typ)
		binary.NativeEndian.PutUint16(ev[18:], e.code)
		binary.NativeEndian.PutUint32(ev[20:], uint32(e.value))
		events.Write(ev[:])
	}
	presses := 0
	_ = onPause(&events, func() { presses++ })
	if presses != 2 {
		t.Errorf("%d presses, want 2", presses)
	}
}
