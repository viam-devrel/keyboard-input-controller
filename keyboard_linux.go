//go:build linux

package keyboard

import (
	"context"
	"time"

	"github.com/viamrobotics/evdev"
	"go.viam.com/utils"
)

// evdevCodes translates evdev key constants to browser KeyboardEvent.code
// names, the canonical key identifiers used by layouts.
var evdevCodes = map[evdev.KeyType]string{
	evdev.KeyW: "KeyW", evdev.KeyS: "KeyS", evdev.KeyA: "KeyA", evdev.KeyD: "KeyD",
	evdev.KeyQ: "KeyQ", evdev.KeyE: "KeyE", evdev.KeyZ: "KeyZ", evdev.KeyC: "KeyC",
	evdev.KeyUp: "ArrowUp", evdev.KeyDown: "ArrowDown",
	evdev.KeyLeft: "ArrowLeft", evdev.KeyRight: "ArrowRight",
	evdev.KeyLeftShift: "ShiftLeft", evdev.KeyRightShift: "ShiftRight",
	evdev.KeyLeftCtrl: "ControlLeft", evdev.KeyRightCtrl: "ControlRight",
	evdev.KeySpace: "Space",
}

// deviceWorker returns a background worker that keeps devFile open and feeds
// key events into the controller, reconnecting on loss. See spec "Source 1".
func (k *keyboard) deviceWorker(devFile string, grab bool) (func(context.Context), error) {
	return func(ctx context.Context) {
		lastErr := ""
		for ctx.Err() == nil {
			dev, err := evdev.OpenFile(devFile)
			if err != nil {
				if err.Error() != lastErr {
					k.logger.Warnw("cannot open keyboard device; retrying", "dev_file", devFile, "err", err)
					lastErr = err.Error()
				}
				if !utils.SelectContextOrWait(ctx, 250*time.Millisecond) {
					return
				}
				continue
			}
			lastErr = ""
			k.logger.Infow("keyboard device opened", "dev_file", devFile, "name", dev.Name())
			if grab {
				if err := dev.Lock(); err != nil {
					k.logger.Warnw("cannot grab keyboard device; continuing ungrabbed", "err", err)
				}
			}
			k.deviceConnected(ctx)
			k.readLoop(ctx, dev)
			if err := dev.Close(); err != nil {
				k.logger.Debugw("closing keyboard device", "err", err)
			}
			if ctx.Err() != nil {
				return // Close() handles releases; no Disconnect on shutdown
			}
			k.deviceLost(ctx)
		}
	}, nil
}

// readLoop consumes Poll until the channel closes (read error or ctx cancel)
// or a SyncDisconnect arrives. Do not copy the RDK gamepad's nil-check loop;
// it busy-spins on a closed channel.
func (k *keyboard) readLoop(ctx context.Context, dev *evdev.Evdev) {
	ch := dev.Poll(ctx)
	for {
		ev, ok := <-ch
		if !ok {
			return
		}
		if ev.Event.Type == evdev.EventSync && evdev.SyncType(ev.Event.Code) == evdev.SyncDisconnect {
			return
		}
		if ev.Event.Type != evdev.EventKey || ev.Event.Value == 2 { // 2 = autorepeat, ignore
			continue
		}
		code, ok := evdevCodes[ev.Type.(evdev.KeyType)]
		if !ok {
			continue
		}
		at := time.Unix(int64(ev.Event.Time.Sec), int64(ev.Event.Time.Usec)*1000)
		k.evdevKey(ctx, code, ev.Event.Value == 1, at)
	}
}
