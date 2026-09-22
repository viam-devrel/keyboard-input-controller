<script lang="ts">
  import { MachineConnectionEvent, StreamClient, type RobotClient } from '@viamrobotics/sdk'
  import { untrack } from 'svelte'

  let { client, name }: { client: RobotClient; name: string | null } = $props()

  let videoEl = $state<HTMLVideoElement>()

  // Depends only on `name`: `client` is read through `untrack` so a stable
  // connection identity (the normal case) doesn't force a teardown/reacquire
  // of the stream. Each run still uses whichever client is current at that
  // moment.
  $effect(() => {
    const selected = name
    if (!selected) return

    const currentClient = untrack(() => client)
    const streamClient = new StreamClient(currentClient)
    // Local to this effect run: a switch to a different camera tears this
    // run down (see the returned cleanup) before the next run starts, so a
    // stale run's own `live` flag — not a shared one — is what tells its
    // late-arriving getStream() to discard itself instead of landing on the
    // element a newer selection now owns.
    let live = true
    let currentStream: MediaStream | null = null
    // Set before the attempt, not after it: getStream() sends AddStream
    // first, so by the time it can fail the robot may already be encoding.
    // Failing this flag "on" costs at most one rejected remove() for a
    // stream the server never had; failing it "off" leaves the robot
    // encoding for a camera nobody is watching, which is the leak this
    // whole flag exists to close.
    let added = false

    const teardown = () => {
      currentStream?.getTracks().forEach((track) => track.stop())
      currentStream = null
      if (videoEl) videoEl.srcObject = null
      // track.stop() only tears down the local end — the robot keeps
      // encoding for `selected` until the server is told to stop.
      if (added) {
        added = false
        void streamClient.remove(selected).catch(() => {})
      }
    }

    const acquire = async () => {
      try {
        added = true
        const stream = await streamClient.getStream(selected)
        if (!live) {
          // Abandoned mid-flight (camera switched or tab hidden before this
          // resolved). Stop it rather than leak it or attach it late.
          stream.getTracks().forEach((track) => track.stop())
          return
        }
        // Idempotent: an earlier still-in-flight acquire (e.g. a fast
        // hide/show/hide/show while getStream is slow) can resolve after
        // this one and land here too. Stop whatever is currently held
        // before taking ownership so an earlier stream is never orphaned.
        currentStream?.getTracks().forEach((track) => track.stop())
        currentStream = stream
        if (videoEl) videoEl.srcObject = stream
      } catch {
        // Best-effort: leave the element blank, nothing else to recover here.
      }
    }

    void acquire()

    // Tear down while hidden and re-acquire on return — a torn-down stream
    // left untouched stays black forever after the first tab switch.
    const onVisibilityChange = () => {
      if (document.hidden) {
        live = false
        teardown()
      } else {
        live = true
        void acquire()
      }
    }
    document.addEventListener('visibilitychange', onVisibilityChange)

    // A reconnect's old MediaStream tracks are dead but stay attached to the
    // element — nothing else re-runs this effect (it depends only on `name`),
    // so without this the video goes black permanently after a WebRTC drop
    // and recovery, while the rest of the app reports "armed" again.
    const onReconnected = () => {
      teardown()
      live = true
      void acquire()
    }
    currentClient.on(MachineConnectionEvent.CONNECTED, onReconnected)

    return () => {
      live = false
      document.removeEventListener('visibilitychange', onVisibilityChange)
      currentClient.off(MachineConnectionEvent.CONNECTED, onReconnected)
      teardown()
    }
  })
</script>

<div class="camera">
  {#if name}
    <video bind:this={videoEl} autoplay muted playsinline></video>
  {:else}
    <p class="placeholder">no camera selected</p>
  {/if}
</div>

<style>
  /* Both branches share this root so the video is never nested in a wrapper
     that only exists on one branch — that would collapse it to zero height
     (an intrinsic-size box with nothing sizing it) and read as a stream bug
     rather than a layout one. */
  .camera {
    position: relative;
    flex: 1;
    min-height: 0;
    background: #000;
  }

  video {
    display: block;
    width: 100%;
    height: 100%;
    object-fit: contain;
  }

  .placeholder {
    display: flex;
    align-items: center;
    justify-content: center;
    height: 100%;
    margin: 0;
    color: #666;
  }
</style>
