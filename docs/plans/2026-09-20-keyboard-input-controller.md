# Keyboard Input Controller Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement `devrel:keyboard:input`, a Viam `input_controller` that maps keyboard keys (local evdev or browser `TriggerEvent`) to gamepad-style controls, per `docs/SPEC.md`.

**Architecture:** One Go package with a held-key state machine guarded by a single mutex. Two sources (evdev worker on Linux, `TriggerEvent` RPC) add and remove keys; every change recomputes seven controls against `lastEvents` and emits only diffs to registered callbacks. A watchdog expires web-held keys. Platform code is isolated behind build tags.

**Tech Stack:** Go 1.25+, `go.viam.com/rdk v1.7.0`, `go.viam.com/utils v0.10.1` (RDK's pin), `github.com/viamrobotics/evdev v0.1.3` (RDK's pin, Linux only), stdlib `testing`.

**Read first:** `docs/SPEC.md` in full. It is the source of truth; this plan is the build order. When they disagree, the spec wins and this plan gets fixed.

**Conventions for every task:**
- Run `gofmt -s -w .` before each commit.
- Tests use stdlib `testing` only. No test frameworks.
- Commit after each task with the message given. The repo has no commits yet; Task 1 makes the first.
- All Go files live at the repo root in package `keyboard` except `cmd/module/main.go`.
- The `input.Controller` interface is all-or-nothing, so `module.go` is written once in final form in Task 2. Do not introduce stubs or partial states.

---

### Task 1: Dependencies, entry point, baseline commit

**Files:**
- Modify: `go.mod`
- Create: `go.sum` (generated)
- Replace: `cmd/module/main.go`
- Delete: `cmd/cli/main.go` (scaffold CLI; calls a constructor signature we are replacing, and the Makefile never builds it)
- Delete: `devrel_keyboard_input.md` (scaffold placeholder; the README in Task 4 replaces it)

**Step 1: Remove scaffold leftovers and pull in the RDK**

Run:
```bash
cd /Users/nick.hehr/src/keyboard
rm cmd/cli/main.go devrel_keyboard_input.md
rmdir cmd/cli
go get go.viam.com/rdk@v1.7.0
go mod tidy
```
Expected: `go.mod` lists `go.viam.com/rdk v1.7.0` under `require` and bumps the `go` directive to `1.25.10`. `go.sum` exists. `go mod tidy` does not type-check, so the broken scaffold `module.go` does not stop it. It will pull a few of the scaffold's unused imports (`go.viam.com/api`, `google.golang.org/protobuf`) in as direct requires; the tidy in Task 3 demotes them to indirect once `module.go` is replaced.

**Step 2: Rewrite the module entry point with keyed struct fields**

The scaffold uses positional fields, which `go vet` rejects. Replace `cmd/module/main.go` with:

```go
package main

import (
	"keyboard"

	input "go.viam.com/rdk/components/input"
	"go.viam.com/rdk/module"
	"go.viam.com/rdk/resource"
)

func main() {
	module.ModularMain(resource.APIModel{API: input.API, Model: keyboard.Input})
}
```

**Step 3: Verify**

Run: `go list -m all | grep -E 'go.viam.com/rdk |viamrobotics/evdev|viam.com/utils '`
Expected: three lines, `rdk v1.7.0`, `evdev v0.1.3`, `utils v0.10.1`.

**Step 4: Commit**

```bash
git add go.mod go.sum .gitignore .github Makefile meta.json .viam-gen-info cmd docs module.go README.md DEVELOPER_GUIDE.md
git commit -m "chore: scaffold keyboard input module with RDK deps and spec"
```

---

### Task 2: Controller state machine, interface, TriggerEvent, watchdog, Close

This is the whole of `module.go` plus the non-Linux stub for `deviceWorker`. The evdev reader comes in Task 3.

**Files:**
- Replace: `module.go` (whole file)
- Create: `keyboard_other.go`
- Create: `module_test.go`

**Step 1: Write the failing tests**

Create `module_test.go` exactly as follows. The `key` helper drives a source directly, bypassing `TriggerEvent`; `deviceConnected` and `deviceLost` are the entry points the evdev worker will call in Task 3.

