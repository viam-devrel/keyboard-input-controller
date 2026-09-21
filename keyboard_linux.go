//go:build linux

package keyboard

import (
	"context"
	"time"

	"github.com/viamrobotics/evdev"
	"go.viam.com/utils"
)

// reconnectDelay is the backoff used both when OpenFile fails and after a
// device is lost, so the worker cannot spin hot when a bad dev_file opens
// but never yields usable reads.
const reconnectDelay = 250 * time.Millisecond

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
					k.logger.Errorw("cannot open keyboard device; retrying", "dev_file", devFile, "err", err)
					lastErr = err.Error()
				}
				if !utils.SelectContextOrWait(ctx, reconnectDelay) {
					return
				}
				continue
			}
			lastErr = ""
			k.logger.Infow("keyboard device opened", "dev_file", devFile, "name", dev.Name())
			if grab {
				if err := dev.Lock(); err != nil {
					k.logger.Warnw("cannot grab keyboard device; continuing ungrabbed", "dev_file", devFile, "err", err)
				}
			}
			// This device's own Connect sweep; NewInput already swept once at
			// construction, before any callback could have been registered.
			k.deviceConnected(ctx)
			k.readLoop(ctx, dev)
			if err := dev.Close(); err != nil {
				k.logger.Debugw("closing keyboard device", "err", err)
			}
			if ctx.Err() != nil {
				return // Close() handles releases; no Disconnect on shutdown
			}
			k.logger.Warnw("keyboard device lost; reconnecting", "dev_file", devFile)
			k.deviceLost(ctx)
			// Prevent a hot reconnect loop when the device opens but reads
			// fail immediately (e.g. dev_file names a non-event file).
			if !utils.SelectContextOrWait(ctx, reconnectDelay) {
				return
			}
		}
	}, nil
}

// readLoop consumes Poll until the channel closes (read error or ctx cancel)
// or a SyncDisconnect arrives. Do not rewrite this as a select on ctx.Done():
// Poll's send is on an unbuffered channel with no context guard, so a
// select-based loop would abandon the pending send and strand the producer
// goroutine forever (the RDK gamepad's nil-check loop has this bug; its
// visible symptom is busy-spinning on a closed channel, but that is only the
// symptom). The accepted cost of always draining to completion is that Poll
// only checks ctx between reads, guarded by a ~1s read deadline, so Close
// can stall up to about a second.
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
		code, pressed, at, ok := keyEvent(ev)
		if !ok {
			continue
		}
		k.evdevKey(ctx, code, pressed, at)
	}
}

// keyEvent decodes a key press/release from an evdev event envelope. It is
// pure so it can be table-tested without hardware; readLoop keeps the
// channel plumbing and the SyncDisconnect check.
//
// ev.Type and ev.Event.Type are different fields, despite the similar names:
// EventEnvelope embeds Event and then declares its own Type, shadowing
// Event.Type. ev.Event.Type is the event class (EventKey, EventSync, ...);
// ev.Type is the envelope's already-decoded code for that class (a
// evdev.KeyType when Event.Type is EventKey).
func keyEvent(ev *evdev.EventEnvelope) (code string, pressed bool, at time.Time, ok bool) {
	if ev.Event.Type != evdev.EventKey || ev.Event.Value == 2 { // 2 = autorepeat, ignore
		return "", false, time.Time{}, false
	}
	code, ok = evdevCodes[ev.Type.(evdev.KeyType)]
	if !ok {
		return "", false, time.Time{}, false
	}
	// Convert to int64 before multiplying: Usec is int32 on 32-bit targets,
	// and multiplying first would risk overflow if this were ever near
	// int32's range. Do not "simplify" to int64(ev.Event.Time.Usec * 1000).
	at = time.Unix(int64(ev.Event.Time.Sec), int64(ev.Event.Time.Usec)*1000)
	return code, ev.Event.Value == 1, at, true
}
