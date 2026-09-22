package keyboard

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"go.viam.com/rdk/components/input"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"
)

func intp(i int) *int { return &i }

// recorder collects dispatched events. Dispatch no longer runs under the
// controller's mutex, so it can arrive on a background goroutine (watchdog,
// evdev worker) while the test goroutine reads; guard it.
type recorder struct {
	mu   sync.Mutex
	evts []input.Event
}

func (r *recorder) add(ev input.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evts = append(r.evts, ev)
}

func (r *recorder) events() []input.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]input.Event(nil), r.evts...)
}

func (r *recorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evts = nil
}

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
		{"too-small positive timeout", Config{HoldTimeoutMs: intp(1)}, true},
		{"minimum positive timeout", Config{HoldTimeoutMs: intp(50)}, false},
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
		if want := int(actEStop) + 1; len(keys) != want {
			t.Errorf("layout %q has %d entries, want %d (an action is duplicated across codes)",
				name, len(keys), want)
		}
	}
}

// newTestKB returns a controller with every control subscribed to AllEvents
// and a pointer to the slice of events received, in order.
func newTestKB(t *testing.T, cfg Config) (*keyboard, *recorder) {
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
	rec := &recorder{}
	for _, c := range controls {
		if err := kb.RegisterControlCallback(ctx, c, []input.EventType{input.AllEvents},
			func(_ context.Context, ev input.Event) { rec.add(ev) }, nil); err != nil {
			t.Fatal(err)
		}
	}
	return kb, rec
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
	kb, rec := newTestKB(t, Config{})
	key(t, kb, "KeyW", true)
	key(t, kb, "KeyS", true)
	if v := value(t, kb, input.AbsoluteHat0Y); v != 0 {
		t.Fatalf("both held: Hat0Y = %v, want 0", v)
	}
	key(t, kb, "KeyW", false)
	if v := value(t, kb, input.AbsoluteHat0Y); v != 1 {
		t.Fatalf("only S held: Hat0Y = %v, want 1", v)
	}
	for _, ev := range rec.events() {
		if ev.Control == input.AbsoluteHat0Y && ev.Event != input.PositionChangeAbs && ev.Event != input.Connect {
			t.Errorf("hat emitted %s, want PositionChangeAbs", ev.Event)
		}
	}
}

