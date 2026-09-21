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

// key drives a key through evdevKey, the evdev worker's real entry point.
func key(t *testing.T, kb *keyboard, code string, pressed bool) {
	t.Helper()
	if _, ok := kb.keys[code]; !ok {
		t.Fatalf("unknown code %q for this layout", code)
	}
	kb.evdevKey(context.Background(), code, pressed, time.Now())
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
		key(t, kb, c.code, true)
		if got := value(t, kb, c.ctrl); got != c.want {
			t.Errorf("%s pressed: %s = %v, want %v", c.code, c.ctrl, got, c.want)
		}
		key(t, kb, c.code, false)
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
		key(t, kb, c.code, true)
		if got := value(t, kb, c.ctrl); got != c.want {
			t.Errorf("%s pressed: %s = %v, want %v", c.code, c.ctrl, got, c.want)
		}
		key(t, kb, c.code, false)
		if got := value(t, kb, c.ctrl); got != 0 {
			t.Errorf("%s released: %s = %v, want 0", c.code, c.ctrl, got)
		}
	}
}

func TestOppositeKeysCancel(t *testing.T) {
	kb, got := newTestKB(t, Config{})
	key(t, kb, "KeyW", true)
	key(t, kb, "KeyS", true)
	if v := value(t, kb, input.AbsoluteHat0Y); v != 0 {
		t.Fatalf("both held: Hat0Y = %v, want 0", v)
	}
	key(t, kb, "KeyW", false)
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
	key(t, kb, "KeyW", true)
	n := len(*got)
	key(t, kb, "KeyW", true)
	if len(*got) != n {
		t.Fatalf("repeat press emitted %d events", len(*got)-n)
	}
}

func TestTwoSourcesUnion(t *testing.T) {
	kb, _ := newTestKB(t, Config{})
	key(t, kb, "KeyW", true)
	if err := web(kb, "KeyW", input.ButtonPress); err != nil {
		t.Fatal(err)
	}
	if err := web(kb, "KeyW", input.ButtonRelease); err != nil {
		t.Fatal(err)
	}
	if v := value(t, kb, input.AbsoluteHat0Y); v != -1 {
		t.Fatalf("evdev still holds W: Hat0Y = %v, want -1", v)
	}
	key(t, kb, "KeyW", false)
	if v := value(t, kb, input.AbsoluteHat0Y); v != 0 {
		t.Fatalf("both released: Hat0Y = %v, want 0", v)
	}
}

func TestEStopClearsAllAndEmitsPress(t *testing.T) {
	kb, got := newTestKB(t, Config{})
	key(t, kb, "KeyW", true)
	if err := web(kb, "KeyQ", input.ButtonPress); err != nil {
		t.Fatal(err)
	}
	*got = nil
	key(t, kb, "Space", true)
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
	key(t, kb, "Space", false)
	if v := value(t, kb, input.ButtonEStop); v != 0 {
		t.Fatalf("EStop after release = %v, want 0", v)
	}
}

func TestConnectSweepReemitsHeld(t *testing.T) {
	kb, got := newTestKB(t, Config{})
	if err := web(kb, "KeyW", input.ButtonPress); err != nil {
		t.Fatal(err)
	}
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
	if err := web(kb, "KeyW", input.ButtonRelease); err != nil {
		t.Fatal(err)
	}
	if v := value(t, kb, input.AbsoluteHat0Y); v != 0 {
		t.Fatalf("after release Hat0Y = %v, want 0", v)
	}
}

// TestEvdevIgnoresUnmappedCode calls kb.evdevKey directly (not the key
// helper, whose own guard would reject an unmapped code before evdevKey's
// guard is exercised at all). Without evdevKey's own `if act, ok :=
// k.keys[code]; ok` check, an unmapped code would resolve to the zero-value
// action, actForward, so an unrecognized keystroke would command forward
// motion.
func TestEvdevIgnoresUnmappedCode(t *testing.T) {
	kb, got := newTestKB(t, Config{Layout: "wasd"})
	*got = nil
	kb.evdevKey(context.Background(), "ArrowUp", true, time.Now())
	if len(*got) != 0 {
		t.Fatalf("unmapped code emitted %d events: %+v", len(*got), *got)
	}
	if v := value(t, kb, input.AbsoluteHat0Y); v != 0 {
		t.Fatalf("unmapped code moved Hat0Y to %v, want 0", v)
	}
}

