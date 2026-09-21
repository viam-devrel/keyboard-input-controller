// Package keyboard implements a Viam input controller driven by keyboard keys,
// from a local evdev device (Linux) or from a browser via TriggerEvent.
//
// A single mutex guards all controller state, but it is deliberately NOT held
// while callbacks run. Events are queued under the lock and dispatched after
// releasing it, because a subscriber whose callback blocks would otherwise
// stall every later RegisterControlCallback behind the same mutex, which
// presents as a consumer hanging on registration and never receiving events.
//
// Each consumer registers with its own context (the RDK input server passes
// its stream context), so independent consumers coexist rather than
// overwriting one another, and a consumer whose context is done is dropped
// on the next dispatch. This matters because the RDK never deregisters a
// callback when its stream dies.
package keyboard

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.viam.com/rdk/components/input"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"
	"go.viam.com/utils"
)

// Input is the model triplet for this component.
var Input = resource.NewModel("devrel", "keyboard", "input")

func init() {
	resource.RegisterComponent(input.API, Input,
		resource.Registration[input.Controller, *Config]{
			Constructor: newKeyboardInput,
		},
	)
}

// Config is the component's JSON attributes. See docs/SPEC.md "Configuration".
type Config struct {
	Layout        string `json:"layout,omitempty"`
	DevFile       string `json:"dev_file,omitempty"`
	Grab          bool   `json:"grab,omitempty"`
	HoldTimeoutMs *int   `json:"hold_timeout_ms,omitempty"` // pointer: 0 disables, nil means default
}

const defaultHoldTimeout = 500 * time.Millisecond

// minHoldTimeoutMs is the smallest positive hold_timeout_ms Validate
// accepts. Anything smaller (e.g. 1) produces a sub-millisecond watchdog
// ticker, a hot loop. 0 is still allowed: it disables the watchdog.
const minHoldTimeoutMs = 50

// Validate checks attributes. No dependencies.
func (cfg *Config) Validate(path string) ([]string, []string, error) {
	if cfg.Layout != "" {
		if _, ok := layouts[cfg.Layout]; !ok {
			return nil, nil, resource.NewConfigValidationError(path,
				fmt.Errorf("layout must be \"wasd\" or \"arrows\", got %q", cfg.Layout))
		}
	}
	if cfg.HoldTimeoutMs != nil && *cfg.HoldTimeoutMs != 0 && *cfg.HoldTimeoutMs < minHoldTimeoutMs {
		return nil, nil, resource.NewConfigValidationError(path,
			fmt.Errorf("hold_timeout_ms must be 0 (disabled) or >= %d", minHoldTimeoutMs))
	}
	return nil, nil, nil
}

// action is what a key means. Controls are derived from which actions are held.
type action int

const (
	actForward action = iota
	actBack
	actLeft
	actRight
	actZDown
	actZUp
	actGripClose
	actGripOpen
	actEStop
)

// layouts map browser KeyboardEvent.code names to actions. evdev codes are
// translated to these same names in keyboard_linux.go.
var layouts = map[string]map[string]action{
	"wasd": {
		"KeyW": actForward, "KeyS": actBack, "KeyA": actLeft, "KeyD": actRight,
		"KeyQ": actZDown, "KeyE": actZUp, "KeyZ": actGripClose, "KeyC": actGripOpen,
		"Space": actEStop,
	},
	"arrows": {
		"ArrowUp": actForward, "ArrowDown": actBack, "ArrowLeft": actLeft, "ArrowRight": actRight,
		"ShiftLeft": actZDown, "ShiftRight": actZUp, "ControlLeft": actGripClose, "ControlRight": actGripOpen,
		"Space": actEStop,
	},
}

// buttonControls are the actions that map 1:1 to a button control, in a
// fixed emission order. Hat axes are synthesized from the forward/back and
// left/right pairs. The order matters: recomputeLocked emits in this order
// so that on an EStop clear-all, ButtonEStop's press is always emitted last,
// after every other button's release and the hat axes have gone out.
var buttonControls = []struct {
	act  action
	ctrl input.Control
}{
	{actZDown, input.ButtonLT},
	{actZUp, input.ButtonRT},
	{actGripClose, input.ButtonWest},
	{actGripOpen, input.ButtonEast},
	{actEStop, input.ButtonEStop},
}

