import { beforeEach, describe, expect, it } from 'vitest'
import { load, resolve, save } from './settings'

describe('settings', () => {
  beforeEach(() => localStorage.clear())

  it('round-trips a selection per machine', () => {
    save('machine-a', { controller: 'keyboard', camera: 'cam' })
    expect(load('machine-a')).toEqual({ controller: 'keyboard', camera: 'cam' })
    expect(load('machine-b')).toEqual({ controller: null, camera: null })
  })

  it('survives absent and corrupt storage', () => {
    expect(load('nope')).toEqual({ controller: null, camera: null })
    localStorage.setItem('keyboard-teleop:bad', '{not json')
    expect(load('bad')).toEqual({ controller: null, camera: null })
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
