//go:build linux

package keyboard

import "testing"

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
