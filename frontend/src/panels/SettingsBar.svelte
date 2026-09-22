<script lang="ts">
  // Presentational only: App.svelte owns what these selects mean (loading,
  // resolving, saving). The root element's `data-settings` attribute is load
  // -bearing — driver.ts's inSettings() guard keys off it via closest(), so
  // it must stay on the outermost node. Styling hangs off the same attribute
  // selector rather than a class, so losing the attribute visibly breaks the
  // bar's look instead of silently breaking key capture.
  let {
    controllers,
    cameras,
    controller = $bindable(),
    camera = $bindable(),
  }: {
    controllers: string[]
    cameras: string[]
    controller: string | null
    camera: string | null
  } = $props()
</script>

<div data-settings>
  <label>
    Controller
    <select bind:value={controller}>
      {#each controllers as name (name)}
        <option value={name}>{name}</option>
      {/each}
    </select>
  </label>
  <label>
    Camera
    <select bind:value={camera}>
      <option value={null}>none</option>
      {#each cameras as name (name)}
        <option value={name}>{name}</option>
      {/each}
    </select>
  </label>
</div>

<style>
  [data-settings] {
    display: flex;
    align-items: center;
    gap: 1.5rem;
    padding: 0.5rem 1rem;
    background: #1a1a1a;
    border-bottom: 1px solid #333;
  }

  [data-settings] label {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.9rem;
  }

  [data-settings] select {
    background: #222;
    color: #eee;
    border: 1px solid #555;
    border-radius: 0.25rem;
    padding: 0.25rem 0.5rem;
    font: inherit;
  }
</style>
