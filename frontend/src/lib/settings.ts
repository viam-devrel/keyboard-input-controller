// Which controller and camera this browser last used, per machine. Everything
// here tolerates storage being unavailable or holding junk: a lost preference
// is a shrug, a thrown exception at startup is a blank page.

export interface Selection {
  controller: string | null
  camera: string | null
}

const EMPTY: Selection = { controller: null, camera: null }
const keyFor = (machineId: string) => `keyboard-teleop:${machineId}`

export function load(machineId: string): Selection {
  try {
    const raw = localStorage.getItem(keyFor(machineId))
    if (!raw) return { ...EMPTY }
    const parsed = JSON.parse(raw) as Partial<Selection>
    return {
      controller: typeof parsed.controller === 'string' ? parsed.controller : null,
      camera: typeof parsed.camera === 'string' ? parsed.camera : null,
    }
  } catch {
    return { ...EMPTY }
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
