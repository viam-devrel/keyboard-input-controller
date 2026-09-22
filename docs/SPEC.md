# Spec: `devrel:keyboard:input`

A Viam `input_controller` component that turns keyboard keys into the same
controls a gamepad emits, so existing gamepad consumers (e.g.
`hipsterbrown:arm-remote-control`) work unchanged. Keys can come from a
keyboard plugged into the machine (Linux evdev) or from a browser over the
`TriggerEvent` RPC.

## Goals

- One model that works as a local keyboard, a web keyboard, or both at once.
- Emit only standard `input.Control` values, digital ±1, so any gamepad
  consumer is a drop-in.
- Two selectable key layouts: WASD (default) and arrow keys (LeRobot's
  `keyboard_ee` layout).
- Safe on disconnect: a browser that dies or a keyboard that is unplugged
  mid-keypress must not leave the arm moving.

## Non-goals

- Analog values, acceleration curves, or speed modifiers. Step size belongs
  in the consumer, as in LeRobot's `end_effector_step_sizes`.
- Arbitrary user-defined keymaps. Two fixed layouts only.
- Accepting pre-mapped Viam controls over `TriggerEvent` the way
  `webgamepad` does. Every inbound event is a raw key so it goes through
  the held-key state and the watchdog.
- macOS or Windows local keyboard capture. viam-server has no display
  session; only the web path works there.
- Changes to `arm-remote-control`. It already reads `AbsoluteHat0X/Y` and
  `ButtonLT/RT`; gripper support there is separate future work.

## Configuration

```json
{
  "layout": "wasd",
  "dev_file": "/dev/input/by-id/usb-XXXX-event-kbd",
  "grab": false,
  "hold_timeout_ms": 500
}
```

| Attribute | Type | Default | Description |
|---|---|---|---|
| `layout` | `"wasd"` \| `"arrows"` | `"wasd"` | Which key layout maps to controls. |
| `dev_file` | string | unset | Linux evdev device to read. Unset means web-only. Constructor errors on non-Linux if set. |
| `grab` | bool | `false` | Take exclusive access to the device (`EVIOCGRAB`) so keystrokes do not also reach the console. Off by default because it can lock you out of a tty on that keyboard. |
| `hold_timeout_ms` | int | `500` | Keys held via `TriggerEvent` are auto-released if no keepalive arrives in this window. `0` disables the watchdog entirely. Otherwise must be >= 50; smaller values would drive a sub-millisecond watchdog ticker. Does not apply to evdev keys, which have real release events. |

`Validate` rejects unknown `layout` values, negative timeouts, and positive
timeouts below 50ms. No dependencies.

## Controls emitted

`Controls()` always returns exactly these seven, regardless of layout:

| Control | Type | Value |
|---|---|---|
| `AbsoluteHat0Y` | axis | -1 forward, +1 back, 0 neither or both |
| `AbsoluteHat0X` | axis | -1 left, +1 right, 0 neither or both |
| `ButtonLT` | button | Z down |
| `ButtonRT` | button | Z up |
| `ButtonWest` | button | gripper close |
| `ButtonEast` | button | gripper open |
| `ButtonEStop` | button | stop |

Axis events use `PositionChangeAbs`. Button events use `ButtonPress` and
`ButtonRelease`. `ButtonHold` is never emitted outbound (it is accepted
inbound as a keepalive, see below). `Connect` is emitted for every control
at construction (no callbacks exist yet, so this only seeds `Events()`) and
whenever the evdev device is (re)opened. `Disconnect` is emitted when the
evdev device is lost. Both are written to `lastEvents` with `Value: 0`,
which zeroes the comparison baseline, so every `Connect` or `Disconnect`
sweep is immediately followed by a recompute. That re-emits any control the
other source is currently holding and gives consumers true state right
after `Connect`.

Event `Time`: the triggering key event's time (evdev `timevaltoTime`, or
the inbound `TriggerEvent`'s `Time`) for synthesized events; `time.Now()`
for watchdog releases, device-loss releases, `Connect`, and `Disconnect`.

## Layouts

Keys are identified by browser `KeyboardEvent.code` strings. This is the
canonical key name for both sources; evdev codes are translated to it.

