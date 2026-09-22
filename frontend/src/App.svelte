<script lang="ts">
  import {
    createRobotClient,
    InputControllerClient,
    MachineConnectionEvent,
    type RobotClient,
  } from '@viamrobotics/sdk'
  import { currentMachine, type MachineIdentity } from './lib/machine'
  import { load, resolve, save } from './lib/settings'
  import { createDriver, keepaliveFor, type Sink } from './lib/driver'
  import SettingsBar from './panels/SettingsBar.svelte'
  // Task 6 adds <KeyLegend keys={layout?.keys} held={heldCodes} /> here.
  // Task 7 adds <CameraView client={robotClient} name={camera} /> here.

  interface Layout {
    layout: string
    keys: Record<string, string>
    holdTimeoutMs: number
  }

  let fatalError = $state<string | null>(null)
  let status = $state('connecting…')
  let controllers = $state<string[]>([])
  let cameras = $state<string[]>([])
  let controller = $state<string | null>(null)
  let camera = $state<string | null>(null)
  let layout = $state<Layout | null>(null)
  let armed = $state(false)
  // The driver's `held` is a plain Set; Svelte doesn't track it, so this is
  // re-derived after every call that can mutate it. Task 6 reads this for the
  // legend highlight.
  let heldCodes = $state<string[]>([])

  let robotClient: RobotClient | null = null
  let settingsReady = false

  let lastError: string | null = null
  function reportOnce(err: unknown) {
    const message = err instanceof Error ? err.message : String(err)
    if (message === lastError) return
    lastError = message
    status = message
  }

  let identity: MachineIdentity | null = null
  try {
    identity = currentMachine()
  } catch (err) {
    fatalError = err instanceof Error ? err.message : String(err)
  }

  // Runs once: connect, discover resources, resolve the saved selection.
  // No cleanup is needed — App.svelte is the app's root and lives for the
  // whole session, so there is nothing to cancel or tear down here.
  async function connect(id: MachineIdentity) {
    try {
      const client = await createRobotClient(id.dialConf)
      robotClient = client
      const names = await client.resourceNames()
      controllers = names.filter((n) => n.subtype === 'input_controller').map((n) => n.name)
      cameras = names.filter((n) => n.subtype === 'camera').map((n) => n.name)

      const saved = load(id.id)
      const resolvedController = resolve(saved?.controller ?? null, controllers)
      status =
        controllers.length === 0 ? 'no input_controller found on this machine' : 'connected'
      if (saved?.controller && saved.controller !== resolvedController) {
        status = `"${saved.controller}" is no longer on this machine, using "${resolvedController ?? 'none'}"`
      }
      controller = resolvedController
      // Honoured verbatim, including null meaning "no camera" — only ever
      // defaulted when there was no record at all for this machine.
      camera = saved === null ? (cameras[0] ?? null) : saved.camera
      settingsReady = true
    } catch (err) {
      status = err instanceof Error ? err.message : String(err)
    }
  }
  if (identity) connect(identity)

  // Persist both fields together; `save` replaces the whole Selection, so
  // writing from separate per-select handlers would let one wipe the other.
  $effect(() => {
    const c = controller
    const cam = camera
    if (!identity || !settingsReady) return
    save(identity.id, { controller: c, camera: cam })
  })

  // Arms/disarms capture for whichever controller is selected. Re-running on
  // a new `controller` first tears down the previous run (window listeners,
  // connection listeners, the driver's keepalive), so a switch releases under
  // the old keymap before the new one arms.
  $effect(() => {
    const selected = controller
    const client = robotClient
    if (!selected || !client) {
      armed = false
      return
    }
    // Reset immediately: the async get_layout round-trip below must not
    // leave `armed` showing the previous controller's state in the meantime.
    armed = false
    let cancelled = false
    let cleanup: (() => void) | undefined

    void (async () => {
      try {
        const controllerClient = new InputControllerClient(client, selected)
        const res = (await controllerClient.doCommand({ get_layout: true })) as {
          layout: string
          hold_timeout_ms: number
          keys: Record<string, string>
        }
        if (cancelled) return

        layout = { layout: res.layout, keys: res.keys, holdTimeoutMs: res.hold_timeout_ms }

        // Time is omitted on purpose: the module ignores client clocks.
        const send = (control: string, event: string, value: number) =>
          controllerClient.triggerEvent({ control, event, value }).catch(reportOnce)
        const sink: Sink = {
          press: (code) => send(code, 'ButtonPress', 1),
          release: (code) => send(code, 'ButtonRelease', 0),
          hold: (code) => send(code, 'ButtonHold', 1),
        }
        const driver = createDriver(sink, res.keys, keepaliveFor(res.hold_timeout_ms))

        // Five call sites mutate driver.held: keydown, keyup, releaseAll,
        // disconnect and teardown below. Wrapping rather than remembering to
        // re-sync each one is what keeps the (future) legend from going
        // silently stale.
        const sync =
          <A extends unknown[]>(fn: (...args: A) => void) =>
          (...args: A) => {
            fn(...args)
            heldCodes = [...driver.held]
          }
        const onKeyDown = sync((e: KeyboardEvent) => driver.handleKeyDown(e))
        const onKeyUp = sync((e: KeyboardEvent) => driver.handleKeyUp(e))
        const onReleaseAll = sync(() => driver.releaseAll())
        const onVisibilityChange = () => {
          if (document.hidden) onReleaseAll()
        }

        window.addEventListener('keydown', onKeyDown)
        window.addEventListener('keyup', onKeyUp)
        window.addEventListener('blur', onReleaseAll)
        window.addEventListener('beforeunload', onReleaseAll)
        document.addEventListener('visibilitychange', onVisibilityChange)

        // The server-side watchdog is the real safety net; this pair only
        // keeps the UI honest about whether capture is actually live. The
        // connection is already gone by DISCONNECTING/DISCONNECTED, so the
        // releaseAll below sends triggerEvent calls that reject — reportOnce
        // swallows them, which is expected here, not a bug.
        const onDisconnected = () => {
          onReleaseAll()
          armed = false
          status = 'connection lost — capture paused (the module watchdog still releases keys)'
        }
        const onReconnected = () => {
          armed = true
          status = `armed — layout "${res.layout}"`
          driver.start()
        }
        client.on(MachineConnectionEvent.DISCONNECTING, onDisconnected)
        client.on(MachineConnectionEvent.DISCONNECTED, onDisconnected)
        client.on(MachineConnectionEvent.CONNECTED, onReconnected)

        driver.start()
        armed = true
        status = `armed — layout "${res.layout}"`

        cleanup = sync(() => {
          window.removeEventListener('keydown', onKeyDown)
          window.removeEventListener('keyup', onKeyUp)
          window.removeEventListener('blur', onReleaseAll)
          window.removeEventListener('beforeunload', onReleaseAll)
          document.removeEventListener('visibilitychange', onVisibilityChange)
          client.off(MachineConnectionEvent.DISCONNECTING, onDisconnected)
          client.off(MachineConnectionEvent.DISCONNECTED, onDisconnected)
          client.off(MachineConnectionEvent.CONNECTED, onReconnected)
          driver.stop()
        })
      } catch {
        if (cancelled) return
        armed = false
        layout = null
        status = `"${selected}" is not a devrel:keyboard:input, or an older version of the module that predates get_layout`
      }
    })()

    return () => {
      cancelled = true
      cleanup?.()
    }
  })
</script>

{#if fatalError}
  <main class="fatal">{fatalError}</main>
{:else}
  <SettingsBar {controllers} {cameras} bind:controller bind:camera />
  <p class="status">{status}</p>
{/if}

<style>
  .fatal {
    display: flex;
    align-items: center;
    justify-content: center;
    height: 100%;
    padding: 2rem;
    text-align: center;
  }

  .status {
    margin: 0;
    padding: 0.5rem 1rem;
    font-size: 0.9rem;
    color: #aaa;
  }
</style>
