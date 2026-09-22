<script lang="ts">
  // Fixed order first so the common controls always land in the same place;
  // anything a module adds beyond this set still renders (after these rows,
  // in Object.keys order) with its raw action name as the label, rather than
  // silently disappearing.
  const ORDER = [
    'forward',
    'back',
    'left',
    'right',
    'z_up',
    'z_down',
    'gripper_open',
    'gripper_close',
    'stop',
  ]
  const LABELS: Record<string, string> = {
    forward: 'Forward',
    back: 'Back',
    left: 'Left',
    right: 'Right',
    z_up: 'Up',
    z_down: 'Down',
    gripper_open: 'Open gripper',
    gripper_close: 'Close gripper',
    stop: 'Stop',
  }

  let { keys, held }: { keys: Record<string, string>; held: string[] } = $props()

  const rows = $derived(
    [
      ...ORDER.filter((action) => action in keys),
      ...Object.keys(keys).filter((action) => !ORDER.includes(action)),
    ].map((action) => ({
      action,
      label: LABELS[action] ?? action,
      code: keys[action],
    })),
  )
</script>

<ul class="legend">
  {#each rows as row (row.action)}
    <li class:held={held.includes(row.code)}>
      <span class="label">{row.label}</span>
      <kbd>{row.code}</kbd>
    </li>
  {/each}
</ul>

<style>
  .legend {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem 1rem;
    margin: 0;
    padding: 0.5rem 1rem;
    list-style: none;
    background: #1a1a1a;
    border-bottom: 1px solid #333;
  }

  li {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    font-size: 0.9rem;
    color: #aaa;
  }

  .label {
    min-width: 6rem;
  }

  /* Weight as well as colour: held state is the "is capture actually live"
     indicator, so it should not rely on colour alone. */
  li.held {
    color: #eee;
    font-weight: 600;
  }

  li.held kbd {
    background: #3a6;
    border-color: #4c8;
    color: #fff;
  }
</style>