```go
package keyboard

import (
	"context"
	"runtime"
	"testing"
	"time"

	"go.viam.com/rdk/components/input"
	"go.viam.com/rdk/logging"
)

func intp(i int) *int { return &i }

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"empty", Config{}, false},
		{"wasd", Config{Layout: "wasd"}, false},
		{"arrows", Config{Layout: "arrows"}, false},
		{"bad layout", Config{Layout: "dvorak"}, true},
		{"zero timeout", Config{HoldTimeoutMs: intp(0)}, false},
		{"negative timeout", Config{HoldTimeoutMs: intp(-1)}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := c.cfg.Validate("components.0")
			if (err != nil) != c.wantErr {
				t.Fatalf("Validate() err = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

func TestLayoutsCoverEveryAction(t *testing.T) {
	for name, keys := range layouts {
		seen := map[action]bool{}
		for _, a := range keys {
			seen[a] = true
		}
		for a := actForward; a <= actEStop; a++ {
			if !seen[a] {
				t.Errorf("layout %q missing action %d", name, a)
			}
		}
	}
}

// newTestKB returns a controller with every control subscribed to AllEvents
// and a pointer to the slice of events received, in order.
func newTestKB(t *testing.T, cfg Config) (*keyboard, *[]input.Event) {
	t.Helper()
	ctx := context.Background()
	if cfg.HoldTimeoutMs == nil {
		cfg.HoldTimeoutMs = intp(0) // no watchdog goroutine in unit tests unless asked
	}
	k, err := NewInput(ctx, input.Named("kb"), &cfg, logging.NewTestLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	kb := k.(*keyboard)
	t.Cleanup(func() { _ = kb.Close(ctx) })
	var got []input.Event
	for _, c := range controls {
		if err := kb.RegisterControlCallback(ctx, c, []input.EventType{input.AllEvents},
			func(_ context.Context, ev input.Event) { got = append(got, ev) }, nil); err != nil {
			t.Fatal(err)
		}
	}
	return kb, &got
}

func value(t *testing.T, kb *keyboard, c input.Control) float64 {
	t.Helper()
	evs, err := kb.Events(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return evs[c].Value
}

// key drives a key from a source directly, bypassing TriggerEvent.
func key(t *testing.T, kb *keyboard, src source, code string, pressed bool) {
	t.Helper()
	act, ok := kb.keys[code]
	if !ok {
		t.Fatalf("unknown code %q for this layout", code)
	}
	kb.mu.Lock()
	defer kb.mu.Unlock()
	kb.keyLocked(context.Background(), src, act, pressed, time.Now())
}

func web(kb *keyboard, code string, typ input.EventType) error {
	return kb.TriggerEvent(context.Background(),
		input.Event{Control: input.Control(code), Event: typ, Value: 1}, nil)
}

func TestMappingWASD(t *testing.T) {
	kb, _ := newTestKB(t, Config{Layout: "wasd"})
	cases := []struct {
		code string
		ctrl input.Control
		want float64
	}{
		{"KeyW", input.AbsoluteHat0Y, -1}, {"KeyS", input.AbsoluteHat0Y, 1},
		{"KeyA", input.AbsoluteHat0X, -1}, {"KeyD", input.AbsoluteHat0X, 1},
		{"KeyQ", input.ButtonLT, 1}, {"KeyE", input.ButtonRT, 1},
		{"KeyZ", input.ButtonWest, 1}, {"KeyC", input.ButtonEast, 1},
		{"Space", input.ButtonEStop, 1},
	}
	for _, c := range cases {
		key(t, kb, srcEvdev, c.code, true)
		if got := value(t, kb, c.ctrl); got != c.want {
			t.Errorf("%s pressed: %s = %v, want %v", c.code, c.ctrl, got, c.want)
		}
		key(t, kb, srcEvdev, c.code, false)
		if got := value(t, kb, c.ctrl); got != 0 {
			t.Errorf("%s released: %s = %v, want 0", c.code, c.ctrl, got)
		}
	}
}

func TestMappingArrows(t *testing.T) {
	kb, _ := newTestKB(t, Config{Layout: "arrows"})
	cases := []struct {
		code string
		ctrl input.Control
		want float64
	}{
		{"ArrowUp", input.AbsoluteHat0Y, -1}, {"ArrowDown", input.AbsoluteHat0Y, 1},
		{"ArrowLeft", input.AbsoluteHat0X, -1}, {"ArrowRight", input.AbsoluteHat0X, 1},
		{"ShiftLeft", input.ButtonLT, 1}, {"ShiftRight", input.ButtonRT, 1},
		{"ControlLeft", input.ButtonWest, 1}, {"ControlRight", input.ButtonEast, 1},
		{"Space", input.ButtonEStop, 1},
	}
	for _, c := range cases {
		key(t, kb, srcEvdev, c.code, true)
		if got := value(t, kb, c.ctrl); got != c.want {
			t.Errorf("%s pressed: %s = %v, want %v", c.code, c.ctrl, got, c.want)
		}
		key(t, kb, srcEvdev, c.code, false)
	}
}

func TestOppositeKeysCancel(t *testing.T) {
	kb, got := newTestKB(t, Config{})
	key(t, kb, srcEvdev, "KeyW", true)
	key(t, kb, srcEvdev, "KeyS", true)
	if v := value(t, kb, input.AbsoluteHat0Y); v != 0 {
		t.Fatalf("both held: Hat0Y = %v, want 0", v)
	}
	key(t, kb, srcEvdev, "KeyW", false)
	if v := value(t, kb, input.AbsoluteHat0Y); v != 1 {
		t.Fatalf("only S held: Hat0Y = %v, want 1", v)
	}
	for _, ev := range *got {
		if ev.Control == input.AbsoluteHat0Y && ev.Event != input.PositionChangeAbs && ev.Event != input.Connect {
			t.Errorf("hat emitted %s, want PositionChangeAbs", ev.Event)
		}
	}
}

func TestRepeatPressEmitsNothing(t *testing.T) {
	kb, got := newTestKB(t, Config{})
	key(t, kb, srcEvdev, "KeyW", true)
	n := len(*got)
	key(t, kb, srcEvdev, "KeyW", true)
	if len(*got) != n {
		t.Fatalf("repeat press emitted %d events", len(*got)-n)
	}
}

func TestTwoSourcesUnion(t *testing.T) {
	kb, _ := newTestKB(t, Config{})
	key(t, kb, srcEvdev, "KeyW", true)
	key(t, kb, srcWeb, "KeyW", true)
	key(t, kb, srcWeb, "KeyW", false)
	if v := value(t, kb, input.AbsoluteHat0Y); v != -1 {
		t.Fatalf("evdev still holds W: Hat0Y = %v, want -1", v)
	}
	key(t, kb, srcEvdev, "KeyW", false)
	if v := value(t, kb, input.AbsoluteHat0Y); v != 0 {
		t.Fatalf("both released: Hat0Y = %v, want 0", v)
	}
}

func TestEStopClearsAllAndEmitsPress(t *testing.T) {
	kb, got := newTestKB(t, Config{})
	key(t, kb, srcEvdev, "KeyW", true)
	key(t, kb, srcWeb, "KeyQ", true)
	*got = nil
	key(t, kb, srcEvdev, "Space", true)
	want := map[input.Control]input.Event{
		input.AbsoluteHat0Y: {Event: input.PositionChangeAbs, Value: 0},
		input.ButtonLT:      {Event: input.ButtonRelease, Value: 0},
		input.ButtonEStop:   {Event: input.ButtonPress, Value: 1},
	}
	for _, ev := range *got {
		w, ok := want[ev.Control]
		if !ok {
			t.Errorf("unexpected event %+v", ev)
			continue
		}
		if ev.Event != w.Event || ev.Value != w.Value {
			t.Errorf("%s: got %s/%v, want %s/%v", ev.Control, ev.Event, ev.Value, w.Event, w.Value)
		}
		delete(want, ev.Control)
	}
	if len(want) != 0 {
		t.Errorf("missing events for %v", want)
	}
	key(t, kb, srcEvdev, "Space", false)
	if v := value(t, kb, input.ButtonEStop); v != 0 {
		t.Fatalf("EStop after release = %v, want 0", v)
	}
}

func TestConnectSweepReemitsHeld(t *testing.T) {
	kb, got := newTestKB(t, Config{})
	key(t, kb, srcWeb, "KeyW", true)
	*got = nil
	kb.deviceConnected(context.Background())
	sawConnect, sawReemit := false, false
	for _, ev := range *got {
		if ev.Control == input.AbsoluteHat0Y && ev.Event == input.Connect {
			sawConnect = true
		}
		if ev.Control == input.AbsoluteHat0Y && ev.Event == input.PositionChangeAbs && ev.Value == -1 {
			sawReemit = true
		}
	}
	if !sawConnect || !sawReemit {
		t.Fatalf("connect=%v reemit=%v, want both", sawConnect, sawReemit)
	}
	key(t, kb, srcWeb, "KeyW", false)
	if v := value(t, kb, input.AbsoluteHat0Y); v != 0 {
		t.Fatalf("after release Hat0Y = %v, want 0", v)
	}
}

func TestDeviceLostReleasesEvdevKeys(t *testing.T) {
	kb, got := newTestKB(t, Config{})
	key(t, kb, srcEvdev, "KeyW", true)
	key(t, kb, srcWeb, "KeyQ", true)
	*got = nil
	kb.deviceLost(context.Background())
	if v := value(t, kb, input.AbsoluteHat0Y); v != 0 {
		t.Fatalf("Hat0Y = %v, want 0 after device lost", v)
	}
	if v := value(t, kb, input.ButtonLT); v != 1 {
		t.Fatalf("web-held LT = %v, want 1 (re-emitted after Disconnect sweep)", v)
	}
	// Order: zero axis, then Disconnect sweep, then LT re-emitted.
	idxZero, idxDisc, idxRe := -1, -1, -1
	for i, ev := range *got {
		switch {
		case ev.Control == input.AbsoluteHat0Y && ev.Event == input.PositionChangeAbs && idxZero < 0:
			idxZero = i
		case ev.Control == input.ButtonLT && ev.Event == input.Disconnect:
			idxDisc = i
		case ev.Control == input.ButtonLT && ev.Event == input.ButtonPress && idxDisc >= 0 && i > idxDisc:
			idxRe = i
		}
	}
	if !(idxZero >= 0 && idxZero < idxDisc && idxDisc < idxRe) {
		t.Fatalf("event order wrong: zero=%d disc=%d re=%d", idxZero, idxDisc, idxRe)
	}
}

func TestTriggerEventPressRelease(t *testing.T) {
	kb, _ := newTestKB(t, Config{})
	if err := web(kb, "KeyA", input.ButtonPress); err != nil {
		t.Fatal(err)
	}
	if v := value(t, kb, input.AbsoluteHat0X); v != -1 {
		t.Fatalf("Hat0X = %v, want -1", v)
	}
	if err := web(kb, "KeyA", input.ButtonRelease); err != nil {
		t.Fatal(err)
	}
	if v := value(t, kb, input.AbsoluteHat0X); v != 0 {
		t.Fatalf("Hat0X = %v, want 0", v)
	}
}

func TestTriggerEventRejectsUnknown(t *testing.T) {
	kb, _ := newTestKB(t, Config{})
	if err := web(kb, "AbsoluteHat0Y", input.PositionChangeAbs); err == nil {
		t.Fatal("pre-mapped control accepted; spec forbids passthrough")
	}
	if err := web(kb, "KeyX", input.ButtonPress); err == nil {
		t.Fatal("unmapped key accepted")
	}
	if err := web(kb, "KeyW", input.PositionChangeAbs); err == nil {
		t.Fatal("unsupported event type accepted")
	}
}

func TestHoldIsKeepaliveOnly(t *testing.T) {
	kb, got := newTestKB(t, Config{})
	*got = nil
	if err := web(kb, "KeyW", input.ButtonHold); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 || value(t, kb, input.AbsoluteHat0Y) != 0 {
		t.Fatal("Hold for a key never pressed created a press")
	}
	_ = web(kb, "KeyW", input.ButtonPress)
	before := kb.held[srcWeb][actForward]
	time.Sleep(2 * time.Millisecond)
	_ = web(kb, "KeyW", input.ButtonHold)
	if !kb.held[srcWeb][actForward].After(before) {
		t.Fatal("Hold did not refresh timestamp")
	}
}

func TestTriggerEventUsesServerClock(t *testing.T) {
	kb, _ := newTestKB(t, Config{HoldTimeoutMs: intp(60000)})
	// Epoch (what an unset proto Timestamp decodes to) and zero Time must both be held.
	epoch := input.Event{Control: "KeyW", Event: input.ButtonPress, Time: time.Unix(0, 0)}
	unset := input.Event{Control: "KeyA", Event: input.ButtonPress}
	for _, ev := range []input.Event{epoch, unset} {
		if err := kb.TriggerEvent(context.Background(), ev, nil); err != nil {
			t.Fatal(err)
		}
	}
	kb.mu.Lock()
	kb.expireWebLocked(context.Background(), time.Now())
	kb.mu.Unlock()
	if v := value(t, kb, input.AbsoluteHat0Y); v != -1 {
		t.Fatalf("epoch-timed press expired immediately: Hat0Y = %v", v)
	}
	if v := value(t, kb, input.AbsoluteHat0X); v != -1 {
		t.Fatalf("unset-time press expired immediately: Hat0X = %v", v)
	}
	if ev, _ := kb.Events(context.Background(), nil); ev[input.AbsoluteHat0Y].Time.Unix() <= 0 {
		t.Fatalf("emitted event time was not defaulted to now: %v", ev[input.AbsoluteHat0Y].Time)
	}
}

func TestWatchdogExpiresWebOnly(t *testing.T) {
	kb, _ := newTestKB(t, Config{HoldTimeoutMs: intp(60000)})
	_ = web(kb, "KeyW", input.ButtonPress)
	key(t, kb, srcEvdev, "KeyQ", true)
	kb.mu.Lock()
	kb.expireWebLocked(context.Background(), time.Now().Add(61*time.Second))
	kb.mu.Unlock()
	if v := value(t, kb, input.AbsoluteHat0Y); v != 0 {
		t.Fatalf("web W not expired: Hat0Y = %v", v)
	}
	if v := value(t, kb, input.ButtonLT); v != 1 {
		t.Fatalf("evdev Q was expired: LT = %v", v)
	}
}

func TestButtonChangeRegistersBoth(t *testing.T) {
	kb, _ := newTestKB(t, Config{})
	var seen []input.EventType
	_ = kb.RegisterControlCallback(context.Background(), input.ButtonLT,
		[]input.EventType{input.ButtonChange},
		func(_ context.Context, ev input.Event) { seen = append(seen, ev.Event) }, nil)
	key(t, kb, srcEvdev, "KeyQ", true)
	key(t, kb, srcEvdev, "KeyQ", false)
	if len(seen) != 2 || seen[0] != input.ButtonPress || seen[1] != input.ButtonRelease {
		t.Fatalf("ButtonChange saw %v", seen)
	}
}

func TestControlsFixed(t *testing.T) {
	kb, _ := newTestKB(t, Config{Layout: "arrows"})
	cs, _ := kb.Controls(context.Background(), nil)
	if len(cs) != 7 {
		t.Fatalf("Controls() = %v, want 7", cs)
	}
}

func TestCloseReleasesAndRejects(t *testing.T) {
	kb, _ := newTestKB(t, Config{})
	_ = web(kb, "KeyW", input.ButtonPress)
	if err := kb.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := value(t, kb, input.AbsoluteHat0Y); v != 0 {
		t.Fatalf("Close left Hat0Y = %v", v)
	}
	if err := web(kb, "KeyW", input.ButtonPress); err == nil {
		t.Fatal("TriggerEvent after Close succeeded")
	}
}

func TestNonLinuxRejectsDevFile(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("linux builds support dev_file")
	}
	_, err := NewInput(context.Background(), input.Named("kb"),
		&Config{DevFile: "/dev/input/event0", HoldTimeoutMs: intp(0)}, logging.NewTestLogger(t))
	if err == nil {
		t.Fatal("dev_file accepted on non-linux")
	}
}
```