func TestRepeatPressEmitsNothing(t *testing.T) {
	kb, rec := newTestKB(t, Config{})
	key(t, kb, "KeyW", true)
	n := len(rec.events())
	key(t, kb, "KeyW", true)
	if len(rec.events()) != n {
		t.Fatalf("repeat press emitted %d events", len(rec.events())-n)
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
	kb, rec := newTestKB(t, Config{})
	key(t, kb, "KeyW", true)
	if err := web(kb, "KeyQ", input.ButtonPress); err != nil {
		t.Fatal(err)
	}
	rec.reset()
	key(t, kb, "Space", true)
	want := map[input.Control]input.Event{
		input.AbsoluteHat0Y: {Event: input.PositionChangeAbs, Value: 0},
		input.ButtonLT:      {Event: input.ButtonRelease, Value: 0},
		input.ButtonEStop:   {Event: input.ButtonPress, Value: 1},
	}
	for _, ev := range rec.events() {
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
	kb, rec := newTestKB(t, Config{})
	if err := web(kb, "KeyW", input.ButtonPress); err != nil {
		t.Fatal(err)
	}
	rec.reset()
	kb.deviceConnected(context.Background())
	sawConnect, sawReemit := false, false
	for _, ev := range rec.events() {
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
	kb, rec := newTestKB(t, Config{Layout: "wasd"})
	rec.reset()
	kb.evdevKey(context.Background(), "ArrowUp", true, time.Now())
	if len(rec.events()) != 0 {
		t.Fatalf("unmapped code emitted %d events: %+v", len(rec.events()), rec.events())
	}
	if v := value(t, kb, input.AbsoluteHat0Y); v != 0 {
		t.Fatalf("unmapped code moved Hat0Y to %v, want 0", v)
	}
}

func TestDeviceLostReleasesEvdevKeys(t *testing.T) {
	kb, rec := newTestKB(t, Config{})
	key(t, kb, "KeyW", true)
	if err := web(kb, "KeyQ", input.ButtonPress); err != nil {
		t.Fatal(err)
	}
	rec.reset()
	kb.deviceLost(context.Background())
	if v := value(t, kb, input.AbsoluteHat0Y); v != 0 {
		t.Fatalf("Hat0Y = %v, want 0 after device lost", v)
	}
	if v := value(t, kb, input.ButtonLT); v != 1 {
		t.Fatalf("web-held LT = %v, want 1 (re-emitted after Disconnect sweep)", v)
	}
	// Order: zero axis, then Disconnect sweep, then LT re-emitted.
	idxZero, idxDisc, idxRe := -1, -1, -1
	for i, ev := range rec.events() {
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
	kb, rec := newTestKB(t, Config{})
	rec.reset()
	if err := web(kb, "KeyW", input.ButtonHold); err != nil {
		t.Fatal(err)
	}
	if len(rec.events()) != 0 || value(t, kb, input.AbsoluteHat0Y) != 0 {
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
	kb.expireWebLocked(time.Now())
	kb.mu.Unlock()
	kb.flush(context.Background())
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
	kb.expireWebLocked(time.Now())
	kb.mu.Unlock()
	kb.flush(context.Background())
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
	kb, rec := newTestKB(t, Config{})
	key(t, kb, "KeyW", true)
	key(t, kb, "Space", true) // first EStop press: clears W, holds EStop
	key(t, kb, "KeyW", true)  // W held again while EStop is still down
	rec.reset()
	key(t, kb, "Space", true) // repeat EStop press: refresh only
	if len(rec.events()) != 0 {
		t.Fatalf("repeat EStop press emitted %d events, want 0: %+v", len(rec.events()), rec.events())
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
	kb.expireWebLocked(time.Now().Add(61 * time.Second))
	kb.mu.Unlock()
	kb.flush(context.Background())
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

// TestRegisterNotBlockedBySlowCallback pins the fix for a consumer hanging in
// RegisterControlCallback. Dispatch used to run while the controller's mutex
// was held, so a subscriber whose callback blocked (an RDK StreamEvents
// ctrlFunc whose stream died, with a full 1024-slot channel) stalled every
// later registration behind the same lock.
func TestRegisterNotBlockedBySlowCallback(t *testing.T) {
	kb, _ := newTestKB(t, Config{})
	ctx := context.Background()

	blocking := make(chan struct{})
	if err := kb.RegisterControlCallback(ctx, input.AbsoluteHat0Y,
		[]input.EventType{input.PositionChangeAbs},
		func(context.Context, input.Event) { <-blocking }, nil); err != nil {
		t.Fatal(err)
	}

	// Join the dispatching goroutine before returning, or it logs after the
	// test completes and races the testing package.
	dispatched := make(chan struct{})
	go func() {
		defer close(dispatched)
		_ = web(kb, "KeyW", input.ButtonPress)
	}()
	defer func() {
		close(blocking)
		<-dispatched
	}()
	time.Sleep(100 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		done <- kb.RegisterControlCallback(ctx, input.AbsoluteHat0X,
			[]input.EventType{input.PositionChangeAbs},
			func(context.Context, input.Event) {}, nil)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("register returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RegisterControlCallback blocked behind a slow subscriber's callback")
	}
}

// TestTwoSubscribersBothReceive pins that independent consumers coexist. Each
// gRPC stream registers with its own context; a second consumer used to
// overwrite the first, which silently stopped the first from receiving.
func TestTwoSubscribersBothReceive(t *testing.T) {
	kb, _ := newTestKB(t, Config{})
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()

	var mu sync.Mutex
	var a, b int
	_ = kb.RegisterControlCallback(ctxA, input.AbsoluteHat0Y,
		[]input.EventType{input.PositionChangeAbs},
		func(context.Context, input.Event) { mu.Lock(); a++; mu.Unlock() }, nil)
	_ = kb.RegisterControlCallback(ctxB, input.AbsoluteHat0Y,
		[]input.EventType{input.PositionChangeAbs},
		func(context.Context, input.Event) { mu.Lock(); b++; mu.Unlock() }, nil)

	_ = web(kb, "KeyW", input.ButtonPress)
	mu.Lock()
	defer mu.Unlock()
	if a == 0 || b == 0 {
		t.Fatalf("subscriber A got %d events, B got %d; both should receive", a, b)
	}
}

// TestDepartedSubscriberDropped pins that a consumer whose context is done is
// neither called nor left occupying a slot. The RDK never deregisters a
// callback when its stream dies.
func TestDepartedSubscriberDropped(t *testing.T) {
	kb, _ := newTestKB(t, Config{})
	gone, cancel := context.WithCancel(context.Background())

	var mu sync.Mutex
	calls := 0
	_ = kb.RegisterControlCallback(gone, input.AbsoluteHat0Y,
		[]input.EventType{input.PositionChangeAbs},
		func(context.Context, input.Event) { mu.Lock(); calls++; mu.Unlock() }, nil)

	_ = web(kb, "KeyW", input.ButtonPress)
	mu.Lock()
	before := calls
	mu.Unlock()
	if before == 0 {
		t.Fatal("live subscriber received nothing")
	}

	cancel() // the consumer goes away without deregistering
	_ = web(kb, "KeyW", input.ButtonRelease)
	_ = web(kb, "KeyW", input.ButtonPress)

	mu.Lock()
	defer mu.Unlock()
	if calls != before {
		t.Fatalf("departed subscriber still called: %d -> %d", before, calls)
	}
	kb.mu.Lock()
	defer kb.mu.Unlock()
	if n := len(kb.callbacks[input.AbsoluteHat0Y][input.PositionChangeAbs]); n != 0 {
		t.Fatalf("departed subscriber still holds %d slot(s)", n)
	}
}

func TestActionStringIsStableAndDistinct(t *testing.T) {
	want := map[action]string{
		actForward: "forward", actBack: "back", actLeft: "left", actRight: "right",
		actZDown: "z_down", actZUp: "z_up",
		actGripClose: "gripper_close", actGripOpen: "gripper_open",
		actEStop: "stop",
	}
	seen := map[string]bool{}
	for act, name := range want {
		got := act.String()
		if got != name {
			t.Errorf("action %d: got %q, want %q", act, got, name)
		}
		if seen[got] {
			t.Errorf("duplicate action name %q", got)
		}
		seen[got] = true
	}
	if len(want) != len(layouts["wasd"]) {
		t.Fatalf("want covers %d actions but a layout has %d; add the new action here",
			len(want), len(layouts["wasd"]))
	}
}

func TestDoCommandGetLayout(t *testing.T) {
	cases := []struct {
		name        string
		cfg         Config
		wantLayout  string
		wantForward string
		wantTimeout float64
	}{
		// Every case sets HoldTimeoutMs explicitly. newTestKB rewrites a nil
		// HoldTimeoutMs to 0 so unit tests do not start a watchdog goroutine,
		// so Config{} here would report 0, not the 500 default. The default's
		// resolution is asserted in TestDoCommandReportsDefaultTimeout below,
		// which builds the controller directly.
		{"wasd", Config{HoldTimeoutMs: intp(500)}, "wasd", "KeyW", 500},
		{"arrows", Config{Layout: "arrows", HoldTimeoutMs: intp(500)}, "arrows", "ArrowUp", 500},
		{"configured timeout", Config{HoldTimeoutMs: intp(100)}, "wasd", "KeyW", 100},
		{"watchdog disabled", Config{HoldTimeoutMs: intp(0)}, "wasd", "KeyW", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kb, _ := newTestKB(t, c.cfg)
			res, err := kb.DoCommand(context.Background(), map[string]interface{}{"get_layout": true})
			if err != nil {
				t.Fatal(err)
			}
			if res["layout"] != c.wantLayout {
				t.Errorf("layout: got %v, want %v", res["layout"], c.wantLayout)
			}
			if got := res["hold_timeout_ms"]; got != c.wantTimeout {
				t.Errorf("hold_timeout_ms: got %v (%T), want %v", got, got, c.wantTimeout)
			}
			keys, ok := res["keys"].(map[string]interface{})
			if !ok {
				t.Fatalf("keys: got %T, want map[string]interface{}", res["keys"])
			}
			if len(keys) != len(layouts[c.wantLayout]) {
				t.Errorf("keys: got %d entries, want %d", len(keys), len(layouts[c.wantLayout]))
			}
			if keys["forward"] != c.wantForward {
				t.Errorf("keys[forward]: got %v, want %v", keys["forward"], c.wantForward)
			}
			if keys["stop"] != "Space" {
				t.Errorf("keys[stop]: got %v, want Space", keys["stop"])
			}
		})
	}
}

// The 500ms default cannot be asserted through newTestKB, which forces a nil
// HoldTimeoutMs to 0. Build the controller directly so the default resolves.
func TestDoCommandReportsDefaultTimeout(t *testing.T) {
	ctx := context.Background()
	k, err := NewInput(ctx, input.Named("kb"), &Config{}, logging.NewTestLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.Close(ctx) })
	res, err := k.DoCommand(ctx, map[string]interface{}{"get_layout": true})
	if err != nil {
		t.Fatal(err)
	}
	if got := res["hold_timeout_ms"]; got != float64(500) {
		t.Errorf("hold_timeout_ms: got %v, want 500", got)
	}
}

func TestDoCommandRejectsOtherCommands(t *testing.T) {
	kb, _ := newTestKB(t, Config{})
	if _, err := kb.DoCommand(context.Background(), map[string]interface{}{"set_layout": "arrows"}); !errors.Is(err, resource.ErrDoUnimplemented) {
		t.Fatalf("unknown command: got err %v, want resource.ErrDoUnimplemented", err)
	}
	if _, err := kb.DoCommand(context.Background(), map[string]interface{}{}); !errors.Is(err, resource.ErrDoUnimplemented) {
		t.Fatalf("empty command: got err %v, want resource.ErrDoUnimplemented", err)
	}
}
