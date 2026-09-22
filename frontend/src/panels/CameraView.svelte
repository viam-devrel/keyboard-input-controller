<script lang="ts">
  import { StreamClient, type RobotClient } from '@viamrobotics/sdk'
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

    const streamClient = new StreamClient(untrack(() => client))
    // Local to this effect run: a switch to a different camera tears this
    // run down (see the returned cleanup) before the next run starts, so a
    // stale run's own `live` flag — not a shared one — is what tells its
    // late-arriving getStream() to discard itself instead of landing on the
    // element a newer selection now owns.
    let live = true
    let currentStream: MediaStream | null = null

    const teardown = () => {
      currentStream?.getTracks().forEach((track) => track.stop())
      currentStream = null
      if (videoEl) videoEl.srcObject = null
    }

    const acquire = async () => {
      try {
        const stream = await streamClient.getStream(selected)
        if (!live) {
          // Abandoned mid-flight (camera switched or tab hidden before this
          // resolved). Stop it rather than leak it or attach it late.
          stream.getTracks().forEach((track) => track.stop())
          return
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

    return () => {
      live = false
      document.removeEventListener('visibilitychange', onVisibilityChange)
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