**Step 2: Run to verify failure**

Run: `go test ./... 2>&1 | head -5`
Expected: compile errors from the scaffold `module.go` still in place (unused imports, `undefined: errors`). This is the only honest red state available: the interface cannot be half-implemented.

**Step 3: Write `module.go`**

Replace the entire file:

```go
// Package keyboard implements a Viam input controller driven by keyboard keys,
// from a local evdev device (Linux) or from a browser via TriggerEvent.
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

// Validate checks attributes. No dependencies.
func (cfg *Config) Validate(path string) ([]string, []string, error) {
	if cfg.Layout != "" {
		if _, ok := layouts[cfg.Layout]; !ok {
			return nil, nil, resource.NewConfigValidationError(path,
				fmt.Errorf("layout must be \"wasd\" or \"arrows\", got %q", cfg.Layout))
		}
	}
	if cfg.HoldTimeoutMs != nil && *cfg.HoldTimeoutMs < 0 {
		return nil, nil, resource.NewConfigValidationError(path,
			errors.New("hold_timeout_ms must be >= 0"))
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

// buttonControls are the actions that map 1:1 to a button control.
// Hat axes are synthesized from the forward/back and left/right pairs.
var buttonControls = map[action]input.Control{
	actZDown:     input.ButtonLT,
	actZUp:       input.ButtonRT,
	actGripClose: input.ButtonWest,
	actGripOpen:  input.ButtonEast,
	actEStop:     input.ButtonEStop,
}

// controls is the fixed list returned by Controls(), regardless of layout.
var controls = []input.Control{
	input.AbsoluteHat0X, input.AbsoluteHat0Y,
	input.ButtonLT, input.ButtonRT, input.ButtonWest, input.ButtonEast, input.ButtonEStop,
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

	// mu guards everything below and is held during callback dispatch.
	// Callbacks must not call back into this controller.
	mu         sync.Mutex
	closed     bool
	held       [numSources]map[action]time.Time // last press or keepalive, server clock
	lastEvents map[input.Control]input.Event
	callbacks  map[input.Control]map[input.EventType]input.ControlFunction

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
		callbacks:   map[input.Control]map[input.EventType]input.ControlFunction{},
	}
	for s := range k.held {
		k.held[s] = map[action]time.Time{}
	}

	k.mu.Lock()
	k.sweepLocked(ctx, input.Connect)
	k.recomputeLocked(ctx, time.Now())
	k.mu.Unlock()

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
// Caller holds k.mu.
func (k *keyboard) keyLocked(ctx context.Context, src source, act action, pressed bool, at time.Time) {
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
	k.recomputeLocked(ctx, at)
}

func (k *keyboard) isHeld(act action) bool {
	for s := range k.held {
		if _, ok := k.held[s][act]; ok {
			return true
		}
	}
	return false
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// recomputeLocked derives every control from the held sets and emits only
// the ones whose value differs from lastEvents. Caller holds k.mu.
func (k *keyboard) recomputeLocked(ctx context.Context, at time.Time) {
	k.setLocked(ctx, input.AbsoluteHat0Y, b2f(k.isHeld(actBack))-b2f(k.isHeld(actForward)), at)
	k.setLocked(ctx, input.AbsoluteHat0X, b2f(k.isHeld(actRight))-b2f(k.isHeld(actLeft)), at)
	for act, ctrl := range buttonControls {
		k.setLocked(ctx, ctrl, b2f(k.isHeld(act)), at)
	}
}

func (k *keyboard) setLocked(ctx context.Context, ctrl input.Control, val float64, at time.Time) {
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
	k.emitLocked(ctx, ev)
}

// emitLocked records the event and fires callbacks. Caller holds k.mu.
func (k *keyboard) emitLocked(ctx context.Context, ev input.Event) {
	k.lastEvents[ev.Control] = ev
	cbs := k.callbacks[ev.Control]
	if f := cbs[ev.Event]; f != nil {
		f(ctx, ev)
	}
	if f := cbs[input.AllEvents]; f != nil {
		f(ctx, ev)
	}
}

// sweepLocked emits typ (Connect or Disconnect) for every control, which
// zeroes the comparison baseline. Callers follow it with recomputeLocked.
func (k *keyboard) sweepLocked(ctx context.Context, typ input.EventType) {
	now := time.Now()
	for _, c := range controls {
		k.emitLocked(ctx, input.Event{Time: now, Event: typ, Control: c})
	}
}

// deviceConnected is called by the evdev worker after opening the device.
func (k *keyboard) deviceConnected(ctx context.Context) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.sweepLocked(ctx, input.Connect)
	k.recomputeLocked(ctx, time.Now())
}

// deviceLost is called by the evdev worker when the device goes away.
func (k *keyboard) deviceLost(ctx context.Context) {
	k.mu.Lock()
	defer k.mu.Unlock()
	clear(k.held[srcEvdev])
	k.recomputeLocked(ctx, time.Now())
	k.sweepLocked(ctx, input.Disconnect)
	k.recomputeLocked(ctx, time.Now())
}

// evdevKey is the evdev worker's entry point. code is a browser
// KeyboardEvent.code name; unmapped codes are ignored.
func (k *keyboard) evdevKey(ctx context.Context, code string, pressed bool, at time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if act, ok := k.keys[code]; ok {
		k.keyLocked(ctx, srcEvdev, act, pressed, at)
	}
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

// RegisterControlCallback mirrors webgamepad: ButtonChange expands to Press+Release.
func (k *keyboard) RegisterControlCallback(
	_ context.Context, control input.Control, triggers []input.EventType,
	f input.ControlFunction, _ map[string]interface{},
) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.callbacks[control] == nil {
		k.callbacks[control] = map[input.EventType]input.ControlFunction{}
	}
	for _, tr := range triggers {
		if tr == input.ButtonChange {
			k.callbacks[control][input.ButtonPress] = f
			k.callbacks[control][input.ButtonRelease] = f
			continue
		}
		k.callbacks[control][tr] = f
	}
	return nil
}

// TriggerEvent accepts raw browser key codes only. Press adds to the web
// held set, Release removes, Hold refreshes an existing hold (keepalive).
// Timestamps use the server clock; inbound Time only feeds Event.Time.
func (k *keyboard) TriggerEvent(ctx context.Context, ev input.Event, _ map[string]interface{}) error {
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
	if at.IsZero() || at.Unix() <= 0 {
		at = time.Now()
	}
	switch ev.Event {
	case input.ButtonPress:
		k.keyLocked(ctx, srcWeb, act, true, at)
	case input.ButtonRelease:
		k.keyLocked(ctx, srcWeb, act, false, at)
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
			k.expireWebLocked(ctx, now)
			k.mu.Unlock()
		}
	}
}

// expireWebLocked is the watchdog body, separated so tests can drive it with
// a chosen clock. Caller holds k.mu.
func (k *keyboard) expireWebLocked(ctx context.Context, now time.Time) {
	changed := false
	for act, ts := range k.held[srcWeb] {
		if now.Sub(ts) > k.holdTimeout {
			delete(k.held[srcWeb], act)
			changed = true
		}
	}
	if changed {
		k.recomputeLocked(ctx, now)
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
	defer k.mu.Unlock()
	for s := range k.held {
		clear(k.held[s])
	}
	k.recomputeLocked(ctx, time.Now())
	return nil
}
```