// controls is the fixed list returned by Controls(), regardless of layout.
var controls = []input.Control{
	input.AbsoluteHat0X, input.AbsoluteHat0Y,
	input.ButtonLT, input.ButtonRT, input.ButtonWest, input.ButtonEast, input.ButtonEStop,
}

// subscriber is one registered callback. Each streaming consumer registers
// with its own context (the RDK input server passes its stream context), so
// independent consumers coexist instead of overwriting each other, and a
// consumer that goes away is recognised by its context being done.
type subscriber struct {
	ctx context.Context
	fn  input.ControlFunction
}

type source int

const (
	srcEvdev source = iota
	srcWeb
	numSources
)

type keyboard struct {
	resource.Named
	resource.AlwaysRebuild

	logger      logging.Logger
	keys        map[string]action // active layout
	holdTimeout time.Duration

	// mu guards everything below. It is NOT held while callbacks run:
	// emitLocked queues events and flush dispatches them after releasing the
	// lock, so one slow subscriber cannot block registration or other emits.
	mu         sync.Mutex
	closed     bool
	held       [numSources]map[action]time.Time // last press or keepalive, server clock
	lastEvents map[input.Control]input.Event
	pending    []input.Event // emitted under mu, dispatched by flush
	callbacks  map[input.Control]map[input.EventType][]subscriber

	workers *utils.StoppableWorkers
}

func newKeyboardInput(
	ctx context.Context, _ resource.Dependencies, rawConf resource.Config, logger logging.Logger,
) (input.Controller, error) {
	conf, err := resource.NativeConfig[*Config](rawConf)
	if err != nil {
		return nil, err
	}
	return NewInput(ctx, rawConf.ResourceName(), conf, logger)
}

// NewInput builds the controller from a native config.
func NewInput(ctx context.Context, name resource.Name, conf *Config, logger logging.Logger) (input.Controller, error) {
	layout := conf.Layout
	if layout == "" {
		layout = "wasd"
	}
	keys, ok := layouts[layout]
	if !ok {
		return nil, fmt.Errorf("unknown layout %q", layout)
	}
	timeout := defaultHoldTimeout
	if conf.HoldTimeoutMs != nil {
		timeout = time.Duration(*conf.HoldTimeoutMs) * time.Millisecond
	}

	k := &keyboard{
		Named:       name.AsNamed(),
		logger:      logger,
		keys:        keys,
		holdTimeout: timeout,
		lastEvents:  map[input.Control]input.Event{},
		callbacks:   map[input.Control]map[input.EventType][]subscriber{},
	}
	for s := range k.held {
		k.held[s] = map[action]time.Time{}
	}

	k.deviceConnected(ctx)

	var workers []func(context.Context)
	if timeout > 0 {
		workers = append(workers, k.watchdog)
	}
	if conf.DevFile != "" {
		w, err := k.deviceWorker(conf.DevFile, conf.Grab)
		if err != nil {
			return nil, err
		}
		workers = append(workers, w)
	}
	k.workers = utils.NewBackgroundStoppableWorkers(workers...)
	return k, nil
}

// keyLocked applies a press or release of act from src and recomputes.
// A repeat press from the same source only refreshes the timestamp.
// eventTime is used only as the emitted Event.Time passed to
// recomputeLocked; it is deliberately NOT stored in k.held. Held-key
// timestamps always use time.Now(), the server's receipt clock, because the
// watchdog must never depend on a caller-supplied time (see docs/SPEC.md,
// "Held-key timestamps use the server clock only"). Do not "simplify" this
// by storing eventTime instead.
//
// Caller holds k.mu.
func (k *keyboard) keyLocked(src source, act action, pressed bool, eventTime time.Time) {
	if pressed {
		if _, already := k.held[src][act]; already {
			k.held[src][act] = time.Now()
			return
		}
		if act == actEStop {
			for s := range k.held {
				clear(k.held[s])
			}
		}
		k.held[src][act] = time.Now()
	} else {
		delete(k.held[src], act)
	}
	k.recomputeLocked(eventTime)
}

