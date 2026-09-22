# keyboard-teleop Application Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `docs/APP_SPEC.md` — a `get_layout` DoCommand on the Go component, and a Svelte app bundled in the module that drives it from a browser with a camera view.

**Architecture:** The Go component gains one command that reports its layout, keymap and resolved watchdog window. A Vite-built Svelte 5 app in `frontend/` reads the machine identity from the Viam app cookie, asks the selected `input_controller` for that layout, and translates browser key events into `TriggerEvent` calls. All the key-state logic lives in one Svelte-free module (`lib/driver.ts`) so `vitest` can drive it directly; the Svelte components are thin.

**Tech Stack:** Go (`go.viam.com/rdk`), Svelte 5 + Vite + vitest, `@viamrobotics/sdk`, `js-cookie`, mise-managed Node 22 for the cloud build.

**Read first:** `docs/APP_SPEC.md` (this plan implements it) and `docs/SPEC.md` "Web client contract" and "Held-key state" (the behaviour the driver must match).

---

## Dependencies

`@viamrobotics/sdk` and `js-cookie`, no `@viamrobotics/svelte-sdk`. `teach-frames`
needs its reactive `createResourceClient` because many components deep in a tree
each want their own client; here `App.svelte` owns the one machine, the one
controller client and the one stream. `docs/APP_SPEC.md` agrees — nothing to
decide.

---

## File structure

| File | Responsibility |
|---|---|
| `module.go` (modify) | `action.String()`, a `layout` field on `keyboard`, `DoCommand` |
| `module_test.go` (modify) | Tests for the above |
| `frontend/package.json`, `vite.config.ts`, `tsconfig.json`, `svelte.config.js`, `index.html`, `.gitignore` | Build config |
| `frontend/src/main.ts`, `App.svelte`, `app.css` | Shell: connect, own the clients, wire window listeners |
| `frontend/src/lib/machine.ts` | URL machine id + cookie → `DialConf` |
| `frontend/src/lib/driver.ts` | Held-key state machine, keepalive, EStop resync. **The only non-trivial logic.** |
| `frontend/src/lib/settings.ts` | `localStorage` selections + fallback resolution |
| `frontend/src/panels/SettingsBar.svelte` | Two selects |
| `frontend/src/panels/KeyLegend.svelte` | Legend from the `get_layout` keys, held highlight |
| `frontend/src/panels/CameraView.svelte` | Stream lifecycle into a `<video>` |
| `setup.sh`, `build.sh`, `Makefile`, `meta.json` (modify) | Build plumbing |
| `docs/SPEC.md`, `README.md` (modify), `examples/web/index.html` (delete) | Doc reconciliation |

---

## Task 1: `get_layout` DoCommand

**Files:**
- Modify: `module.go` (the `action` const block ~line 72, the `keyboard` struct ~line 140, `NewInput` ~line 171, `DoCommand` ~line 522)
- Test: `module_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `module_test.go`:

```go
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
	if _, err := kb.DoCommand(context.Background(), map[string]interface{}{"set_layout": "arrows"}); err == nil {
		t.Fatal("want error for unknown command, got nil")
	}
	if _, err := kb.DoCommand(context.Background(), map[string]interface{}{}); err == nil {
		t.Fatal("want error for empty command, got nil")
	}
}
```

This controller runs a real watchdog goroutine (`HoldTimeoutMs` is 500), which
is why it is built and closed by hand rather than through `newTestKB`.

Note `wantTimeout` is `float64`: `hold_timeout_ms` is declared as a float64 below so the value matches what a client sees after the protobuf `Struct` round-trip, where every number is a double. Asserting on `int` here would pass in-process and mislead.

- [ ] **Step 2: Run the tests, verify they fail**

Run: `go test ./... -run 'TestActionString|TestDoCommand' -v`
Expected: compile failure — `act.String undefined`.

- [ ] **Step 3: Add `action.String()`**

After the `action` const block in `module.go`:

```go
// String is the wire name for an action, used by DoCommand's get_layout so
// the app can label keys without duplicating the layout table in JS.
func (a action) String() string {
	switch a {
	case actForward:
		return "forward"
	case actBack:
		return "back"
	case actLeft:
		return "left"
	case actRight:
		return "right"
	case actZDown:
		return "z_down"
	case actZUp:
		return "z_up"
	case actGripClose:
		return "gripper_close"
	case actGripOpen:
		return "gripper_open"
	case actEStop:
		return "stop"
	}
	return fmt.Sprintf("action(%d)", int(a))
}
```

- [ ] **Step 4: Store the layout name**

`keyboard` keeps only the resolved keymap, not the name it came from. Add the field beside `keys`:

```go
	logger      logging.Logger
	layout      string            // name of the active layout, reported by DoCommand
	keys        map[string]action // active layout
	holdTimeout time.Duration
