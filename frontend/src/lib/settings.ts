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
