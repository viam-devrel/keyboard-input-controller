<script lang="ts">
  import { MachineConnectionEvent, StreamClient, type RobotClient } from '@viamrobotics/sdk'

  let { client, name }: { client: RobotClient; name: string | null } = $props()

  let videoEl = $state<HTMLVideoElement>()

  // One StreamClient per connection, not one per camera selection. Its
  // constructor permanently registers two listeners on the RobotClient and it
  // exposes no dispose, so a per-selection instance leaks one every time the
  // user switches camera — and a leaked one re-adds its remembered streams on
  // the next CONNECTED event, resurrecting exactly the encoder load remove()
  // exists to shed.
  const streamClient = $derived(new StreamClient(client))

  $effect(() => {
    const selected = name
    if (!selected) return

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

    // Never stop a track here. They belong to an RTCRtpReceiver, not to us:
    // stopping one ends it permanently, and Viam's WebRTC fork deliberately
    // reuses the same transceiver when a stream is re-added, so the browser
    // hands back the *same*, now-ended track. getStream() matches on stream id
    // alone and never inspects readyState, so it resolves successfully with a
    // dead stream and the video is silently black until a page reload — the
    // "re-select a camera and it never comes back" bug. The remove() below is
    // what actually stops the robot encoding; stopping the local track never
    // contributed to that.
    const teardown = () => {
      currentStream = null
      if (videoEl) videoEl.srcObject = null
      if (added) {
        added = false
        void streamClient.remove(selected).catch(() => {})
      }
    }

    const acquire = async () => {
      try {
        added = true
        const stream = await streamClient.getStream(selected)
        // Abandoned mid-flight (camera switched or tab hidden before this
        // resolved). Just drop it: teardown() already sent the remove() that
        // stops the robot end, and ending the track would poison the receiver
        // for every future re-add.
        if (!live) return
        if (stream.getTracks().some((t) => t.readyState === 'ended')) {
          // Unreachable while nothing ends receiver tracks — but getStream()
          // resolves happily with a dead stream, so the only other symptom is
          // an unexplained black video. Leave a breadcrumb.
          console.warn('[CameraView] stream resolved with an ended track', selected, stream.id)
        }
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
    client.on(MachineConnectionEvent.CONNECTED, onReconnected)

    return () => {
      live = false
      document.removeEventListener('visibilitychange', onVisibilityChange)
      client.off(MachineConnectionEvent.CONNECTED, onReconnected)
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