// isHeldLocked reports whether act is held by any source. Caller holds k.mu.
func (k *keyboard) isHeldLocked(act action) bool {
	for s := range k.held {
		if _, ok := k.held[s][act]; ok {
			return true
		}
	}
	return false
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// recomputeLocked derives every control from the held sets and emits only
// the ones whose value differs from lastEvents, in a fixed order (hat axes,
// then buttonControls in its declared order) so that dispatch is
// deterministic. In particular, on an EStop clear-all, ButtonEStop is always
// the last event of the recompute, after both axes are zeroed and every
// other button's release goes out. Caller holds k.mu.
func (k *keyboard) recomputeLocked(at time.Time) {
	k.setLocked(input.AbsoluteHat0Y, boolToFloat(k.isHeldLocked(actBack))-boolToFloat(k.isHeldLocked(actForward)), at)
	k.setLocked(input.AbsoluteHat0X, boolToFloat(k.isHeldLocked(actRight))-boolToFloat(k.isHeldLocked(actLeft)), at)
	for _, bc := range buttonControls {
		k.setLocked(bc.ctrl, boolToFloat(k.isHeldLocked(bc.act)), at)
	}
}

// setLocked records val for ctrl and emits an event only if it differs from
// the last value recorded for ctrl. Caller holds k.mu.
func (k *keyboard) setLocked(ctrl input.Control, val float64, at time.Time) {
	if last, ok := k.lastEvents[ctrl]; ok && last.Value == val {
		return
	}
	ev := input.Event{Time: at, Control: ctrl, Value: val}
	switch ctrl {
	case input.AbsoluteHat0X, input.AbsoluteHat0Y:
		ev.Event = input.PositionChangeAbs
	default:
		ev.Event = input.ButtonRelease
		if val == 1 {
			ev.Event = input.ButtonPress
		}
	}
	k.emitLocked(ev)
}

// emitLocked records the event and queues it for dispatch. Caller holds k.mu.
//
// Dispatch deliberately does NOT happen here. Callbacks used to run while
// k.mu was held, which meant one slow or wedged subscriber blocked every
// later RegisterControlCallback behind the same mutex: a consumer would hang
// registering and never receive anything. The RDK's input server installs a
// callback that sends on a 1024-slot channel and escapes only via its
// context, so a subscriber whose stream died is exactly that wedge. Events
// are queued here and dispatched by flush once the lock is released.
func (k *keyboard) emitLocked(ev input.Event) {
	k.lastEvents[ev.Control] = ev
	k.pending = append(k.pending, ev)
}

// flush dispatches queued events without holding k.mu. Subscribers whose
// context is done are dropped rather than called, so a departed consumer
// neither receives events nor holds a slot: the RDK never deregisters a
// callback when its stream dies.
func (k *keyboard) flush(ctx context.Context) {
	k.mu.Lock()
	pending := k.pending
	k.pending = nil
	k.mu.Unlock()

	for _, ev := range pending {
		k.mu.Lock()
		subs := append([]subscriber(nil), k.callbacks[ev.Control][ev.Event]...)
		subs = append(subs, k.callbacks[ev.Control][input.AllEvents]...)
		k.mu.Unlock()

		fired := 0
		for _, sub := range subs {
			if sub.ctx != nil && sub.ctx.Err() != nil {
				continue // consumer is gone
			}
			fired++
			// Bound the call so a live-but-backed-up subscriber costs one
			// delayed event rather than stalling this goroutine.
			cctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			sub.fn(cctx, ev)
			cancel()
		}
		k.logger.Debugw("input event emitted",
			"control", ev.Control, "event", ev.Event, "value", ev.Value,
			"subscribers", len(subs), "listeners_fired", fired)
	}
	k.pruneDead()
}

// pruneDead drops subscribers whose context is done.
func (k *keyboard) pruneDead() {
	k.mu.Lock()
	defer k.mu.Unlock()
	for ctrl, byEvent := range k.callbacks {
		for et, subs := range byEvent {
			live := subs[:0]
			for _, sub := range subs {
				if sub.ctx == nil || sub.ctx.Err() == nil {
					live = append(live, sub)
				}
			}
			if len(live) == 0 {
				delete(byEvent, et)
				continue
			}
			byEvent[et] = live
		}
		if len(byEvent) == 0 {
			delete(k.callbacks, ctrl)
		}
	}
}

// sweepLocked emits typ (Connect or Disconnect) for every control, which
// zeroes the comparison baseline. Callers follow it with recomputeLocked.
func (k *keyboard) sweepLocked(typ input.EventType) {
	now := time.Now()
	for _, c := range controls {
		k.emitLocked(input.Event{Time: now, Event: typ, Control: c})
	}
}

// deviceConnected is called by the evdev worker after opening the device.
// No closed guard: Close stops the evdev worker (joining it) before its own
// final clear runs, so this cannot race an emit-after-Close.
func (k *keyboard) deviceConnected(ctx context.Context) {
	k.mu.Lock()
	k.sweepLocked(input.Connect)
	k.recomputeLocked(time.Now())
	k.mu.Unlock()
	k.flush(ctx)
}

// deviceLost is called by the evdev worker when the device goes away. No
// closed guard: Close stops the evdev worker (joining it) before its own
// final clear runs, so this cannot race an emit-after-Close.
func (k *keyboard) deviceLost(ctx context.Context) {
	k.mu.Lock()
	clear(k.held[srcEvdev])
	k.recomputeLocked(time.Now())
	k.sweepLocked(input.Disconnect)
	k.recomputeLocked(time.Now())
	k.mu.Unlock()
	k.flush(ctx)
}

// evdevKey is the evdev worker's entry point. code is a browser
// KeyboardEvent.code name; unmapped codes are ignored. No closed guard:
// Close stops the evdev worker (joining it) before its own final clear
// runs, so this cannot race an emit-after-Close.
func (k *keyboard) evdevKey(ctx context.Context, code string, pressed bool, eventTime time.Time) {
	k.mu.Lock()
	if act, ok := k.keys[code]; ok {
		k.keyLocked(srcEvdev, act, pressed, eventTime)
	}
	k.mu.Unlock()
	k.flush(ctx)
}

// Controls returns the fixed set this controller can emit.
func (k *keyboard) Controls(context.Context, map[string]interface{}) ([]input.Control, error) {
	return append([]input.Control(nil), controls...), nil
}

// Events returns the last event per control.
func (k *keyboard) Events(context.Context, map[string]interface{}) (map[input.Control]input.Event, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make(map[input.Control]input.Event, len(k.lastEvents))
	for c, e := range k.lastEvents {
		out[c] = e
	}
	return out, nil
}

// RegisterControlCallback subscribes f to control. ButtonChange expands to
// ButtonPress + ButtonRelease, as in webgamepad.
//
// Unlike webgamepad, several consumers may subscribe to the same control and
// event type: entries are keyed by ctx, so registering again with the same
// ctx replaces that consumer's entry while leaving others alone, and a nil f
// removes only that consumer. f runs on the dispatching goroutine with no
// controller lock held, so it may call back into this controller, but it
// should still return promptly: dispatch is sequential and a slow f delays
// the events behind it.
func (k *keyboard) RegisterControlCallback(
	ctx context.Context, control input.Control, triggers []input.EventType,
	f input.ControlFunction, _ map[string]interface{},
) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.callbacks[control] == nil {
		k.callbacks[control] = map[input.EventType][]subscriber{}
	}
	for _, tr := range triggers {
		if tr == input.ButtonChange {
			k.setSubscriberLocked(control, input.ButtonPress, ctx, f)
			k.setSubscriberLocked(control, input.ButtonRelease, ctx, f)
			continue
		}
		k.setSubscriberLocked(control, tr, ctx, f)
	}
	k.logger.Infow("input callback registered",
		"control", control, "triggers", triggers, "removing", f == nil,
		"subscribers", len(k.callbacks[control][triggers[0]]))
	return nil
}

