<script lang="ts">
  import {
    createRobotClient,
    InputControllerClient,
    MachineConnectionEvent,
    type RobotClient,
  } from '@viamrobotics/sdk'
  import { currentMachine, type MachineIdentity } from './lib/machine'
  import { load, resolve, save } from './lib/settings'
  import { createDriver, keepaliveFor, type LayoutAction, type Sink } from './lib/driver'
  import SettingsBar from './panels/SettingsBar.svelte'
  import KeyLegend from './panels/KeyLegend.svelte'
  import CameraView from './panels/CameraView.svelte'

  interface Layout {
    layout: string
    actions: LayoutAction[]
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
  // Bumped by the CONNECTED (reconnect) handler to force the arm effect to
  // tear down and re-run: re-arming in place against a stale get_layout
  // response would keep an outdated keepalive interval that may no longer fit
  // the module's current hold_timeout_ms.
  let armGeneration = $state(0)
  // The driver's `held` is a plain Set; Svelte doesn't track it, so this is
  // re-derived after every call that can mutate it. Task 6 reads this for the
  // legend highlight.
  let heldCodes = $state<string[]>([])

  // $state, not a plain let: the arm effect below reads this to know when to
  // start, and must react to it directly rather than depend on assignment
  // order with `controller`: as a plain variable this only worked because
  // `robotClient = client` happened to precede the `controller` write in
  // connect() below. A RobotClient instance is a class, not a plain
  // object/array, so Svelte's state proxy returns it unwrapped; only the
  // variable reference becomes reactive.
  let robotClient = $state<RobotClient | null>(null)
  // Set to true only by a real user edit in SettingsBar's onchange callback,
  // never by applying defaults in connect() — otherwise a machine with no
  // cameras at first load would permanently persist "chose none" instead of
  // leaving no record, and a camera added later would never get defaulted to.
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

      lastError = null
      const saved = load(id.id)
      const resolvedController = resolve(saved?.controller ?? null, controllers)
      status =
        controllers.length === 0
          ? 'no input_controller found on this machine — configure one, then reload this page'
          : 'connected'
      if (saved?.controller && saved.controller !== resolvedController) {
        status = `"${saved.controller}" is no longer on this machine, using "${resolvedController ?? 'none'}"`
      }
      controller = resolvedController
      // Honoured verbatim, including null meaning "no camera" — only ever
      // defaulted when there was no record at all for this machine. This is
      // an application of a default, not a user choice, so it deliberately
      // does not set settingsReady (see its declaration above).
      if (saved === null) {
        camera = cameras[0] ?? null
      } else if (saved.camera !== null && !cameras.includes(saved.camera)) {
        // Falls back to null, not cameras[0]: defaulting to the first camera
        // would resurrect one the user deliberately set to "none".
        camera = null
        status += ` — camera "${saved.camera}" is no longer on this machine, using "none"`
      } else {
        camera = saved.camera
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err)
      status = `could not connect: ${message}`
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
    // Read so a bump from the CONNECTED handler below forces this whole
    // effect to tear down and re-run — see armGeneration's declaration.
    armGeneration
    if (!selected || !client) {
      armed = false
      layout = null
      return
    }
    // Reset immediately: the async get_layout round-trip below must not
    // leave `armed` showing the previous controller's state in the meantime.
    armed = false
    status = `probing "${selected}"…`
    let cancelled = false
    let cleanup: (() => void) | undefined

    void (async () => {
      try {
        const controllerClient = new InputControllerClient(client, selected)
        const res = (await controllerClient.doCommand({ get_layout: true })) as unknown as {
          layout: string
          hold_timeout_ms: number
          actions: LayoutAction[]
        }
        if (cancelled) return

        layout = { layout: res.layout, actions: res.actions, holdTimeoutMs: res.hold_timeout_ms }

        // Time is omitted on purpose: the module ignores client clocks.
        // Gated on `armed`: once disconnect or teardown has disarmed capture,
        // the release calls those paths trigger are expected to reject (the
        // connection is already gone), and reporting those rejections would
        // overwrite the very "connection lost" status line they're a
        // consequence of, at the exact moment a stuck key matters most.
        // `armed` is always set to false synchronously before these
        // rejections can land (see onDisconnected and the effect's own
        // teardown/re-entry), so reading it here is safe without creating a
        // reactive dependency.
        const send = (control: string, event: string, value: number) =>
          controllerClient.triggerEvent({ control, event, value }).catch((err) => {
            if (armed) reportOnce(err)
          })
        const sink: Sink = {
          press: (code) => send(code, 'ButtonPress', 1),
          release: (code) => send(code, 'ButtonRelease', 0),
          hold: (code) => send(code, 'ButtonHold', 1),
        }
        const driver = createDriver(sink, res.actions, keepaliveFor(res.hold_timeout_ms))

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
        // Best-effort only: a triggerEvent fired from beforeunload may not
        // land before the page actually unloads. Kept anyway because it
        // sometimes lands faster than the watchdog notices the socket is
        // gone; the module's own watchdog remains the actual guarantee.
        window.addEventListener('beforeunload', onReleaseAll)
        document.addEventListener('visibilitychange', onVisibilityChange)

        // The server-side watchdog is the real safety net; this trio only
        // keeps the UI honest about whether capture is actually live.
        // RECONNECTING is what the SDK actually emits on an ordinary dropped
        // connection (confirmed in the installed bundle: RobotClient.onDisconnect
        // emits DISCONNECTED only when `noReconnect` is set or the client is
        // already closed — neither applies here — and emits RECONNECTING
        // otherwise, before it starts retrying). DISCONNECTING/DISCONNECTED
        // are kept too, for an explicit disconnect() call this app never
        // makes today but might in the future. RECONNECTION_FAILED is
        // terminal: the SDK has given up retrying.
        const onDisconnected = () => {
          onReleaseAll()
          armed = false
          status =
            'connection lost — capture paused; reconnecting (the module watchdog still releases keys)'
        }
        const onFailed = () => {
          armed = false
          status = 'reconnection gave up; reload the page'
        }
        // Re-probes get_layout from scratch rather than re-arming this driver
        // in place: the module's hold_timeout_ms may have changed while
        // disconnected, and reusing this closure's stale `res` would keep an
        // out-of-date keepalive interval — exactly the mismatch driver.ts's
        // header calls load-bearing. Bumping armGeneration tears this whole
        // effect down and re-runs it fresh. CONNECTED cannot fire spuriously
        // for the *initial* connection, because it's emitted inside connect()
        // before createRobotClient resolves — long before this listener is
        // attached.
        const onReconnected = () => {
          armGeneration++
        }
        client.on(MachineConnectionEvent.DISCONNECTING, onDisconnected)
        client.on(MachineConnectionEvent.DISCONNECTED, onDisconnected)
        client.on(MachineConnectionEvent.RECONNECTING, onDisconnected)
        client.on(MachineConnectionEvent.RECONNECTION_FAILED, onFailed)
        client.on(MachineConnectionEvent.CONNECTED, onReconnected)

        driver.start()
        armed = true
        lastError = null
        status = `armed — layout "${res.layout}"`

        cleanup = sync(() => {
          window.removeEventListener('keydown', onKeyDown)
          window.removeEventListener('keyup', onKeyUp)
          window.removeEventListener('blur', onReleaseAll)
          window.removeEventListener('beforeunload', onReleaseAll)
          document.removeEventListener('visibilitychange', onVisibilityChange)
          client.off(MachineConnectionEvent.DISCONNECTING, onDisconnected)
          client.off(MachineConnectionEvent.DISCONNECTED, onDisconnected)
          client.off(MachineConnectionEvent.RECONNECTING, onDisconnected)
          client.off(MachineConnectionEvent.RECONNECTION_FAILED, onFailed)
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
  <SettingsBar
    {controllers}
    {cameras}
    bind:controller
    bind:camera
    onchange={() => (settingsReady = true)}
  />
  <p class="status">{status}</p>
  {#if armed && layout}
    <KeyLegend actions={layout.actions} held={heldCodes} />
  {/if}
  {#if robotClient}
    <CameraView client={robotClient} name={camera} />
  {/if}
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