**Step 4: Write `keyboard_other.go`**

```go
//go:build !linux

package keyboard

import (
	"context"
	"errors"
)

func (k *keyboard) deviceWorker(string, bool) (func(context.Context), error) {
	return nil, errors.New("dev_file is only supported on linux; leave it unset for web-only use")
}
```

**Step 5: Run tests and vet**

Run: `go vet ./... && go test ./... -race`
Expected: vet clean; `ok  keyboard`. `TestNonLinuxRejectsDevFile` passes on macOS via the stub and skips on Linux.

**Step 6: Commit**

```bash
gofmt -s -w . && git add module.go keyboard_other.go module_test.go && git commit -m "feat: keyboard input controller state machine, TriggerEvent, watchdog, Close"
```

---

### Task 3: evdev reader (Linux)

**Files:**
- Create: `keyboard_linux.go`
- Create: `keyboard_linux_test.go`
- Modify: `go.mod`, `go.sum` (tidy adds the evdev zip hash)

**Step 1: Write the failing test**

Create `keyboard_linux_test.go`:

```go
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
```

**Step 2: Run to verify failure**

Run: `GOOS=linux GOARCH=arm64 go vet ./... 2>&1 | head -3`
Expected: `k.deviceWorker undefined` (on linux, `keyboard_other.go` is excluded; vet reports the non-test package first, so `evdevCodes` may not appear in the first three lines).

