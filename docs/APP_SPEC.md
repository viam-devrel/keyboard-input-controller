# Spec: `keyboard-teleop` Viam application

A single-machine [Viam application](https://docs.viam.com/build-apps/hosting/overview/)
bundled in this module, serving as the default web interface for driving a
`devrel:keyboard:input` component from a browser. It is the shipped
implementation of the "Web client contract" section of `docs/SPEC.md`, plus a
camera view so the operator can watch the robot while driving it.

## Goals

- Zero-credential launch: the app runs at a Viam app URL, reads the machine
  identity from the URL and the injected cookie, and connects. No host or API
  key entry.
- The user never has to know the module's configured `layout`. The app asks
  the component and captures exactly the keys that component accepts.
- A camera feed next to the keys, because teleop without seeing the robot is
  not teleop.
- The browser is never the only safety net. Every release path the contract
  requires is implemented, and the module's watchdog still backs it up.

## Non-goals

- Editing the component's configuration. The app reads the layout; it does not
  set it.
- Arm/gripper UI, jog buttons, pose readout. This app emits key events; what
  consumes them is somebody else's component.
- Multiple simultaneous camera views, onboarding wizards, persisted sessions
  beyond two `localStorage` selections.
- Replacing `docs/SPEC.md`'s raw contract snippet, which remains the reference
  for anyone writing their own client.
- A `local_machine` or `multi_machine` app type. Single machine only.

## Module change: `DoCommand`

`DoCommand` is currently unimplemented. It gains exactly one command.

Request: any map containing the key `get_layout`. Response:

```json
{
  "layout": "wasd",
  "hold_timeout_ms": 500,
  "keys": {
    "forward": "KeyW", "back": "KeyS", "left": "KeyA", "right": "KeyD",
    "z_down": "KeyQ", "z_up": "KeyE",
    "gripper_close": "KeyZ", "gripper_open": "KeyC",
    "stop": "Space"
  }
}
```

`keys` is the module's `layouts[layout]` map inverted: action name to
`KeyboardEvent.code`. Action names are a new `String()` on the existing
`action` type, so the two representations cannot drift. `hold_timeout_ms` is
the component's resolved watchdog window in milliseconds (`0` when disabled),
which the app needs to pick a keepalive interval — see `lib/driver.ts`. Any
other command returns `resource.ErrDoUnimplemented` as today.

This splits ownership cleanly: the module owns key-to-meaning, the app owns
presentation. The app hardcodes display order and human labels for the nine
known action names and falls back to rendering the raw action name for any it
does not recognise, so a module that adds an action does not break an older app.

`DoCommand` is also the app's identity probe. An `input_controller` that
answers is a `devrel:keyboard:input`; one that errors is not, and the app says
so rather than arming capture against a component that will reject every event.
A machine pinned to a pre-`get_layout` version of this module fails the same
probe, so the error text names both causes: "not a `devrel:keyboard:input`, or
an older version of the module that predates `get_layout`".

## Application registration

`meta.json` gains:

```json
"applications": [
  {
    "name": "keyboard-teleop",
    "type": "single_machine",
    "entrypoint": "frontend/dist/index.html",
    "customizations": {
      "machinePicker": {
        "heading": "Keyboard teleop",
        "subheading": "Sign in and pick the machine you want to drive"
      }
    }
  }
]
```

## Frontend

Svelte 5 + Vite, mirroring `../teach-frames/frontend`. npm, not pnpm, so the
build needs only mise-managed Node. No Tailwind and no `@viamrobotics/prime-core`:
the UI is a video, two `<select>`s, a key legend and a status line, which plain
CSS in `app.css` covers.

```
frontend/
  index.html  package.json  vite.config.ts  tsconfig.json  svelte.config.js
  src/
    main.ts
    App.svelte
    app.css
    lib/machine.ts          # machineId + cookie -> DialConf
    lib/driver.ts           # held-key state machine, keepalive, release-all
    lib/driver.test.ts
    lib/settings.ts         # localStorage, keyed per machine
    panels/SettingsBar.svelte
    panels/CameraView.svelte
    panels/KeyLegend.svelte
```

Dependencies: `@viamrobotics/sdk` and `js-cookie`. Not
`@viamrobotics/svelte-sdk`, which `teach-frames` needs because many components
deep in its tree each want their own reactive resource client; here `App.svelte`
owns the one machine, the one controller client and the one stream. Dropping it
also drops `@tanstack/svelte-query`.
Dev: `svelte`, `@sveltejs/vite-plugin-svelte`, `vite`, `vitest`, `jsdom`,
`typescript`, `svelte-check`, `@types/js-cookie`.

`vite.config.ts` sets `base: './'` (the app is served from a path, not a
domain root) and `build.outDir: 'dist'`.

### `lib/machine.ts`

Lifted from `teach-frames`: machine id is the third path segment of
`window.location.pathname` (`/machine/{id}/...`); the credentials are a JSON
cookie keyed by that id; the signaling address is `https://app.viam.com:443`.
Missing id or missing/unparseable cookie throws, and `App.svelte` renders the
message as a terminal error card. `viam module local-app-testing` injects the
same cookie, so dev and production take one code path.

### `lib/driver.ts`

The only non-trivial logic, kept free of Svelte so `vitest` can drive it:

```ts
interface Sink {
  press(code: string): void
  release(code: string): void
  hold(code: string): void
}

createDriver(sink: Sink, keys: Record<string, string>, keepaliveMs: number): {
  handleKeyDown(e: KeyboardEvent): void
  handleKeyUp(e: KeyboardEvent): void
  releaseAll(): void
  start(): void   // begins the keepalive interval
  stop(): void    // stops it and releases everything
  held: ReadonlySet<string>
}
```

`keys` is the `get_layout` response's map verbatim (action name to code). The
driver derives its own mapped-code set from the values and reads `keys.stop` for
the EStop branch below, so the legend and the capture set come from one object
and the driver stays drivable from `vitest` with no Svelte in scope.

Behaviour, matching `docs/SPEC.md`'s contract:

- `handleKeyDown`: ignore unmapped codes entirely (no `preventDefault`, so
  ordinary browser keys still work). Ignore events whose target is inside the
  settings bar (`e.target.closest('[data-settings]')`), so choosing a camera
  from the keyboard does not drive the robot. Otherwise `preventDefault()`,
  and if `e.repeat` or the code is already held, stop — no duplicate press.
  Else add to `held` and `sink.press(code)`.
- `handleKeyUp`: ignore unmapped codes. For mapped codes the release runs even
  when the target is inside the settings bar, so a key pressed before focus
  moved is still released; `preventDefault()` is skipped in that case, because
  suppressing it would break Space-activating a focused control.
- keepalive interval: `sink.hold(code)` for every held code. Nothing when the
  set is empty.
- `releaseAll`: `sink.release(code)` for every held code, then clear.

`keepaliveMs` is derived from the `hold_timeout_ms` in the `get_layout`
response, not hardcoded: `t > 0 ? clamp(Math.floor(t / 3), 20, 200) : 200`. `docs/SPEC.md`'s
contract quotes 200ms because it assumes the 500ms default, but `Validate`
accepts anything >= 50, and a component configured with `hold_timeout_ms: 100`
against a fixed 200ms keepalive would have the watchdog expire every held key
roughly twice a second — the axes would stutter while the key is still down.
Dividing by three leaves two keepalives inside every window. The floor of 20ms
bounds the request rate; the ceiling caps it for long windows. `0`
(watchdog disabled) falls back to 200ms, which costs nothing and keeps one code
path.

`App.svelte` wires `releaseAll` to `blur`, `visibilitychange` (to hidden) and
`beforeunload`, and calls `stop()` on destroy.

One divergence needs handling: an EStop press clears *both* held sets
server-side (`docs/SPEC.md`, "Held-key state"), so after Space the module holds
nothing while the app's local `held` set still lists every physically-down key.
The legend would keep highlighting them, and the keepalives would be ignored.
`handleKeyDown` therefore clears the local set the same way the module does: on
a press of `keys.stop`, `held` is emptied and the stop code re-added, and
`sink.press(stopCode)` is sent as for any other key — but no `release` calls,
since the module already zeroed everything. The existing repeat/already-held
guard runs first, so a held-down Space is a local no-op, mirroring the module's
"a repeat press of an already-held Space is refresh only".

The `Sink` passed in production maps to
`controller.triggerEvent({control: code, event: 'ButtonPress' | 'ButtonRelease' | 'ButtonHold', value})`.
`Time` is deliberately omitted; the module ignores client clocks.

### Settings bar

Always visible, `data-settings` on its container. Two selects:

- **Controller**: every `input_controller` in `resourceNames()`.
- **Camera**: every `camera` in `resourceNames()`, plus a "none" option.

Both default to the first available and persist to
`localStorage["keyboard-teleop:" + machineId]` as `{controller, camera}`. A
persisted name that is no longer in `resourceNames()` falls back to the default
rather than selecting nothing.

Changing the controller re-runs `get_layout`, which re-arms capture with the
new code set after releasing everything held under the old one.

### Camera view

`StreamClient.getStream(name)` into a `<video autoplay muted playsinline>` via
`srcObject`. Tracks are stopped and the stream released when the selection
changes, when the page is hidden, and on destroy, and the stream is
re-acquired when the page becomes visible again — without that, the video goes
permanently black after the first tab switch. "none" renders a placeholder and
starts nothing.

Whether `@viamrobotics/sdk` exposes a helper that already owns this lifecycle
is an implementation-time question; if it does, use it, otherwise drive
`StreamClient` directly.

### Key legend

Rendered from the `get_layout` response in a fixed display order (forward,
back, left, right, z_up, z_down, gripper_open, gripper_close, stop), each row
showing the human label and the `<kbd>` for its code. A held code is
highlighted, which doubles as the "is this thing on" indicator.

## States and errors

| Condition | UI |
|---|---|
| No machine id / no cookie | Terminal error card, nothing else renders |
| Connecting | Status line, capture disarmed |
| No `input_controller` on the machine | Status line explaining the machine has none; camera still works |
| Selected controller rejects `get_layout` | The two-cause message from "Module change: `DoCommand`" — capture disarmed, selection preserved so the user can pick another |
| Connected and armed | Legend + video; status shows the layout name |
| `triggerEvent` failure | Status line with the error, deduplicated by message so a stuck key cannot spam |
| Connection lost | `releaseAll()`, disarm, status line. The module's watchdog releases server-side within `hold_timeout_ms` regardless |

## Build

Node reaches the Viam cloud builder the way `../so-101` does it, because the
build step runs in a fresh shell where mise is not activated:

- `setup.sh` — installs mise if absent, `mise use -g node@22`, then `make setup`.
- `build.sh` — puts `~/.local/bin` on `PATH`, `eval "$(mise activate bash)"`,
  then `make module.tar.gz`.
- `meta.json.build` becomes `"setup": "./setup.sh"`, `"build": "./build.sh"`.

Makefile:

- `frontend:` runs `cd frontend && npm ci && npm run build`. `setup:` is left
  alone, so the cloud build installs once rather than twice.
- `module.tar.gz` depends on `frontend` and adds `frontend/dist` to `TAR_FILES`.
- `test:` also runs `cd frontend && npm test`.

`frontend/.gitignore` covers `node_modules/` and `dist/`; the repo's top-level
`.gitignore` is the Go module template and has neither.

The frontend is platform-independent but gets rebuilt for each of the four
`arch` targets. That is wasteful and harmless, and is what `so-101` does.

## Removals

`examples/web/index.html` is deleted. The app supersedes it, and `docs/SPEC.md`
already carries the raw-contract snippet for anyone writing their own client.

`docs/SPEC.md` is a living document, not a historical record, so it is updated
in the same change rather than left contradicting this one:

- "Lifecycle": `DoCommand returns resource.ErrDoUnimplemented` becomes the
  `get_layout` command, cross-referencing this spec.
- "Web client contract": drop "Ships as `examples/web/index.html`…" and point at
  `frontend/` instead; note that the 200ms keepalive in the snippet assumes the
  default `hold_timeout_ms` and that a client should scale it to the configured
  window.
- "Files" table: drop the `examples/web/index.html` row, add rows for
  `frontend/` and `docs/APP_SPEC.md`.
- "Testing", manual step 3: retarget from the example page to the app.

`README.md`'s "Web keyboard" section points at the app instead, and documents
`DoCommand`'s `get_layout` in place of "Not implemented". Its line "`TriggerEvent`
rejects keys outside the configured layout, so the page's layout selector must
match the module's `layout` attribute" is obsolete once the app reads the layout,
and becomes a note that the app does this automatically.

## Testing

Unit, `vitest`, on `lib/driver.ts` with a fake sink and fake timers:

- A mapped keydown presses once; a second keydown for the same code, and one
  with `repeat: true`, press nothing.
- Keyup releases and removes from `held`.
- An unmapped code neither presses nor calls `preventDefault`.
- A keydown whose target is inside `[data-settings]` does nothing; a keyup from
  the same target still releases a held code.
- A keepalive tick sends `hold` for every held code, and nothing when empty.
- `releaseAll` releases every held code and empties the set.
- `stop()` clears the interval and releases.
- A press of the stop code empties `held` of everything except the stop code and
  sends no `release` calls.
- An autorepeat `keydown` for a key the EStop cleared does not re-press it. This
  is the one path where `e.repeat` is load-bearing rather than shadowed by the
  already-held check, and it is the most safety-relevant line in the file.
- `stop()` leaves no interval ticking (observable only by pressing again after
  it), and a second `start()` does not double the keepalive rate.
- `keepaliveMs` derivation: 500 gives 166, 100 gives 33, 50 gives 20 (the
  floor, since 50 is `Validate`'s minimum and 50/3 is below it), 0 gives 200.

Unit, Go, in `module_test.go`:

- `DoCommand({"get_layout": true})` under `wasd` and under `arrows` returns
  that layout's name and the full nine-entry inverted map.
- The reported `hold_timeout_ms` is the resolved value: the 500 default when
  unset, the configured value when set, and 0 when the watchdog is disabled.
- `DoCommand` with any other command returns `resource.ErrDoUnimplemented`.

Manual:

`docs/APP_PLAN.md` Task 8 step 6 carries the full ordered procedure; it opens
with the published app URL, because `base: './'` makes a missing trailing slash
render a blank page and nothing else is testable until that is ruled out.

1. `viam module local-app-testing` against a machine with a configured
   `devrel:keyboard:input` and a camera. Confirm connect, legend matches the
   configured layout, and held keys highlight.
2. Hold W, kill the tab. Confirm `AbsoluteHat0Y` returns to 0 within ~1.5×
   `hold_timeout_ms` — the watchdog ticks at half the window and expires on
   strictly greater, so 1× is not the real bound.
3. Hold W, alt-tab away. Confirm release is immediate, not watchdog-delayed.
4. Point the controller select at a non-keyboard `input_controller`. Confirm
   the "not a devrel:keyboard:input" state and that no events are sent.
5. Deploy and open the published app URL; confirm the cookie path works
   unchanged from local-app-testing.
6. Drop the connection mid-session with keys held, then let it reconnect.
   Confirm both the keys release and the **video** returns — capture and the
   camera recover through independent paths.

## Decisions log

- The layout comes from `DoCommand`, not a dropdown. A mismatched dropdown
  produced an error per keystroke and was the module's sharpest edge.
- `DoCommand` doubles as the component identity probe; `resourceNames()` does
  not report models.
- Settings bar is always visible rather than an onboarding step. Two selects
  are cheaper to show than to hide and re-reveal.
- No `prime-core`, no Tailwind. Four widgets do not need a design system.
- npm over pnpm, so `setup.sh` installs one tool instead of two.
- Raw `@viamrobotics/sdk` over `@viamrobotics/svelte-sdk`. One machine, one
  controller client, one stream, all owned by the root component.
- The app's release-on-blur is a latency optimisation, not the safety
  mechanism. The server-side watchdog remains the guarantee.
- The keepalive interval is derived from the component's `hold_timeout_ms`
  rather than fixed at the contract's 200ms, because that figure assumes the
  default window and a shorter one makes held keys stutter.
- `docs/SPEC.md` is edited alongside this work rather than frozen. Two specs
  that disagree are worse than one that is longer.
