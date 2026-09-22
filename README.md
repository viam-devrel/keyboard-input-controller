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
| `hold_timeout_ms` | int | `500` | Web-held keys auto-release if no keepalive arrives within this window. `0` disables; otherwise must be >= 50. |

### Controls

| Control | `wasd` | `arrows` | Value |
|---|---|---|---|
| `AbsoluteHat0Y` | W / S | ↑ / ↓ | -1 `hat_y_neg`, +1 `hat_y_pos` |
| `AbsoluteHat0X` | A / D | ← / → | -1 `hat_x_neg`, +1 `hat_x_pos` |
| `ButtonLT` | Q | Left Shift | `trigger_left` |
| `ButtonRT` | E | Right Shift | `trigger_right` |
| `ButtonWest` | Z | Left Ctrl | gripper close (emitted; not consumed by `arm-remote-control`) |
| `ButtonEast` | C | Right Ctrl | gripper open (emitted; not consumed by `arm-remote-control`) |
| `ButtonEStop` | Space | Space | releases every held key |

Axes are digital (-1, 0, +1). Opposite keys held together give 0.

These names describe the control a key drives, never a physical direction:
the module has no idea which reference frame a consumer (e.g.
`arm-remote-control`) maps its controls onto, so it cannot honestly claim
"forward" or "up". `gripper_open`/`gripper_close` and `stop` are the
exceptions, kept semantic because they mean the same thing in any frame.

With `hipsterbrown:arm-remote-control`, only `AbsoluteHat0X/Y` and
`ButtonLT/RT` are consumed. Space does not call `arm.Stop()`: releasing
every held key zeros the hat axes so the movement loop stops issuing new
moves, but an in-flight move completes, up to one `step_size` (default
10mm).

### Local keyboard (Linux)

Find the device:

```sh
ls -l /dev/input/by-id/ | grep kbd
```

or run `evtest` and pick the device that reports your keypresses.

viam-server needs read/write on it. Add its user to the `input` group or
add a udev rule. The module reconnects automatically if the keyboard is
unplugged, and zeroes every control the keyboard was holding.

If you see `cannot open keyboard device` in the machine's LOGS, `dev_file`
is wrong or viam-server lacks permission on it; the component otherwise
builds and shows green while silently emitting nothing.

### Web keyboard

Any Viam TypeScript SDK client can be a keyboard. Send raw browser
`KeyboardEvent.code` strings as the control:

| Event | Meaning |
|---|---|
| `ButtonPress` | key down |
| `ButtonRelease` | key up |
| `ButtonHold` | keepalive, send every ~200ms while held |

The 200ms figure assumes the default `hold_timeout_ms` (500); a client should
scale its keepalive interval to the configured window rather than hardcode
200ms, or a shorter `hold_timeout_ms` (e.g. 100) gets the key
watchdog-released mid-hold. The bundled app derives it via `keepaliveFor` in
`frontend/src/lib/driver.ts`.

The module ignores client timestamps. If keepalives stop for
`hold_timeout_ms`, the key is released server-side.

`TriggerEvent` rejects keys outside the configured layout; the shipped app
reads the layout automatically via `get_layout` (see below), so there is no
layout selector to keep in sync by hand.

A ready-made application ships with this module: the "keyboard-teleop" Viam
application, registered in `meta.json` and built from `frontend/`. Open it
from the machine's Viam app page, pick the `devrel:keyboard:input` component
and a camera, and hold keys. Design: `docs/APP_SPEC.md`. Changing the
component's `layout` while the app is already open doesn't recover on its
own — the WebRTC connection survives the config change, so the app never
re-probes `get_layout` and keeps sending the old codes (rejected with
`unknown key ... for this layout` per keystroke); reload the page to pick up
the new layout.

### DoCommand

`get_layout`: returns the component's layout name, resolved
`hold_timeout_ms`, and an `actions` array — for each action, the key that
drives it, the `input.Control` a consumer receives while it's held, and the
value that control carries. It reports controls rather than physical
directions on purpose: the module has no idea which reference frame the
consumer maps those controls onto, so it cannot honestly claim "forward" or
"up" — see the "Controls" table above. See `docs/APP_SPEC.md` "Module
change: `DoCommand`" for the exact request/response shape. Any other command
returns `resource.ErrDoUnimplemented`.