**Step 3: Write `keyboard_linux.go`**

```go
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
```

**Step 4: Tidy so go.sum has the evdev module hash**

Task 1's tidy only recorded evdev's `go.mod` hash (it was in the graph through the RDK but nothing imported it). Now a file imports it, and every Linux build fails with `missing go.sum entry` until you run:

```bash
go mod tidy
```
Expected: `go.mod` gains `github.com/viamrobotics/evdev v0.1.3` as a direct require and demotes the scaffold's stray `go.viam.com/api` and `google.golang.org/protobuf` to `// indirect`.

**Step 5: Verify on both platforms**

Run:
```bash
go vet ./... && go test ./... -race
GOOS=linux GOARCH=arm64 go vet ./...
GOOS=linux GOARCH=arm64 go test -c -o /dev/null .
GOOS=linux GOARCH=amd64 go build -o /dev/null ./cmd/module
```
Expected: all clean. The linux test binary compiles but cannot run on macOS; that is fine.

**Step 6: Commit**

```bash
gofmt -s -w . && git add go.mod go.sum keyboard_linux.go keyboard_linux_test.go && git commit -m "feat: evdev keyboard reader on linux with reconnect"
```

---

### Task 4: Web test page and README

**Files:**
- Create: `examples/web/index.html`
- Replace: `README.md`