| Meaning | `wasd` | `arrows` | evdev constant (wasd / arrows) |
|---|---|---|---|
| forward (Hat0Y -1) | `KeyW` | `ArrowUp` | `KeyW` / `KeyUp` |
| back (Hat0Y +1) | `KeyS` | `ArrowDown` | `KeyS` / `KeyDown` |
| left (Hat0X -1) | `KeyA` | `ArrowLeft` | `KeyA` / `KeyLeft` |
| right (Hat0X +1) | `KeyD` | `ArrowRight` | `KeyD` / `KeyRight` |
| Z down (`ButtonLT`) | `KeyQ` | `ShiftLeft` | `KeyQ` / `KeyLeftShift` |
| Z up (`ButtonRT`) | `KeyE` | `ShiftRight` | `KeyE` / `KeyRightShift` |
| gripper close (`ButtonWest`) | `KeyZ` | `ControlLeft` | `KeyZ` / `KeyLeftCtrl` |
| gripper open (`ButtonEast`) | `KeyC` | `ControlRight` | `KeyC` / `KeyRightCtrl` |
| stop (`ButtonEStop`) | `Space` | `Space` | `KeySpace` |

The `arrows` layout follows LeRobot `KeyboardEndEffectorTeleop` with two
deliberate differences. LeRobot maps Left to +X and Right to -X; we keep
screen direction (Left is -1) to match the gamepad hat convention the
consumers already expect. LeRobot has no stop key (Esc disconnects its
listener); we add Space as `ButtonEStop` in both layouts.

Unknown keys are ignored silently by the evdev reader and rejected with an
error by `TriggerEvent`.

## Behavior

### Held-key state and axis synthesis

Two sets of held keys, one per source: `evdevHeld` and `webHeld`, each a
`map[code]time.Time` (last press or keepalive). A key counts as held when it
is in either set:

```
held(k) = k in evdevHeld || k in webHeld
hat0y   = (held[back]  ? 1 : 0) - (held[forward] ? 1 : 0)
hat0x   = (held[right] ? 1 : 0) - (held[left]    ? 1 : 0)
button  = held[key] ? 1 : 0
```

After any change to either set, recompute all seven control values and
compare each against `lastEvents[control].Value`. Emit an event only where
the value differs. There is no separately kept computed state; `lastEvents`
is the single source of truth for "what did we last tell consumers."

A single mutex guards `evdevHeld`, `webHeld`, `lastEvents`, `callbacks`,
and the closed flag. Every mutation plus its recompute, `lastEvents`
update, and callback dispatch happens under that lock, exactly as
`webgamepad` does, so concurrent sources (gRPC, watchdog, evdev worker)
can never emit duplicate or out-of-order events for the same control.
Consequence: callbacks must not call `RegisterControlCallback` or any other
method of this controller.

Opposite keys held together yield 0. A press of an already-held key from
the same source refreshes its timestamp and emits nothing. This applies to
the EStop key too: a repeat press of an already-held Space is refresh only
and does not re-run the clear-all below.

`ButtonEStop` press additionally clears both held sets, then re-inserts the
EStop code into the pressing source's set, then recomputes. One recompute
therefore emits every zeroed axis, every button release, and the
`ButtonEStop` press. `buttonControls`'s fixed order guarantees `ButtonEStop`
is always the last event of that recompute, after both axes are zeroed and
every other button's release has gone out. Space is a true panic key. Its
later release emits the `ButtonEStop` release as normal. Physical keys still
held will re-press on their next evdev event (there is none until release,
so they stay released); web keys stay released because keepalives for a key
that is not in `webHeld` are ignored.

### Callback dispatch

A `map[Control]map[EventType][]subscriber`, last event cached per control,
`ButtonChange` registration expands to `ButtonPress` + `ButtonRelease`,
`AllEvents` callbacks fire in addition to specific ones.

This deliberately diverges from the RDK `webgamepad`, which keeps a single
callback per control and event type and invokes it while holding its lock.
Both properties are defects for this module, whose whole purpose is to be
read by a consumer while a page drives it:

- **Several consumers may subscribe to the same control.** Entries are keyed
  by the registering context, which for a streaming consumer is its stream
  context. Registering again with the same context replaces that consumer's
  entry; a nil callback removes only that consumer. With a single slot, a
  second consumer silently displaced the first, which then received nothing.