// setSubscriberLocked adds or replaces this subscriber's entry for one
// control and event type, keyed by its registering context so consumers do
// not overwrite each other. A nil f removes only this subscriber, which is
// how the RDK input server cancels a subscription. Caller holds k.mu.
func (k *keyboard) setSubscriberLocked(
	control input.Control, et input.EventType, ctx context.Context, f input.ControlFunction,
) {
	subs := k.callbacks[control][et]
	for i, sub := range subs {
		if sub.ctx == ctx {
			if f == nil {
				k.callbacks[control][et] = append(subs[:i], subs[i+1:]...)
				return
			}
			subs[i].fn = f
			return
		}
	}
	if f != nil {
		k.callbacks[control][et] = append(subs, subscriber{ctx: ctx, fn: f})
	}
}

// TriggerEvent accepts raw browser key codes only. Press adds to the web
// held set, Release removes, Hold refreshes an existing hold (keepalive).
// Timestamps use the server clock; inbound Time only feeds Event.Time.
func (k *keyboard) TriggerEvent(ctx context.Context, ev input.Event, _ map[string]interface{}) error {
	err := k.triggerEventLocking(ev)
	k.flush(ctx)
	return err
}

func (k *keyboard) triggerEventLocking(ev input.Event) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return errors.New("keyboard input controller is closed")
	}
	act, ok := k.keys[string(ev.Control)]
	if !ok {
		return fmt.Errorf("unknown key %q for this layout", ev.Control)
	}
	at := ev.Time
	if at.Unix() <= 0 {
		at = time.Now()
	}
	switch ev.Event {
	case input.ButtonPress:
		k.keyLocked(srcWeb, act, true, at)
	case input.ButtonRelease:
		k.keyLocked(srcWeb, act, false, at)
	case input.ButtonHold:
		if _, held := k.held[srcWeb][act]; held {
			k.held[srcWeb][act] = time.Now()
		}
	default:
		return fmt.Errorf("unsupported event type %q; want ButtonPress, ButtonRelease, or ButtonHold", ev.Event)
	}
	return nil
}

