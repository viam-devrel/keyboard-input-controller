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
  start(): void   // begins the keepalive interval
  stop(): void    // stops it and releases everything
  readonly held: ReadonlySet<string>
}

// The floor is also the fan-out ceiling: at 20ms with a 9-key layout that is
// ~450 fire-and-forget triggerEvent calls per second with no backpressure,
// which is why it is not set any lower.
const MIN_KEEPALIVE_MS = 20
const MAX_KEEPALIVE_MS = 200

// keepaliveFor picks a keepalive interval for a component whose watchdog
// window is holdTimeoutMs. Dividing by three leaves two refreshes inside every
// window; the floor bounds the request rate, the ceiling caps it for long
// windows. 0 means the watchdog is disabled, so any interval is safe.
export function keepaliveFor(holdTimeoutMs: number): number {
  // Same trust boundary settings.ts validates: get_layout's response crosses
  // a version-skew/third-party-module boundary, so `holdTimeoutMs` cannot be
  // trusted to be a positive finite number. NaN/undefined would otherwise
  // propagate through Math.floor/max/min into setInterval, which treats a NaN
  // delay as 0 — an unbounded fire-and-forget triggerEvent hot loop.
  if (!Number.isFinite(holdTimeoutMs) || holdTimeoutMs <= 0) return MAX_KEEPALIVE_MS
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
  // A layout without a `stop` action leaves this undefined and the EStop
  // branch below inert (e.code === undefined never matches a real code) —
  // that is deliberate, not a bug to "fix" into a loose compare.
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
      if (!codes.has(e.code)) return   // unmapped: leave the browser alone
      if (inSettings(e)) return        // typing in the settings bar must not drive the robot
      e.preventDefault()               // after the settings guard: see handleKeyUp
      // Before the EStop clear below: after an EStop, the OS keeps autorepeating a
      // physically-down key that is no longer in `held`, and this is the only guard
      // left to stop it re-pressing the axis the EStop just zeroed.
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
      // no-op server-side, and sending it anyway covers the EStop clear above
      // and a driver rebuilt on controller change, whose predecessor's stop()
      // releases failed and left the server holding keys this driver's empty
      // `held` knows nothing about.
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