- **Dispatch does not hold the mutex.** Events are queued under the lock and
  dispatched after releasing it. Holding the lock across dispatch meant one
  blocked subscriber stalled every later `RegisterControlCallback`, so a
  consumer hung on registration and never received anything. The RDK's input
  server installs a callback that sends on a 1024-slot channel and escapes
  only via its context, so a subscriber whose stream died is exactly that.
- **Departed consumers are dropped.** A subscriber whose context is done is
  skipped and pruned, because the RDK does not deregister a callback when
  its stream dies. Each call is also bounded, so a live-but-backed-up
  subscriber costs one delayed event rather than stalling the dispatcher.

Callbacks run on the dispatching goroutine with no lock held, so they may
call back into the controller, but dispatch is sequential and a slow
callback delays the events behind it. `RegisterControlCallback` does not replay `lastEvents` to the new
callback, so a consumer that (re)registers mid-hold sees zero state until
the next key change; this fails safe (worst case is a missed update, not a
stuck value) and self-heals on the next held-key transition.

### Source 1: evdev (Linux only)

Behind a `//go:build linux` tag. Adds `github.com/viamrobotics/evdev v0.1.3`
(the version RDK pins) to `go.mod`, imported only from that file so the
`darwin/arm64` target in `meta.json` keeps building.

When `dev_file` is set, a background worker loops until its context is
cancelled. It checks `ctx.Done()` before step 1 and at the device-lost
step; on cancel it closes the device if open and returns without emitting
`Disconnect` (`Close` runs its own release recompute).

1. `evdev.OpenFile(devFile)`. On error, wait 250ms via
   `utils.SelectContextOrWait`, retry. Log the failure once, and again only
   if the error text changes; log once on success. The file is opened
   read/write, so viam-server needs rw on the device (the `input` group has
   this on `/dev/input/event*`).
2. If `grab` is true, `dev.Lock()`. On failure (e.g. `EBUSY`, another
   process holds the grab), log once and continue ungrabbed.
3. Emit `Connect` for all controls, then recompute (see Controls emitted).
4. Read from `dev.Poll(ctx)`:
   ```
   ev, ok := <-ch
   if !ok || (ev.Event.Type == evdev.EventSync && evdev.SyncType(ev.Event.Code) == evdev.SyncDisconnect) {
       goto device lost
   }
   if ev.Event.Type != evdev.EventKey { continue }
   value 1 -> press, 0 -> release, 2 (autorepeat) -> ignore
   ```
   Note: `Poll` closes the channel on any read error and only sends
   `SyncDisconnect` first for `ENODEV`. The RDK gamepad's `<-evChan` with a
   nil check busy-spins on a closed channel; do not copy that.
5. Device lost: `dev.Close()`. If `ctx.Done()`, return (no `Disconnect`;
   `Close` handles its own release recompute). Otherwise clear `evdevHeld`,
   recompute (emits zero axes and releases), emit `Disconnect` for all
   controls, recompute again (re-emits anything the web source still
   holds), wait 250ms via `utils.SelectContextOrWait`, back to step 1. The
   wait matters even though open succeeded: a `dev_file` naming an
   openable-but-unreadable file (e.g. a regular file or `/dev/null`) makes
   `Poll` return immediately with no timeout error, and without the wait
   the worker would spin a hot open/read/close loop.

The README documents finding the device with `ls /dev/input/by-id/` and
`evtest`, and the `grab` tradeoff.

Non-Linux builds compile a stub whose constructor returns an error if
`dev_file` is set.

### Source 2: `TriggerEvent`

The component implements `input.Triggerable`. The RDK server passes
`Control` through verbatim with no validation, so raw key codes arrive
intact. Handling by `(Control, Event)`:

| `Control` | `Event` | Effect |
|---|---|---|
| layout key code | `ButtonPress` | `webHeld[code] = time.Now()`; recompute |
| layout key code | `ButtonRelease` | remove from `webHeld`; recompute |
| layout key code | `ButtonHold` | if already in `webHeld`, `webHeld[code] = time.Now()`; otherwise no-op |
| anything else | any | return error |

Held-key timestamps always use the server clock so the watchdog never
depends on the browser's clock or on the client setting `Time` at all. The
inbound `Time` is used only as the emitted `Event.Time` (zero or unset
inbound `Time` falls back to `time.Now()`). Inbound `Value` is ignored;
state is derived from `Event`. After `Close`, `TriggerEvent` returns an
error.