**Step 1: Write the page**

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Keyboard Input</title>
<style>
  body { font: 16px system-ui, sans-serif; margin: 2rem auto; max-width: 40rem; padding: 0 1rem; }
  label { display: block; margin: .5rem 0; }
  input { width: 100%; padding: .4rem; font: inherit; }
  #status { margin-top: 1rem; padding: .75rem; background: #eee; border-radius: .5rem; }
  #held { font-family: monospace; min-height: 1.5em; }
  kbd { display: inline-block; padding: .1rem .4rem; border: 1px solid #999; border-radius: .25rem; margin: .1rem; }
</style>
</head>
<body>
<h1>Keyboard → Viam input controller</h1>
<form id="f">
  <label>Machine host <input name="host" placeholder="my-machine-main.abc123.viam.cloud" required></label>
  <label>API key ID <input name="keyId" required></label>
  <label>API key <input name="key" type="password" required></label>
  <label>Controller name <input name="name" value="keyboard" required></label>
  <button>Connect</button>
</form>
<div id="status">Not connected.</div>
<p>Held keys: <span id="held"></span></p>
<p>WASD move, Q/E down/up, Z/C gripper, Space stop. Or arrows, Shift L/R, Ctrl L/R, Space with the <code>arrows</code> layout.</p>

<script type="module">
import { createRobotClient, InputControllerClient } from "https://esm.sh/@viamrobotics/sdk@0.49.0";

const MAPPED = new Set(["KeyW","KeyA","KeyS","KeyD","KeyQ","KeyE","KeyZ","KeyC","Space",
  "ArrowUp","ArrowDown","ArrowLeft","ArrowRight","ShiftLeft","ShiftRight","ControlLeft","ControlRight"]);

const status = document.getElementById("status");
const heldEl = document.getElementById("held");
const held = new Set();
let kb = null;

// Time is omitted on purpose: the module ignores client clocks and defaults Event.Time to now.
const send = (control, event, value) =>
  kb?.triggerEvent({ control, event, value }).catch((e) => { status.textContent = `Send failed: ${e.message}`; });

const render = () => { heldEl.innerHTML = [...held].map((c) => `<kbd>${c}</kbd>`).join(" "); };
const releaseAll = () => { held.forEach((c) => send(c, "ButtonRelease", 0)); held.clear(); render(); };

document.getElementById("f").addEventListener("submit", async (e) => {
  e.preventDefault();
  const d = new FormData(e.target);
  status.textContent = "Connecting…";
  try {
    const machine = await createRobotClient({
      host: d.get("host"),
      signalingAddress: "https://app.viam.com:443",
      credentials: { type: "api-key", payload: d.get("key"), authEntity: d.get("keyId") },
    });
    kb = new InputControllerClient(machine, d.get("name"));
    await kb.getEvents(); // proves the component exists
    status.textContent = "Connected. Click outside the form, then hold keys.";
    document.activeElement?.blur();
  } catch (err) {
    status.textContent = `Connect failed: ${err.message}`;
  }
});

addEventListener("keydown", (e) => {
  if (!MAPPED.has(e.code) || e.target.tagName === "INPUT") return;
  e.preventDefault();
  if (e.repeat || held.has(e.code)) return;
  held.add(e.code); render(); send(e.code, "ButtonPress", 1);
});
addEventListener("keyup", (e) => {
  if (!MAPPED.has(e.code)) return;
  e.preventDefault();
  if (held.delete(e.code)) { render(); send(e.code, "ButtonRelease", 0); }
});
setInterval(() => held.forEach((c) => send(c, "ButtonHold", 1)), 200);
addEventListener("blur", releaseAll);
addEventListener("beforeunload", releaseAll);
document.addEventListener("visibilitychange", () => { if (document.hidden) releaseAll(); });
</script>
</body>
</html>
```

The `InputControllerClient` in SDK 0.49.0 has exactly three methods: `getEvents`, `triggerEvent`, `doCommand`. There is no `getControls` on the client class. `time` is omitted from `triggerEvent` on purpose; the module defaults it to now and never trusts client clocks.

**Step 2: Smoke-check it loads**

Run: `open examples/web/index.html` (macOS) and confirm the form renders and the browser console shows no import error. No machine needed for this step.

**Step 3: Write the README**

Replace `README.md` (outer fence is four backticks because the file contains its own code fences):

````markdown
# devrel:keyboard

A Viam `input_controller` driven by a keyboard. Emits the same controls a
gamepad does, so anything that consumes a gamepad (for example
`hipsterbrown:arm-remote-control`) works unchanged.

Keys can come from:

- a keyboard plugged into the machine (Linux only, via evdev), and/or
- a browser page calling `TriggerEvent` (any platform).

Design details: `docs/SPEC.md`.

## Model `devrel:keyboard:input`

### Configuration

```json
{
  "layout": "wasd",
  "dev_file": "/dev/input/by-id/usb-XXXX-event-kbd",
  "grab": false,
  "hold_timeout_ms": 500
}
```

| Name | Type | Default | Description |
|---|---|---|---|
| `layout` | `"wasd"` or `"arrows"` | `"wasd"` | Key layout. |
| `dev_file` | string | unset | evdev device to read. Leave unset for web-only. Linux only. |
| `grab` | bool | `false` | Take exclusive access so keystrokes do not also reach the console. Can lock you out of a tty on that keyboard. |
| `hold_timeout_ms` | int | `500` | Web-held keys auto-release if no keepalive arrives within this window. `0` disables. |

### Controls

| Control | `wasd` | `arrows` | Value |
|---|---|---|---|
| `AbsoluteHat0Y` | W / S | ↑ / ↓ | -1 forward, +1 back |
| `AbsoluteHat0X` | A / D | ← / → | -1 left, +1 right |
| `ButtonLT` | Q | Left Shift | Z down |
| `ButtonRT` | E | Right Shift | Z up |
| `ButtonWest` | Z | Left Ctrl | gripper close |
| `ButtonEast` | C | Right Ctrl | gripper open |
| `ButtonEStop` | Space | Space | stop; also releases every other key |

Axes are digital (-1, 0, +1). Opposite keys held together give 0.

### Local keyboard (Linux)

Find the device:

```sh
ls -l /dev/input/by-id/ | grep kbd
```

or run `evtest` and pick the device that reports your keypresses.

viam-server needs read/write on it. Add its user to the `input` group or
add a udev rule. The module reconnects automatically if the keyboard is
unplugged, and zeroes every control when that happens.

### Web keyboard

Any Viam TypeScript SDK client can be a keyboard. Send raw browser
`KeyboardEvent.code` strings as the control:

| Event | Meaning |
|---|---|
| `ButtonPress` | key down |
| `ButtonRelease` | key up |
| `ButtonHold` | keepalive, send every ~200ms while held |

The module ignores client timestamps. If keepalives stop for
`hold_timeout_ms`, the key is released server-side.

A ready-made page is in `examples/web/index.html`. Open it, enter the
machine host and an API key, click Connect, and hold keys.

### DoCommand

Not implemented.
````

**Step 4: Commit**

```bash
git add examples/web/index.html README.md && git commit -m "docs: README and web test page for keyboard input controller"
```

---

### Task 5: Deploy and manual verification on a Linux machine

**Files:** none

This task is hardware-in-the-loop. Skip and note it if no Linux machine with a USB keyboard is available; the rest of the plan does not depend on it.

**Step 1: Find the device on the machine**

On the machine:
```bash
ls -l /dev/input/by-id/ | grep -i kbd
id viam 2>/dev/null || ps -o user= -p "$(pgrep -f viam-server | head -1)"
```
If the viam-server user is not in `input`: `sudo usermod -aG input <user>` and restart viam-server.

**Step 2: Reload the module**

From this repo: `viam module reload --part-id <PART_ID>` (see `@viam-modules-fleet` skill for CLI details). Then add a component:

```json
{ "name": "keyboard", "api": "rdk:component:input_controller", "model": "devrel:keyboard:input",
  "attributes": { "dev_file": "/dev/input/by-id/<...>-event-kbd" } }
```

**Step 3: Verify, in order**

1. Machine logs show `keyboard device opened`.
2. Hold W on the USB keyboard. In the app's component card, `Events` shows `AbsoluteHat0Y` with `Value -1` and `PositionChangeAbs`.
3. Release W, value returns to 0.
4. Hold W, unplug the keyboard. Value returns to 0 and every control shows `Disconnect`. Replug: `Connect` appears, logs show reopen.
5. Open `examples/web/index.html`, connect, hold W. `Value -1`. Close the tab mid-hold. Within ~500ms `Events` shows 0.
6. Configure `hipsterbrown:arm-remote-control` with `"input_controller": "keyboard"` and drive the arm from both sources.

Record anything surprising in `docs/SPEC.md` under a new "Field notes" heading, and open follow-up tasks rather than fixing in place.

---

### Task 6: Final review

**Step 1: Full verification**

Run:
```bash
gofmt -s -l .            # expect no output
go vet ./...
go test ./... -race -count=3
GOOS=linux GOARCH=arm64 go build -o /dev/null ./cmd/module
GOOS=linux GOARCH=amd64 go build -o /dev/null ./cmd/module
make module.tar.gz && tar tzf module.tar.gz
```
Expected: no gofmt output, vet clean, tests `ok`, both cross-builds succeed, tarball lists `meta.json` and `bin/keyboard`. Do not run `./bin/keyboard --help`; `ModularMain` treats the argument as a socket path and blocks forever.

**Step 2: Spec conformance pass**

Read `docs/SPEC.md` "Testing" and confirm every unit bullet has a test in `module_test.go` or `keyboard_linux_test.go`. Add any that are missing.

**Step 3: Request code review**

Use `@superpowers:requesting-code-review` with the spec as the reference document.

**Step 4: Commit any fixes**

```bash
git add -A && git commit -m "fix: review follow-ups"
```