// DoCommand is not implemented.
func (k *keyboard) DoCommand(context.Context, map[string]interface{}) (map[string]interface{}, error) {
	return nil, resource.ErrDoUnimplemented
}

// watchdog expires web-held keys that have not been refreshed within holdTimeout.
func (k *keyboard) watchdog(ctx context.Context) {
	t := time.NewTicker(k.holdTimeout / 2)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			k.mu.Lock()
			k.expireWebLocked(now)
			k.mu.Unlock()
			k.flush(ctx)
		}
	}
}

// expireWebLocked is the watchdog body, separated so tests can drive it with
// a chosen clock. Caller holds k.mu.
func (k *keyboard) expireWebLocked(now time.Time) {
	changed := false
	for act, ts := range k.held[srcWeb] {
		if now.Sub(ts) > k.holdTimeout {
			delete(k.held[srcWeb], act)
			changed = true
		}
	}
	if changed {
		k.recomputeLocked(now)
	}
}

// Close marks the controller closed, stops workers (without holding the
// mutex, see spec), then releases every held key so consumers see zeros.
func (k *keyboard) Close(ctx context.Context) error {
	k.mu.Lock()
	if k.closed {
		k.mu.Unlock()
		return nil
	}
	k.closed = true
	k.mu.Unlock()

	k.workers.Stop()

	k.mu.Lock()
	for s := range k.held {
		clear(k.held[s])
	}
	k.recomputeLocked(time.Now())
	k.mu.Unlock()
	k.flush(ctx)
	return nil
}