`ButtonHold` is the keepalive. It never creates a press, so a keepalive
that races past a release, or a keepalive for a key the module never saw
pressed, cannot re-press a key. This is why the keepalive is not a re-sent
`ButtonPress`.

### Watchdog

Started only when `hold_timeout_ms > 0`. A ticker at `hold_timeout_ms / 2`
removes any `webHeld` key whose timestamp is older than `hold_timeout_ms`
and runs the recompute. This is the safety net for a tab that closes or a
connection that drops mid-hold. `evdevHeld` is never touched by the
watchdog.

### Lifecycle

`AlwaysRebuild` (layout, device, or timeout change rebuilds). `Close`
sets the closed flag under the mutex (so late `TriggerEvent` calls error),
releases the mutex, cancels the context, waits for the evdev worker and
watchdog to exit (the mutex must not be held while waiting or a watchdog
tick mid-recompute deadlocks `Close`), and before returning clears both
held sets and runs the recompute so consumers see zeroed axes. `Poll` uses
a 1s read deadline, so `Close` may take up to about a second to unblock
the evdev worker; that is expected. Do not close the device from `Close`
to speed this up, it races the worker's handle. `DoCommand` handles the
`get_layout` command; see `docs/APP_SPEC.md` "Module change: `DoCommand`" for
the request/response shape. Any other command returns
`resource.ErrDoUnimplemented`.

## Web client contract

Any TypeScript SDK client is a valid keyboard. The contract:

- `keydown` with `!e.repeat` for a mapped code: `preventDefault()`, add to a
  local `held` set, send `ButtonPress`.
- `keyup` for a mapped code: `preventDefault()`, remove from `held`, send
  `ButtonRelease`.
- Every 200ms: send `ButtonHold` for each code in `held`. This figure assumes
  the default `hold_timeout_ms` (500); a client should scale its keepalive
  interval to the configured window rather than hardcode 200ms (the shipped
  app derives it via `keepaliveFor` in `frontend/src/lib/driver.ts`).
- On `blur`, `visibilitychange` (to hidden), and `beforeunload`: send
  `ButtonRelease` for each held code and clear the set.

`preventDefault` matters for the `arrows` layout, where arrows and Space
otherwise scroll the page. This holds only while that layout is active;
arrows are not `preventDefault`ed under `wasd`. The shipped page also scopes
capture to outside its `<form id="f">` (`e.target.closest("#f")`) on both
`keydown` and `keyup`, so typing into the host/API key fields never registers
as gameplay input; `keyup`'s release still runs even when the key originated
inside the form, since suppressing `preventDefault` there would break
Space-activating the Connect button.

```ts
import { createRobotClient, InputControllerClient } from "@viamrobotics/sdk";

const MAPPED = new Set(["KeyW","KeyA","KeyS","KeyD","KeyQ","KeyE","KeyZ","KeyC","Space",
  "ArrowUp","ArrowDown","ArrowLeft","ArrowRight","ShiftLeft","ShiftRight","ControlLeft","ControlRight"]);

const machine = await createRobotClient({
  host, // e.g. my-machine-main.abc123.viam.cloud
  signalingAddress: "https://app.viam.com:443", // WebRTC via app.viam.com; omit for LAN-only gRPC-web
  credentials: { type: "api-key", payload: apiKey, authEntity: apiKeyId },
});
const kb = new InputControllerClient(machine, "keyboard");
const held = new Set<string>();
// Time is omitted on purpose: the module ignores client clocks and defaults Event.Time to now.
const send = (control: string, event: string, value: number) =>
  kb.triggerEvent({ control, event, value });

addEventListener("keydown", (e) => {
  if (!MAPPED.has(e.code)) return;
  e.preventDefault();
  if (e.repeat || held.has(e.code)) return;
  held.add(e.code); send(e.code, "ButtonPress", 1);
});
addEventListener("keyup", (e) => {
  if (!MAPPED.has(e.code)) return;
  e.preventDefault();
  held.delete(e.code); send(e.code, "ButtonRelease", 0);
});
// 200ms assumes the default hold_timeout_ms; scale to the configured window
// (see `keepaliveFor` in frontend/src/lib/driver.ts) rather than hardcoding it.
setInterval(() => held.forEach((c) => send(c, "ButtonHold", 1)), 200);
const releaseAll = () => { held.forEach((c) => send(c, "ButtonRelease", 0)); held.clear(); };
addEventListener("blur", releaseAll);
addEventListener("beforeunload", releaseAll);
document.addEventListener("visibilitychange", () => { if (document.hidden) releaseAll(); });
```

