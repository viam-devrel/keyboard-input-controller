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
    target: opts.target ?? document.body,
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

  it('drives the robot from a focused element outside the settings bar', () => {
    const { sink, calls } = fakeSink()
    const d = createDriver(sink, KEYS, 200)
    settingsTarget()                        // bar exists but is not the target
    d.handleKeyDown(ev('KeyW', { target: document.body }))
    expect(calls).toEqual(['press:KeyW'])
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

  it('releases a key the EStop already cleared from held', () => {
    const { sink, calls } = fakeSink()
    const d = createDriver(sink, KEYS, 200)
    d.handleKeyDown(ev('KeyW'))
    d.handleKeyDown(ev('Space'))
    calls.length = 0
    d.handleKeyUp(ev('KeyW'))
    expect(calls).toEqual(['release:KeyW'])
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