func TestDeviceLostReleasesEvdevKeys(t *testing.T) {
	kb, got := newTestKB(t, Config{})
	key(t, kb, "KeyW", true)
	if err := web(kb, "KeyQ", input.ButtonPress); err != nil {
		t.Fatal(err)
	}
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
	kb.mu.Lock()
	before := kb.held[srcWeb][actForward]
	kb.mu.Unlock()
	time.Sleep(2 * time.Millisecond)
	_ = web(kb, "KeyW", input.ButtonHold)
	kb.mu.Lock()
	after := kb.held[srcWeb][actForward]
	kb.mu.Unlock()
	if !after.After(before) {
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

// TestTriggerEventIgnoresSkewedClientClock guards against using the inbound
// Time to seed k.held: the browser's clock is never trusted for safety, so
// even a valid (non-zero, non-epoch) but skewed client timestamp must not
// affect when a press expires. Only the server's receipt time may.
func TestTriggerEventIgnoresSkewedClientClock(t *testing.T) {
	kb, _ := newTestKB(t, Config{HoldTimeoutMs: intp(60000)})
	skewed := input.Event{Control: "KeyW", Event: input.ButtonPress, Time: time.Now().Add(-10 * time.Minute)}
	if err := kb.TriggerEvent(context.Background(), skewed, nil); err != nil {
		t.Fatal(err)
	}
	kb.mu.Lock()
	kb.expireWebLocked(context.Background(), time.Now())
	kb.mu.Unlock()
	if v := value(t, kb, input.AbsoluteHat0Y); v != -1 {
		t.Fatalf("skewed-but-valid client time expired press early: Hat0Y = %v, want -1", v)
	}
}

// TestRepeatEStopPressDoesNotReclear checks that a repeat press of an
// already-held Space is refresh-only and does not re-run the clear-all: W is
// re-pressed between the two Space presses so an accidental extra clear
// becomes observable (a bare repeat with nothing else held would look
// identical either way, since the first Space press already cleared it).
func TestRepeatEStopPressDoesNotReclear(t *testing.T) {
	kb, got := newTestKB(t, Config{})
	key(t, kb, "KeyW", true)
	key(t, kb, "Space", true) // first EStop press: clears W, holds EStop
	key(t, kb, "KeyW", true)  // W held again while EStop is still down
	*got = nil
	key(t, kb, "Space", true) // repeat EStop press: refresh only
	if len(*got) != 0 {
		t.Fatalf("repeat EStop press emitted %d events, want 0: %+v", len(*got), *got)
	}
	kb.mu.Lock()
	_, held := kb.held[srcEvdev][actForward]
	kb.mu.Unlock()
	if !held {
		t.Fatal("repeat EStop press incorrectly cleared W")
	}
}

// waitForValue polls c's value every 2ms until it equals want or a ~5s
// deadline passes, failing the test on timeout. Used to observe a live
// background goroutine's effect without a fixed sleep. Polling means a
// generous deadline costs nothing on the common, fast-success path.
func waitForValue(t *testing.T, kb *keyboard, c input.Control, want float64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if v := value(t, kb, c); v == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not reach %v within deadline", c, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestWatchdogGoroutineExpiresHeldKey exercises the live watchdog goroutine
// (not expireWebLocked driven directly) to confirm it is actually wired up
// and ticking.
func TestWatchdogGoroutineExpiresHeldKey(t *testing.T) {
	kb, _ := newTestKB(t, Config{HoldTimeoutMs: intp(20)})
	if err := web(kb, "KeyW", input.ButtonPress); err != nil {
		t.Fatal(err)
	}
	if v := value(t, kb, input.AbsoluteHat0Y); v != -1 {
		t.Fatalf("press did not register: Hat0Y = %v", v)
	}
	waitForValue(t, kb, input.AbsoluteHat0Y, 0) // watchdog goroutine must expire it
}

// TestDefaultHoldTimeoutStartsWatchdog exercises the actual production
// default (no HoldTimeoutMs in config) end to end: newTestKB forces the
// timeout to 0 for every other test to keep the watchdog off, so nothing
// else in this suite constructs with the real default and confirms the
// watchdog goroutine is actually running under it.
func TestDefaultHoldTimeoutStartsWatchdog(t *testing.T) {
	ctx := context.Background()
	k, err := NewInput(ctx, input.Named("kb"), &Config{}, logging.NewTestLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	kb := k.(*keyboard)
	t.Cleanup(func() { _ = kb.Close(ctx) })
	if kb.holdTimeout != defaultHoldTimeout {
		t.Fatalf("holdTimeout = %v, want default %v", kb.holdTimeout, defaultHoldTimeout)
	}
	if err := kb.TriggerEvent(ctx, input.Event{Control: "KeyW", Event: input.ButtonPress}, nil); err != nil {
		t.Fatal(err)
	}
	if v := value(t, kb, input.AbsoluteHat0Y); v != -1 {
		t.Fatalf("press did not register: Hat0Y = %v", v)
	}
	waitForValue(t, kb, input.AbsoluteHat0Y, 0) // watchdog must fire under the real default timeout
}

func TestWatchdogExpiresWebOnly(t *testing.T) {
	kb, _ := newTestKB(t, Config{HoldTimeoutMs: intp(60000)})
	_ = web(kb, "KeyW", input.ButtonPress)
	key(t, kb, "KeyQ", true)
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
	key(t, kb, "KeyQ", true)
	key(t, kb, "KeyQ", false)
	if len(seen) != 2 || seen[0] != input.ButtonPress || seen[1] != input.ButtonRelease {
		t.Fatalf("ButtonChange saw %v", seen)
	}
}

// TestButtonControlsMatchControls pins two invariants that recomputeLocked
// relies on but cannot state locally: every control in buttonControls must
// also be in the fixed controls list (otherwise it would be emitted by
// recomputeLocked but never receive a Connect/Disconnect sweep and never
// appear in Controls()), and actEStop must be the last entry in
// buttonControls so its press is always the last event of an EStop
// recompute, after every other button's release.
func TestButtonControlsMatchControls(t *testing.T) {
	inControls := map[input.Control]bool{}
	for _, c := range controls {
		inControls[c] = true
	}
	for _, bc := range buttonControls {
		if !inControls[bc.ctrl] {
			t.Errorf("buttonControls has %s, which is missing from controls", bc.ctrl)
		}
	}
	if n, want := len(controls), len(buttonControls)+2; n != want {
		t.Errorf("controls has %d entries, want %d (2 hat axes + %d buttons)", n, want, len(buttonControls))
	}
	if last := buttonControls[len(buttonControls)-1]; last.act != actEStop {
		t.Errorf("buttonControls must end in actEStop so its press is emitted last; ends in %s", last.ctrl)
	}
}

func TestControlsFixed(t *testing.T) {
	kb, _ := newTestKB(t, Config{Layout: "arrows"})
	cs, err := kb.Controls(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []input.Control{
		input.AbsoluteHat0X, input.AbsoluteHat0Y,
		input.ButtonLT, input.ButtonRT, input.ButtonWest, input.ButtonEast, input.ButtonEStop,
	}
	if len(cs) != len(want) {
		t.Fatalf("Controls() = %v, want %v", cs, want)
	}
	for i, c := range want {
		if cs[i] != c {
			t.Fatalf("Controls()[%d] = %v, want %v", i, cs[i], c)
		}
	}
	// Mutating the returned slice must not affect a subsequent call.
	cs[0] = "corrupted"
	cs2, err := kb.Controls(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cs2[0] != input.AbsoluteHat0X {
		t.Fatalf("mutating the returned slice affected a later call: %v", cs2)
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