`MAPPED` above is shown as the union of both layouts for brevity; the page
itself sends only the keys of the layout selected in its form, since
`TriggerEvent` rejects keys outside the module's configured layout.

Shipped as the `frontend/` app: a Svelte app bundled in the module and
registered as a Viam application (`docs/APP_SPEC.md`), which connects using
the machine identity from the Viam app cookie rather than manually entered
host/API key fields. This raw contract remains the reference for anyone
writing their own client.

## Files

| File | Purpose |
|---|---|
| `module.go` | Config, registration, held-key state, layouts, dispatch, `TriggerEvent`, watchdog |
| `keyboard_linux.go` | evdev reader (`//go:build linux`) |
| `keyboard_other.go` | stub for non-Linux |
| `module_test.go` | mapping, axis synthesis, watchdog, device-loss release |
| `keyboard_linux_test.go` | evdev key translation and decode (`//go:build linux`) |
| `frontend/` | Svelte app bundled as the shipped web client, driven by `get_layout` |
| `docs/APP_SPEC.md` | Design for the bundled `frontend/` application |
| `README.md` | config table, device setup, `grab` tradeoff, web usage |

Existing scaffold issues fixed along the way: `module.go` uses `fmt` and
`errors` without importing them and imports several unused packages.

## Testing

Unit (`go test ./...`), no hardware:

- Each layout maps every key in the table to the expected control and sign.
- Forward + back held yields `Hat0Y = 0`; releasing one yields the other's
  sign.
- Repeated press of a held key emits nothing.
- W held via evdev and via web, web releases: `Hat0Y` stays -1. evdev
  releases too: `Hat0Y` goes to 0.
- `TriggerEvent` with an unknown control or unknown event type errors.
- `ButtonHold` for a key not in `webHeld` emits nothing and does not add it.
- Watchdog releases a web-held key after `hold_timeout_ms` and emits the
  release; an evdev-held key is untouched.
- EStop press with W and Q held emits `Hat0Y = 0`, `ButtonLT` release, and
  `ButtonEStop` press; EStop release emits `ButtonEStop` release.
- `TriggerEvent` press with `Time` unset or epoch is still held for the full
  `hold_timeout_ms` (server clock, not client clock).
- Web holds W, evdev `Connect` sweep fires, `Hat0Y = -1` is re-emitted; web
  releases W, `Hat0Y = 0` is emitted.
- `TriggerEvent` after `Close` returns an error.
- Device-loss path (call the shared handler directly) clears evdev keys,
  emits zero axes, then `Disconnect`.
- `ButtonChange` registration fires on both press and release.
- `Close` with keys held emits releases.

Manual:

1. `viam module reload` to a Linux machine with a USB keyboard, set
   `dev_file`, hold W, confirm `Events()` in the app shows `AbsoluteHat0Y = -1`.
2. Unplug the keyboard while holding W, confirm `AbsoluteHat0Y` returns to 0
   and `Disconnect` appears.
3. Open the `frontend/` app (`docs/APP_SPEC.md`) against the same machine,
   hold W, kill the tab, confirm `AbsoluteHat0Y` returns to 0 within
   `hold_timeout_ms`.
4. Configure `arm-remote-control` with `input_controller: "keyboard"`, drive
   the arm from both sources.

## Decisions log

- X sign in the `arrows` layout follows gamepad convention, not LeRobot.
- Keepalive is inbound `ButtonHold`, not a re-sent `ButtonPress`, so a
  reordered or stray keepalive can never create a press.
- No `webgamepad`-style passthrough of pre-mapped controls; it would bypass
  the watchdog.
- `grab` defaults off; losing the console is worse than duplicate
  keystrokes.
- Held-key timestamps use the server clock only; the browser's clock is
  never trusted for safety.
- One mutex for all state and dispatch, matching `webgamepad`, in exchange
  for the rule that callbacks never call back into the controller.
