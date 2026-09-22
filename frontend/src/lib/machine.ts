// Started as a lift from teach-frames' frontend/src/lib/machine.ts. Dropped
// the Svelte context helpers (provideMachineId/useMachineId) since this app
// has one consumer, App.svelte. Also diverges by validating the cookie's
// shape after parsing (see the check in currentMachine below) and by using
// user-facing, remedy-specific throw messages instead of developer strings —
// Task 5 renders whichever one fires as the entire page.
import Cookies from 'js-cookie'
import type { DialConf, Credentials } from '@viamrobotics/sdk'

export interface MachineIdentity {
  id: string
  dialConf: DialConf
}

interface MachineCookie {
  hostname: string
  credentials: Credentials
  machineId: string
}

// The Viam browser SDK connects over WebRTC through the app.viam.com
// signaling server. This is the standard address for Viam Applications.
const SIGNALING_ADDRESS = 'https://app.viam.com:443'

// Viam Applications (single_machine) inject a browser cookie keyed by the
// machine id, which is the third URL path segment: /machine/{id}/...  `viam
// module local-app-testing` injects the same cookie in dev.
export function currentMachine(): MachineIdentity {
  const id = window.location.pathname.split('/')[2]
  if (!id) {
    throw new Error(
      'this page must be opened from its Viam application URL (expected /machine/{id}/...)',
    )
  }
  const raw = Cookies.get(id)
  if (!raw) {
    throw new Error(
      `not signed in for machine ${id} — re-open this app from app.viam.com`,
    )
  }
  let cookie: MachineCookie
  try {
    cookie = JSON.parse(raw) as MachineCookie
  } catch {
    throw new Error(
      `the stored session for machine ${id} is damaged — clear site data for this page and re-open it from app.viam.com`,
    )
  }
  // JSON.parse plus `as MachineCookie` only asserts the shape at compile
  // time; a cookie missing or mistyping a field parses fine and would
  // otherwise flow through as a silently broken DialConf (e.g. `host:
  // undefined`), surfacing later as an opaque SDK connection error instead
  // of this message.
  if (typeof cookie.hostname !== 'string' || typeof cookie.credentials !== 'object' || cookie.credentials === null) {
    throw new Error(
      `the stored session for machine ${id} is damaged — clear site data for this page and re-open it from app.viam.com`,
    )
  }
  return {
    id,
    dialConf: {
      host: cookie.hostname,
      credentials: cookie.credentials,
      signalingAddress: SIGNALING_ADDRESS,
    },
  }
}
