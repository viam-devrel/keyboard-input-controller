//go:build linux

package keyboard

import (
	"syscall"
	"testing"
	"time"

	"github.com/viamrobotics/evdev"
)

func TestEvdevCodesCoverBothLayouts(t *testing.T) {
	have := map[string]bool{}
	for _, code := range evdevCodes {
		have[code] = true
	}
	for name, keys := range layouts {
		for code := range keys {
			if !have[code] {
				t.Errorf("layout %q key %q has no evdev translation", name, code)
			}
		}
	}
}

// evdevCodes must agree with the evdev library's own generated names for the
// same constants, so a typo'd constant (evdev.KeyX: "KeyW") fails here.
func TestEvdevCodesMatchKeyNames(t *testing.T) {
	rewrite := map[string]string{
		"Space": "Space",
		"Up":    "ArrowUp", "Down": "ArrowDown", "Left": "ArrowLeft", "Right": "ArrowRight",
		"LeftShift": "ShiftLeft", "RightShift": "ShiftRight",
		"LeftCtrl": "ControlLeft", "RightCtrl": "ControlRight",
	}
	for key, code := range evdevCodes {
		name := key.String()
		want, ok := rewrite[name]
		if !ok {
			want = "Key" + name // single-letter keys
		}
		if code != want {
			t.Errorf("evdev %s (%d) maps to %q, want %q", name, key, code, want)
		}
	}
}

func TestKeyEventDecode(t *testing.T) {
	baseTime := syscall.Timeval{Sec: 100, Usec: 500000}
	wantTime := time.Unix(100, 500000*1000)

	tests := []struct {
		name      string
		ev        *evdev.EventEnvelope
		wantOK    bool
		wantCode  string
		wantPress bool
	}{
		{
			name: "press",
			ev: &evdev.EventEnvelope{
				Event: evdev.Event{Time: baseTime, Type: evdev.EventKey, Code: uint16(evdev.KeyW), Value: 1},
				Type:  evdev.KeyW,
			},
			wantOK:    true,
			wantCode:  "KeyW",
			wantPress: true,
		},
		{
			name: "release",
			ev: &evdev.EventEnvelope{
				Event: evdev.Event{Time: baseTime, Type: evdev.EventKey, Code: uint16(evdev.KeyW), Value: 0},
				Type:  evdev.KeyW,
			},
			wantOK:    true,
			wantCode:  "KeyW",
			wantPress: false,
		},
		{
			name: "autorepeat ignored",
			ev: &evdev.EventEnvelope{
				Event: evdev.Event{Time: baseTime, Type: evdev.EventKey, Code: uint16(evdev.KeyW), Value: 2},
				Type:  evdev.KeyW,
			},
			wantOK: false,
		},
		{
			name: "unmapped key ignored",
			ev: &evdev.EventEnvelope{
				Event: evdev.Event{Time: baseTime, Type: evdev.EventKey, Code: uint16(evdev.KeyF1), Value: 1},
				Type:  evdev.KeyF1,
			},
			wantOK: false,
		},
		{
			name: "non-key event ignored",
			ev: &evdev.EventEnvelope{
				Event: evdev.Event{Time: baseTime, Type: evdev.EventRelative, Code: 0, Value: 1},
				Type:  evdev.RelativeType(0),
			},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, pressed, at, ok := keyEvent(tt.ev)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if code != tt.wantCode {
				t.Errorf("code = %q, want %q", code, tt.wantCode)
			}
			if pressed != tt.wantPress {
				t.Errorf("pressed = %v, want %v", pressed, tt.wantPress)
			}
			if !at.Equal(wantTime) {
				t.Errorf("at = %v, want %v", at, wantTime)
			}
		})
	}
}
