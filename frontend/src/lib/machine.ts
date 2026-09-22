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
    throw new Error('no machine id in URL path (expected /machine/{id}/...)')
  }
  const raw = Cookies.get(id)
  if (!raw) {
    throw new Error(`no Viam credentials cookie for machine ${id}`)
  }
  let cookie: MachineCookie
  try {
    cookie = JSON.parse(raw) as MachineCookie
  } catch {
    throw new Error(`no valid Viam credentials cookie for machine ${id}`)
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