```

and set it in `NewInput`'s struct literal, where `layout` is already in scope:

```go
	k := &keyboard{
		Named:       name.AsNamed(),
		logger:      logger,
		layout:      layout,
		keys:        keys,
```

- [ ] **Step 5: Implement `DoCommand`**

Replace the stub:

```go
// DoCommand implements one command, get_layout, which reports the configured
// layout so a client can render a legend and send exactly the keys this
// component accepts. It doubles as an identity probe: an input_controller
// that answers this is a devrel:keyboard:input, which resourceNames() cannot
// tell a client because it does not report models.
func (k *keyboard) DoCommand(_ context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	if _, ok := cmd["get_layout"]; !ok {
		return nil, resource.ErrDoUnimplemented
	}
	keys := make(map[string]interface{}, len(k.keys))
	for code, act := range k.keys {
		keys[act.String()] = code
	}
	// float64 because the response crosses a protobuf Struct, where every
	// number is a double. Returning an int here would still arrive as a
	// float on the wire; being explicit keeps the tests honest.
	return map[string]interface{}{
		"layout":          k.layout,
		"hold_timeout_ms": float64(k.holdTimeout / time.Millisecond),
		"keys":            keys,
	}, nil
}
```

`k.layout` and `k.keys` are immutable after construction (`AlwaysRebuild`), so no lock is needed.

- [ ] **Step 6: Run the full Go suite**

Run: `go test ./...`
Expected: PASS, including the pre-existing tests. If `TestDoCommandGetLayout`
reports `hold_timeout_ms: 0` where 500 was wanted, a case lost its explicit
`HoldTimeoutMs` — see the comment in the table.

- [ ] **Step 7: Commit**

```bash
git add module.go module_test.go
git commit -m "feat: report the configured layout via a get_layout DoCommand"
```

---

## Task 2: Frontend scaffold

**Files:**
- Create: `frontend/package.json`, `frontend/vite.config.ts`, `frontend/tsconfig.json`, `frontend/svelte.config.js`, `frontend/index.html`, `frontend/.gitignore`, `frontend/src/main.ts`, `frontend/src/App.svelte`, `frontend/src/app.css`, `frontend/src/vite-env.d.ts`

- [ ] **Step 1: Check Node is available**

Run: `node --version`
Expected: v22 or later. If absent: `curl https://mise.run | sh && export PATH="$HOME/.local/bin:$PATH" && eval "$(mise activate bash)" && mise use -g node@22`.

- [ ] **Step 2: Create the package**

```bash
mkdir -p frontend/src/lib frontend/src/panels
cd frontend
npm init -y
npm pkg set name=keyboard-teleop private=true type=module
npm pkg set scripts.dev=vite scripts.build="vite build" scripts.preview="vite preview"
npm pkg set scripts.check="svelte-check --tsconfig ./tsconfig.json" scripts.test="vitest run"
npm install @viamrobotics/sdk js-cookie
npm install -D svelte @sveltejs/vite-plugin-svelte vite vitest jsdom typescript svelte-check @types/js-cookie
```

Let npm resolve current versions rather than pinning guessed ones; record whatever lands in the commit.

- [ ] **Step 3: Write the config files**

`frontend/.gitignore`:

```
node_modules/
dist/
```

`frontend/svelte.config.js`:

```js
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte'

export default { preprocess: vitePreprocess() }
```

`frontend/vite.config.ts`:

```ts
import { defineConfig } from 'vitest/config'
import { svelte } from '@sveltejs/vite-plugin-svelte'

export default defineConfig({
  // The app is served from a path under the Viam app host, never a domain
  // root, so asset URLs must be relative.
  base: './',
  plugins: [svelte()],
  build: { outDir: 'dist', emptyOutDir: true, target: 'esnext' },
  test: { environment: 'jsdom', globals: true },
})
```

`frontend/tsconfig.json`:

```json
{
  "compilerOptions": {
    "target": "ESNext",
    "module": "ESNext",
    "moduleResolution": "bundler",
    "lib": ["ESNext", "DOM", "DOM.Iterable"],
    "strict": true,
    "noEmit": true,
    "isolatedModules": true,
    "skipLibCheck": true,
    "types": ["vite/client", "vitest/globals"]
  },
  "include": ["src/**/*.ts", "src/**/*.svelte"]
}
```

`frontend/index.html`:

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>Keyboard teleop</title>
  </head>
  <body>
    <div id="app"></div>
    <script type="module" src="/src/main.ts"></script>
  </body>
</html>
```

`frontend/src/vite-env.d.ts`:

```ts
/// <reference types="svelte" />
/// <reference types="vite/client" />
```

`frontend/src/main.ts`:

```ts
import { mount } from 'svelte'
import './app.css'
import App from './App.svelte'

export default mount(App, { target: document.getElementById('app')! })
```

`frontend/src/app.css`: global rules only — `color-scheme: dark`, the page background and foreground, `system-ui`, the `#app` flex column, and the base `<kbd>` rule carried over from `examples/web/index.html`. Nothing that styles markup a later task writes: each component in `src/panels/` brings its own scoped `<style>` block, so no rule here can guess a class name wrong or silently orphan itself.

`frontend/src/App.svelte`: a placeholder `<h1>Keyboard teleop</h1>` for now. Task 5 fills it in.

- [ ] **Step 4: Verify it builds**

Run: `cd frontend && npm run build`
Expected: writes `frontend/dist/index.html` plus hashed assets, exit 0.

- [ ] **Step 5: Commit**

```bash
git add frontend
git commit -m "chore: scaffold the keyboard-teleop frontend"
```

---

## Task 3: `lib/driver.ts`

The held-key state machine. Everything here is pure enough to test without Svelte, which is the entire reason it is its own file — keep it that way.

**Files:**
- Create: `frontend/src/lib/driver.ts`
- Test: `frontend/src/lib/driver.test.ts`

- [ ] **Step 1: Write the failing tests**

`frontend/src/lib/driver.test.ts`:

```ts
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createDriver, keepaliveFor, type Sink } from './driver'

const KEYS = {
  forward: 'KeyW', back: 'KeyS', left: 'KeyA', right: 'KeyD',
  z_down: 'KeyQ', z_up: 'KeyE',
  gripper_close: 'KeyZ', gripper_open: 'KeyC',
  stop: 'Space',
}

function fakeSink() {
  const calls: string[] = []
  const sink: Sink = {
    press: (c) => calls.push(`press:${c}`),
    release: (c) => calls.push(`release:${c}`),
    hold: (c) => calls.push(`hold:${c}`),
  }
  return { sink, calls }
}

// A plain object is enough: the driver reads code, repeat and target, and
// calls preventDefault. Constructing a real KeyboardEvent would not let us
// set an arbitrary target without dispatching it.
function ev(code: string, opts: { repeat?: boolean; target?: EventTarget | null } = {}) {
  let prevented = false
  return {
    code,
    repeat: opts.repeat ?? false,
    target: opts.target ?? null,
    preventDefault() { prevented = true },
    get defaultPrevented() { return prevented },
  } as unknown as KeyboardEvent
}

function settingsTarget() {
  const bar = document.createElement('div')
  bar.setAttribute('data-settings', '')
  const select = document.createElement('select')
  bar.appendChild(select)
  document.body.appendChild(bar)
  return select
}

describe('keepaliveFor', () => {
  it('derives the interval from the component watchdog window', () => {
    expect(keepaliveFor(500)).toBe(166)
    expect(keepaliveFor(100)).toBe(33)
    expect(keepaliveFor(50)).toBe(20)    // 16 floored, raised by the floor
    expect(keepaliveFor(6000)).toBe(200) // capped by the ceiling
    expect(keepaliveFor(0)).toBe(200)    // watchdog disabled
  })
})

describe('createDriver', () => {
  beforeEach(() => { document.body.innerHTML = '' })

  it('presses a mapped key once', () => {
    const { sink, calls } = fakeSink()
    const d = createDriver(sink, KEYS, 200)
    const e = ev('KeyW')
    d.handleKeyDown(e)
    expect(calls).toEqual(['press:KeyW'])
    expect(e.defaultPrevented).toBe(true)
    expect([...d.held]).toEqual(['KeyW'])
  })

  it('does not re-press a held key or an autorepeat', () => {
    const { sink, calls } = fakeSink()
    const d = createDriver(sink, KEYS, 200)
    d.handleKeyDown(ev('KeyW'))
    d.handleKeyDown(ev('KeyW'))
    d.handleKeyDown(ev('KeyW', { repeat: true }))
    expect(calls).toEqual(['press:KeyW'])
  })

  it('releases on keyup and forgets the key', () => {
    const { sink, calls } = fakeSink()
    const d = createDriver(sink, KEYS, 200)
    d.handleKeyDown(ev('KeyW'))
    d.handleKeyUp(ev('KeyW'))
    expect(calls).toEqual(['press:KeyW', 'release:KeyW'])
    expect([...d.held]).toEqual([])
  })

  it('ignores unmapped keys entirely, including preventDefault', () => {
    const { sink, calls } = fakeSink()
    const d = createDriver(sink, KEYS, 200)
    const down = ev('KeyP')
    const up = ev('KeyP')
    d.handleKeyDown(down)
    d.handleKeyUp(up)
    expect(calls).toEqual([])
    expect(down.defaultPrevented).toBe(false)
    expect(up.defaultPrevented).toBe(false)
  })

  it('does not drive the robot from inside the settings bar', () => {
    const { sink, calls } = fakeSink()
    const d = createDriver(sink, KEYS, 200)
    const e = ev('Space', { target: settingsTarget() })
    d.handleKeyDown(e)
    expect(calls).toEqual([])
    // Not prevented, so Space still activates the focused control.
    expect(e.defaultPrevented).toBe(false)
  })

  it('still releases a key whose keyup lands in the settings bar', () => {
    const { sink, calls } = fakeSink()
    const d = createDriver(sink, KEYS, 200)
    d.handleKeyDown(ev('KeyW'))
    const up = ev('KeyW', { target: settingsTarget() })
    d.handleKeyUp(up)
    expect(calls).toEqual(['press:KeyW', 'release:KeyW'])
    expect(up.defaultPrevented).toBe(false)
  })

  it('keepalives every held key and nothing when idle', () => {
    vi.useFakeTimers()
    const { sink, calls } = fakeSink()
    const d = createDriver(sink, KEYS, 200)
    d.start()
    vi.advanceTimersByTime(200)
    expect(calls).toEqual([])
    d.handleKeyDown(ev('KeyW'))
    d.handleKeyDown(ev('KeyQ'))
    calls.length = 0
    vi.advanceTimersByTime(200)
    expect(calls.sort()).toEqual(['hold:KeyQ', 'hold:KeyW'])
    d.stop()
    vi.useRealTimers()
  })

  it('releaseAll releases everything held and clears', () => {
    const { sink, calls } = fakeSink()
    const d = createDriver(sink, KEYS, 200)
    d.handleKeyDown(ev('KeyW'))
    d.handleKeyDown(ev('KeyQ'))
    calls.length = 0
    d.releaseAll()
    expect(calls.sort()).toEqual(['release:KeyQ', 'release:KeyW'])
    expect([...d.held]).toEqual([])
  })

  it('stop clears the keepalive and releases', () => {
    vi.useFakeTimers()
    const { sink, calls } = fakeSink()
    const d = createDriver(sink, KEYS, 200)
    d.start()
    d.handleKeyDown(ev('KeyW'))
    calls.length = 0
    d.stop()
    expect(calls).toEqual(['release:KeyW'])
    vi.advanceTimersByTime(1000)
    expect(calls).toEqual(['release:KeyW'])
    vi.useRealTimers()
  })

  // docs/SPEC.md, "Held-key state": an EStop press clears BOTH held sets
  // server-side. The local set has to follow or the legend lies.
  it('EStop clears the local held set without sending releases', () => {
    const { sink, calls } = fakeSink()
    const d = createDriver(sink, KEYS, 200)
    d.handleKeyDown(ev('KeyW'))
    d.handleKeyDown(ev('KeyQ'))
    calls.length = 0
    d.handleKeyDown(ev('Space'))
    expect(calls).toEqual(['press:Space'])
    expect([...d.held]).toEqual(['Space'])
  })

  // The OS keeps autorepeating a physically-down key after an EStop cleared it
  // from `held`. That is the one state where `held.has()` does NOT shadow
  // `e.repeat`, so the repeat guard is the only thing stopping the driver from
  // re-pressing W and silently resuming the axis the EStop just zeroed.
  it('does not resurrect an autorepeating key that EStop cleared', () => {
    const { sink, calls } = fakeSink()
    const d = createDriver(sink, KEYS, 200)
    d.handleKeyDown(ev('KeyW'))
    d.handleKeyDown(ev('Space'))
    calls.length = 0
    d.handleKeyDown(ev('KeyW', { repeat: true }))
    expect(calls).toEqual([])
    expect([...d.held]).toEqual(['Space'])
  })

  // The existing stop() test cannot see a leaked interval, because releaseAll
  // empties `held` first and an orphaned timer over an empty set emits nothing.
  // Re-pressing after stop() is what makes the leak observable.
  it('stop really clears the interval, and start is idempotent', () => {
    vi.useFakeTimers()
    const { sink, calls } = fakeSink()
    const d = createDriver(sink, KEYS, 200)
    d.start()
    d.start()
    d.handleKeyDown(ev('KeyW'))
    calls.length = 0
    vi.advanceTimersByTime(200)
    expect(calls).toEqual(['hold:KeyW']) // one interval, not two
    d.stop()
    d.handleKeyDown(ev('KeyW'))
    calls.length = 0
    vi.advanceTimersByTime(1000)
    expect(calls).toEqual([]) // nothing still ticking
    vi.useRealTimers()
  })

  it('a repeat EStop press is a no-op', () => {
    const { sink, calls } = fakeSink()
    const d = createDriver(sink, KEYS, 200)
    d.handleKeyDown(ev('Space'))
    d.handleKeyDown(ev('KeyW'))
    calls.length = 0
    d.handleKeyDown(ev('Space'))
    expect(calls).toEqual([])
    expect([...d.held].sort()).toEqual(['KeyW', 'Space'])
  })
})
```

- [ ] **Step 2: Run the tests, verify they fail**

Run: `cd frontend && npm test`
Expected: FAIL — cannot resolve `./driver`.

- [ ] **Step 3: Implement the driver**

`frontend/src/lib/driver.ts`:

```ts
// The held-key state machine behind the app. Deliberately free of Svelte and
// of any Viam client so vitest can drive it directly; App.svelte supplies a
// Sink that forwards to TriggerEvent.
//
// It implements docs/SPEC.md's "Web client contract". Two parts of that are
// easy to get wrong and are load-bearing for safety:
//   - The keepalive interval must fit inside the component's hold_timeout_ms,
//     or the server-side watchdog releases keys that are still down.
//   - An EStop press clears both held sets server-side, so the local set has
//     to be cleared to match.

export interface Sink {
  press(code: string): void
  release(code: string): void
  hold(code: string): void
}

export interface Driver {
  handleKeyDown(e: KeyboardEvent): void
  handleKeyUp(e: KeyboardEvent): void
  releaseAll(): void
  start(): void
  stop(): void
  readonly held: ReadonlySet<string>
}

const MIN_KEEPALIVE_MS = 20
const MAX_KEEPALIVE_MS = 200

// keepaliveFor picks a keepalive interval for a component whose watchdog
// window is holdTimeoutMs. Dividing by three leaves two refreshes inside every
// window; the floor bounds the request rate, the ceiling caps it for long
// windows. 0 means the watchdog is disabled, so any interval is safe.
export function keepaliveFor(holdTimeoutMs: number): number {
  if (holdTimeoutMs <= 0) return MAX_KEEPALIVE_MS
  const third = Math.floor(holdTimeoutMs / 3)
  return Math.min(MAX_KEEPALIVE_MS, Math.max(MIN_KEEPALIVE_MS, third))
}

// keys is the get_layout response's map, action name to KeyboardEvent.code.
// Taking the whole map rather than a bare set of codes is what lets the driver
// recognise the stop key without a second source of truth.
export function createDriver(
  sink: Sink,
  keys: Record<string, string>,
  keepaliveMs: number,
): Driver {
  const codes = new Set(Object.values(keys))
  const stopCode = keys.stop
  const held = new Set<string>()
  let timer: ReturnType<typeof setInterval> | undefined

  // Typing into the settings bar must not drive the robot.
  const inSettings = (e: KeyboardEvent) =>
    e.target instanceof Element && e.target.closest('[data-settings]') !== null

  const releaseAll = () => {
    for (const code of held) sink.release(code)
    held.clear()
  }

  return {
    held,

    handleKeyDown(e) {
      if (!codes.has(e.code) || inSettings(e)) return
      e.preventDefault()
      if (e.repeat || held.has(e.code)) return
      // Mirrors the module: EStop zeroes everything, then re-inserts itself.
      // No releases go out, because the module already emitted them.
      if (e.code === stopCode) held.clear()
      held.add(e.code)
      sink.press(e.code)
    },

    handleKeyUp(e) {
      if (!codes.has(e.code)) return
      // The release runs even from inside the settings bar, so a key pressed
      // before focus moved is still released. preventDefault is skipped there
      // so Space still activates a focused control.
      if (!inSettings(e)) e.preventDefault()
      // Unconditional: a release for a key the module is not holding is a
      // no-op server-side, and sending it anyway recovers from a lost press
      // or from the EStop clear above.
      held.delete(e.code)
      sink.release(e.code)
    },

    releaseAll,

    start() {
      if (timer !== undefined) return
      timer = setInterval(() => {
        for (const code of held) sink.hold(code)
      }, keepaliveMs)
    },

    stop() {
      if (timer !== undefined) {
        clearInterval(timer)
        timer = undefined
      }
      releaseAll()
    },
  }
}
```

- [ ] **Step 4: Run the tests, verify they pass**

Run: `cd frontend && npm test`
Expected: PASS, 14 tests.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/driver.ts frontend/src/lib/driver.test.ts
git commit -m "feat: add the browser keyboard driver"
```

---

## Task 4: `lib/machine.ts` and `lib/settings.ts`

**Files:**
- Create: `frontend/src/lib/machine.ts`, `frontend/src/lib/settings.ts`
- Test: `frontend/src/lib/settings.test.ts`

- [ ] **Step 1: Write `machine.ts`**

Lift it from `/Users/nick.hehr/src/teach-frames/frontend/src/lib/machine.ts`, dropping the Svelte context helpers (this app has one consumer, `App.svelte`). Keep `currentMachine()`: machine id is `window.location.pathname.split('/')[2]`, credentials come from a JSON cookie keyed by that id, signaling address is `https://app.viam.com:443`, and both missing cases throw with a message worth showing the user.

Two deliberate divergences from the original, both because Task 5 renders whatever this throws as the entire page:

- **Validate the parsed cookie's shape.** `as MachineCookie` is a compile-time assertion only, so a missing or non-string `hostname` lets `currentMachine()` return successfully with `host: undefined`, and the failure resurfaces at `createRobotClient` — outside the try/catch that wraps this call. Check `hostname` is a string and `credentials` a non-null object, and throw the malformed-cookie error if not.
- **Make the three throw messages actionable**, each naming a distinct remedy: not opened from the app URL (keep the `/machine/{id}/...` shape hint); not signed in for this machine; stored session damaged, clear site data. Today two of them differ by a single word and none says what to do.

Note both in the file's header comment, so the next person diffing against `teach-frames` is not surprised.

No test — the remaining logic is a cookie read, and `viam module local-app-testing` exercises it for real in Task 8.

- [ ] **Step 2: Write the failing test for `settings.ts`**

`frontend/src/lib/settings.test.ts`:

```ts
import { beforeEach, describe, expect, it } from 'vitest'
import { load, resolve, save } from './settings'

describe('settings', () => {
  beforeEach(() => localStorage.clear())

  it('round-trips a selection per machine', () => {
    save('machine-a', { controller: 'keyboard', camera: 'cam' })
    expect(load('machine-a')).toEqual({ controller: 'keyboard', camera: 'cam' })
    expect(load('machine-b')).toBeNull()
  })

  it('survives absent and corrupt storage', () => {
    expect(load('nope')).toBeNull()
    localStorage.setItem('keyboard-teleop:bad', '{not json')
    expect(load('bad')).toBeNull()
  })

  // The typeof guards reject junk *shapes* inside otherwise-valid JSON: a
  // schema change in a future version, or a hand-edited entry. JSON.parse
  // succeeds on these, so the catch block above never sees them.
  it('rejects wrong-typed values inside valid JSON', () => {
    localStorage.setItem(
      'keyboard-teleop:junk',
      JSON.stringify({ controller: 123, camera: {} }),
    )
    expect(load('junk')).toEqual({ controller: null, camera: null })
  })

  it('falls back to the first available name when the saved one is gone', () => {
    expect(resolve('keyboard', ['keyboard', 'other'])).toBe('keyboard')
    expect(resolve('gone', ['keyboard', 'other'])).toBe('keyboard')
    expect(resolve(null, ['keyboard'])).toBe('keyboard')
    expect(resolve('anything', [])).toBe(null)
  })
})
```

- [ ] **Step 3: Run it, verify it fails**

Run: `cd frontend && npm test`
Expected: FAIL — cannot resolve `./settings`.

- [ ] **Step 4: Implement `settings.ts`**

```ts
// Which controller and camera this browser last used, per machine. Everything
// here tolerates storage being unavailable or holding junk: a lost preference
// is a shrug, a thrown exception at startup is a blank page.

export interface Selection {
  controller: string | null
  camera: string | null
}

const keyFor = (machineId: string) => `keyboard-teleop:${machineId}`

// load returns null when there is no usable record for this machine, which is
// deliberately distinct from a record whose camera is null. Task 5 needs that
// difference: without it, "no camera" is indistinguishable from "never chose",
// and the default-to-first rule resurrects the camera on every reload.
export function load(machineId: string): Selection | null {
  try {
    const raw = localStorage.getItem(keyFor(machineId))
    if (!raw) return null
    const parsed = JSON.parse(raw) as Partial<Selection>
    return {
      controller: typeof parsed.controller === 'string' ? parsed.controller : null,
      camera: typeof parsed.camera === 'string' ? parsed.camera : null,
    }
  } catch {
    return null
  }
}

export function save(machineId: string, selection: Selection): void {
  try {
    localStorage.setItem(keyFor(machineId), JSON.stringify(selection))
  } catch {
    // Private browsing, quota, disabled storage. Not worth surfacing.
  }
}

// resolve picks the name to use: the saved one if the machine still has it,
// otherwise the first available, otherwise nothing.
export function resolve(saved: string | null, available: string[]): string | null {
  if (saved !== null && available.includes(saved)) return saved
  return available[0] ?? null
}
```

- [ ] **Step 5: Run the tests, verify they pass**

Run: `cd frontend && npm test`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/lib/machine.ts frontend/src/lib/settings.ts frontend/src/lib/settings.test.ts
git commit -m "feat: add machine identity and per-machine selection storage"
```

---

## Task 5: App shell, settings bar, capture wiring

The first task that produces something runnable end to end. No new unit tests — this is wiring over tested parts, and Task 8's manual pass covers it.

**Files:**
- Modify: `frontend/src/App.svelte`
- Create: `frontend/src/panels/SettingsBar.svelte`

`app.css` needs nothing: the scaffold's `#app` flex column already carries the shell layout.

- [ ] **Step 1: Write `SettingsBar.svelte`**

Presentational only. Props: `controllers: string[]`, `cameras: string[]`, and bindable `controller`/`camera`. Its root element carries `data-settings` — the attribute the driver keys off, so it must be on the outermost node, not an inner wrapper. Style the bar from its own scoped `<style>` using that same attribute selector rather than inventing a class: one token, so an element that loses the attribute loses its styling too and the break is visible instead of silent. The camera select includes a `none` option whose value is `null`.

- [ ] **Step 2: Wire `App.svelte`**

In order:

1. `currentMachine()` in a `try`/`catch`. On throw, render the message as the whole page and stop.
2. `createRobotClient(dialConf)`, then `machine.resourceNames()`; filter to `subtype === 'input_controller'` and `subtype === 'camera'`, mapping to `.name`. Hold the client in `$state`, not a plain `let` — the arming effect reads it, and an untracked `let` makes arming depend on the accident that the client is assigned before the `controller` write that schedules the re-run. Reordering those two lines would silently stop the app arming at all, with nothing for a build or typecheck to catch.
3. `load(machineId)` once. It returns `null` when this browser has no record, which is what separates "never chose" from "chose no camera":
   - **controller**: `resolve(saved?.controller ?? null, controllers)`. When `resolve` falls back because the saved name is gone, say so in the status line — "`<saved>` is no longer on this machine, using `<first>`". This matters more than it looks: the `get_layout` probe catches a substitute that is not a keyboard component, but a machine with two of them (`keyboard-base`, `keyboard-arm`) probes clean and the app silently arms the wrong one. The user presses W and a different subsystem moves.
   - **camera**: honour a record verbatim, including `camera: null` meaning none. Only default to the first camera when `load` returned `null`. Do not run `resolve` on it.
   - **saving**: write both fields from a single `$effect` over both values, never per-select-handler. `save` takes a whole `Selection`, so a handler that writes one field wipes the other. Only start persisting once the user actually changes a select (an `onchange` prop on `SettingsBar`), never right after applying defaults — otherwise a machine with no cameras at first load persists `camera: null`, which permanently reads as "chose none" and stops a camera added later from ever being defaulted to.
4. `$effect` on the selected controller: build an `InputControllerClient`, call `doCommand({ get_layout: true })`. On success store `{layout, keys, hold_timeout_ms}` and arm. On failure set the error state whose text names both causes — not a `devrel:keyboard:input`, or a module too old to have `get_layout`.
5. When armed, build the sink and the driver:

```ts
// Time is omitted on purpose: the module ignores client clocks.
const send = (control: string, event: string, value: number) =>
  controller.triggerEvent({ control, event, value }).catch(reportOnce)
const sink: Sink = {
  press: (code) => send(code, 'ButtonPress', 1),
  release: (code) => send(code, 'ButtonRelease', 0),
  hold: (code) => send(code, 'ButtonHold', 1),
}
const driver = createDriver(sink, keys, keepaliveFor(holdTimeoutMs))
driver.start()
```

6. Disconnect handling, which is APP_SPEC's "Connection lost" row. **Use the
   events the SDK actually emits, not the ones its enum declares.** Read the
   emit sites in `@viamrobotics/sdk/dist/main.es.js`, not `events.d.ts`:
   `Client.onDisconnect()` emits `DISCONNECTED` only when `noReconnect` is set
   or the client is already `closed`, neither of which is true here — a real
   mid-session drop takes the default path and emits **`RECONNECTING`**, and
   when backoff gives up, **`RECONNECTION_FAILED`** (terminal; say so, the
   remedy is a reload). Wire those to `releaseAll()` + disarm + a status line.
   `DISCONNECTING`/`DISCONNECTED` are worth keeping for the explicit-close
   case. `CONNECTED` is the correct re-arm signal and cannot fire spuriously on
   the initial connection, because it is emitted inside `connect()` before
   `createRobotClient` resolves.

   Re-arm by re-running the effect (bump an `armGeneration` `$state` the effect
   reads), not by reusing the captured `get_layout` response: a component
   reconfigured while disconnected would otherwise keep the old keymap *and the
   old keepalive interval*, and a now-shorter `hold_timeout_ms` means the
   watchdog releases keys that are still physically down.

   `releaseAll()` clears the driver's local `held`, so the legend stops lying.
   Its release calls will reject on a dead connection — gate `reportOnce` on
   `armed` so those rejections do not overwrite the very "connection lost"
   message they accompany.
7. Window listeners: `keydown`/`keyup` to the driver, `blur` and `beforeunload` to `releaseAll`, `document` `visibilitychange` to `releaseAll` when `document.hidden`. Return a teardown from the `$effect` that removes them and calls `driver.stop()`, so re-selecting a controller releases under the old keymap before arming the new one.
8. `heldCodes = [...driver.held]` as `$state`, for the legend highlight — the
   driver's `held` is a plain `Set` and Svelte does not track it. There are five
   call sites that mutate it (keydown, keyup, releaseAll, disconnect, teardown)
   and forgetting one gives a silently stale legend with no test to catch it, so
   wrap rather than remember: `const sync = (f) => (...a) => { f(...a); heldCodes = [...driver.held] }`,
   and register the wrapped handlers.
9. `reportOnce` keeps the last error message and ignores repeats, so a stuck key cannot spam the status line.

- [ ] **Step 3: Verify it builds and type-checks**

Run: `cd frontend && npm run build && npm run check`
Expected: both exit 0.

- [ ] **Step 4: Commit**

```bash
git add frontend/src
git commit -m "feat: connect the app to the machine and arm keyboard capture"
```

---

## Task 6: Key legend

**Files:**
- Create: `frontend/src/panels/KeyLegend.svelte`
- Modify: `frontend/src/App.svelte`

- [ ] **Step 1: Write the component**

Props: `keys: Record<string, string>`, `held: string[]`. Renders rows in a fixed display order:

```ts
const ORDER = ['forward', 'back', 'left', 'right', 'z_up', 'z_down', 'gripper_open', 'gripper_close', 'stop']
const LABELS: Record<string, string> = {
  forward: 'Forward', back: 'Back', left: 'Left', right: 'Right',
  z_up: 'Up', z_down: 'Down',
  gripper_open: 'Open gripper', gripper_close: 'Close gripper', stop: 'Stop',
}
```

Any action in `keys` that is not in `ORDER` renders last with its raw name as the label, so a module that adds an action degrades rather than hides it. A row whose code is in `held` gets the `.held` class, defined in the component's own scoped `<style>` — `app.css` carries only the base `kbd` rule.

- [ ] **Step 2: Render it from `App.svelte`** when armed, passing `heldCodes`. Set `layout = null` alongside `armed = false` in the effect's disarm branch, so the legend can never show a keymap for a controller that is not armed.

- [ ] **Step 3: Verify**

Run: `cd frontend && npm run build && npm run check`
Expected: both exit 0.

- [ ] **Step 4: Commit**

```bash
git add frontend/src
git commit -m "feat: render the key legend from the component's layout"
```

---

## Task 7: Camera view

**Files:**
- Create: `frontend/src/panels/CameraView.svelte`
- Modify: `frontend/src/App.svelte`

- [ ] **Step 1: Write the component**

Props: `client: RobotClient`, `name: string | null`. An `$effect` keyed on `name`:

- `null` → render a placeholder, start nothing. Give the placeholder and the
  `<video>` the same root element (or make both top-level siblings with no
  wrapper) and size it from CameraView's own scoped `<style>`. A wrapper that
  appears in only one branch collapses the video to zero height, which reads as
  a stream bug rather than a layout one.
- otherwise `new StreamClient(client)` → `getStream(name)` → assign to the `<video bind:this>`'s `srcObject`. The element is `autoplay muted playsinline`; without `muted` the browser blocks autoplay.
- Teardown (effect cleanup) stops every track and clears `srcObject`.
- A `visibilitychange` listener tears down on hidden and re-acquires on visible. **Do not skip the re-acquire** — teardown alone leaves the video permanently black after the first tab switch.

Check whether the installed `@viamrobotics/sdk` exposes a helper that already owns this lifecycle; if it does, prefer it over hand-rolling `StreamClient`.

- [ ] **Step 2: Render it from `App.svelte`**, filling the space under the settings bar.

- [ ] **Step 3: Verify**

Run: `cd frontend && npm run build && npm run check`
Expected: both exit 0.

- [ ] **Step 4: Commit**

```bash
git add frontend/src
git commit -m "feat: show a camera stream alongside the keys"
```

---

## Task 8: Ship it — build plumbing and doc reconciliation

**Files:**
- Create: `setup.sh`, `build.sh`
- Modify: `Makefile`, `meta.json`, `README.md`, `docs/SPEC.md`
- Delete: `examples/web/index.html`

- [ ] **Step 1: Add `setup.sh` and `build.sh`**

Copy `/Users/nick.hehr/src/so-101/setup.sh` and `build.sh`, dropping the pnpm half of setup (this app uses npm). `setup.sh` installs mise if absent, `mise use -g node@22`, then `make setup`. `build.sh` puts `$HOME/.local/bin` on `PATH`, `eval "$(mise activate bash)"`, then `make module.tar.gz`. Both `chmod +x`.

The two scripts exist because the cloud build runs the build step in a fresh shell where mise is not activated.

- [ ] **Step 2: Update the `Makefile`**

```make
frontend:
	cd frontend && npm ci && npm run build

test:
	go test ./...
	cd frontend && npm test
```

The Makefile has no `.PHONY` line today, so add one — without it, the
`frontend` target never runs, because the `frontend/` directory next to it
already satisfies make:

```make
.PHONY: all module lint test setup frontend
```

Make `module.tar.gz` depend on `frontend`, and add `frontend/dist` to `TAR_FILES`.

Keep `npm test` out of the cloud-build path. `build.sh` runs `make module.tar.gz`,
which must depend on `frontend` only — not on `test` — so Viam's builder never
installs jsdom and vitest just to produce a tarball. The `test` target stays for
local and CI use, exactly as the Go `test` target is wired today. Leave `setup:` alone — `frontend:` already runs `npm ci`, and adding it to `setup:` would install twice in the cloud build.

- [ ] **Step 3: Update `meta.json`**

Add the `applications` array from `docs/APP_SPEC.md` "Application registration" (replacing `"applications": null`), and switch `build.setup` to `"./setup.sh"` and `build.build` to `"./build.sh"`.

- [ ] **Step 4: Verify the tarball**

Run: `make module.tar.gz && tar tzf module.tar.gz | head -20`
Expected: contains `meta.json`, `bin/keyboard`, and `frontend/dist/index.html`.

- [ ] **Step 5: Reconcile the docs**

Delete `examples/web/index.html` (and `examples/` if it is then empty). Then, per `docs/APP_SPEC.md` "Removals":

- `docs/SPEC.md:276` — replace `DoCommand returns resource.ErrDoUnimplemented` with the `get_layout` command, pointing at `docs/APP_SPEC.md`.
- `docs/SPEC.md:338-340` — the "Ships as `examples/web/index.html`…" paragraph becomes a pointer to `frontend/`.
- `docs/SPEC.md:286` and `:327` — note that the 200ms keepalive assumes the default `hold_timeout_ms` and that a client should scale it to the configured window.
- `docs/SPEC.md:351` — Files table: drop the `examples/web/index.html` row, add `frontend/` and `docs/APP_SPEC.md`.
- `docs/SPEC.md:390` — manual test 3 retargets to the app.
- `README.md` — "Web keyboard" points at the app; the `DoCommand` section documents `get_layout` instead of "Not implemented"; the line "so the page's layout selector must match the module's `layout` attribute" becomes a note that the app reads the layout automatically.

Line numbers are from before any edits; re-check them as you go.

- [ ] **Step 6: Manual verification against a real machine**

Per `docs/APP_SPEC.md` "Testing":

1. **Open the published app URL first**, before anything else. `vite.config.ts`
   sets `base: './'`, so `dist/index.html` asks for `./assets/…`. Against
   `/machine/{id}/` that resolves correctly; against `/machine/{id}` with no
   trailing slash it resolves one segment too high and you get a blank page
   with no error text. `machine.ts`'s path parse works either way, so nothing
   warns you. Everything below is untestable if this fails, which is why it is
   step 1 and not step 7. If it does fail, check what the app host actually
   serves before changing `base` — `local-app-testing` and the published host
   may differ.
2. `viam module local-app-testing` against a machine with a
   `devrel:keyboard:input` and a camera. Confirm it connects, the legend
   matches the configured layout, and held keys highlight.
3. Hold W, kill the tab → `AbsoluteHat0Y` returns to 0. Allow up to **~1.5×
   `hold_timeout_ms`**, not 1×: the watchdog ticks at `holdTimeout/2` and
   expires on `>` the window, so the worst case is 750ms at the default. The
   `beforeunload` release often beats it, but that is best-effort by design.
4. Hold W, alt-tab away → release is immediate, not watchdog-delayed.
5. Point the controller select at a non-keyboard `input_controller` → the
   two-cause error, no events sent.
6. Tab away and back → the camera comes back.
7. Configure `hold_timeout_ms: 100`, hold W for several seconds → the axis
   holds at -1 without flickering.
8. **Drop the connection mid-session** — hold W, then kill wifi. Confirm the
   status line stops claiming armed and the keys release. This one cannot be
   trusted from a code read: the plan originally named the wrong SDK events
   here (the enum declares `DISCONNECTED`, but a live drop emits
   `RECONNECTING`), so the whole row was dead code until a review caught it.
9. **Let it reconnect after step 8 and confirm the video comes back**, not just
   the keys. `CameraView` and the arming effect recover independently, and a
   cross-task review found the camera had no reconnect path at all — this step
   exists to prove the fix.

Record the results. Do not claim this task done on a build alone: steps 3, 4 and 7 are the whole safety argument and no unit test covers any of them.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat: bundle the keyboard-teleop application in the module"
```
