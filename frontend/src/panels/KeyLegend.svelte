<script lang="ts">
  import type { LayoutAction } from '../lib/driver'

  // The module reports actions in legend display order (module.go's
  // actionControls), so there is no client-side ORDER to keep in sync.
  //
  // Labelled by control, never by the module's action name: the module
  // cannot know which physical direction a control drives in the consumer's
  // reference frame (see docs/SPEC.md), so the only thing it can honestly
  // report is "which control, with what value". Anything unrecognised falls
  // back to the raw control name rather than disappearing.
  const CONTROL_LABELS: Record<string, string> = {
    'AbsoluteHat0Y:-1': 'D-pad up',
    'AbsoluteHat0Y:1': 'D-pad down',
    'AbsoluteHat0X:-1': 'D-pad left',
    'AbsoluteHat0X:1': 'D-pad right',
    'ButtonLT:1': 'Left trigger',
    'ButtonRT:1': 'Right trigger',
    'ButtonWest:1': 'Close gripper',
    'ButtonEast:1': 'Open gripper',
    'ButtonEStop:1': 'Stop',
  }

  // KeyboardEvent.code names a physical key position, not something to show a
  // human: "KeyW" and "ControlLeft" are wire values. Modifiers keep their side,
  // since both layouts bind left and right variants to different actions.
  // Anything unmapped falls back to the raw code rather than being mangled.
  const KEY_LABELS: Record<string, string> = {
    Space: 'Space',
    ArrowUp: '↑',
    ArrowDown: '↓',
    ArrowLeft: '←',
    ArrowRight: '→',
    ShiftLeft: 'L Shift',
    ShiftRight: 'R Shift',
    ControlLeft: 'L Ctrl',
    ControlRight: 'R Ctrl',
  }

  const keyLabel = (code: string) => KEY_LABELS[code] ?? code.replace(/^(?:Key|Digit)/, '')

  let { actions, held }: { actions: LayoutAction[]; held: string[] } = $props()

  // Signed value only for axis controls (buttons are press/release; a "+1"
  // there is noise). Real minus glyph, not a hyphen.
  const emits = (a: LayoutAction) =>
    a.control.startsWith('Absolute') ? `${a.control} ${a.value < 0 ? '−' : '+'}${Math.abs(a.value)}` : a.control

  const rows = $derived(
    actions.map((a) => ({
      action: a.name,
      label: CONTROL_LABELS[`${a.control}:${a.value}`] ?? a.control,
      code: a.code,
      key: keyLabel(a.code),
      emits: emits(a),
    })),
  )
</script>

<ul class="legend">
  {#each rows as row (row.action)}
    <li class:held={held.includes(row.code)}>
      <kbd>{row.key}</kbd>
      <span class="text">
        <span class="label">{row.label}</span>
        <span class="emits">{row.emits}</span>
      </span>
    </li>
  {/each}
</ul>
<p class="note">
  Which way each axis moves depends on the reference frame your consumer is configured for.
</p>

<style>
  .legend {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    margin: 0;
    padding: 0.5rem 1rem;
    list-style: none;
    background: #1a1a1a;
    border-bottom: 1px solid #333;
  }

  /* Each action is one bordered chip. Proximity alone could not carry the
     grouping here: with a key, a label and a control name per item, whatever
     gap separates the items is also close to the gap inside them, so the
     control name of one action reads as belonging to the next one's label.
     A border is unambiguous however the row wraps. */
  li {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.25rem 0.55rem;
    border: 1px solid #333;
    border-radius: 0.4rem;
    background: #141414;
  }

  .text {
    display: flex;
    flex-direction: column;
    line-height: 1.15;
  }

  .label {
    font-size: 0.85rem;
    color: #ccc;
  }

  .emits {
    font-size: 0.7rem;
    color: #777;
    font-family: monospace;
  }

  /* Three channels, not just colour: held state is the "is capture actually
     live" indicator, so it must survive a colourblind or monochrome view. */
  li.held {
    border-color: #4c8;
    background: #14251c;
  }

  li.held .label {
    color: #eee;
    font-weight: 600;
  }

  li.held kbd {
    background: #3a6;
    border-color: #4c8;
    color: #fff;
  }

  .note {
    margin: 0;
    padding: 0.25rem 1rem 0.5rem;
    font-size: 0.8rem;
    color: #777;
    background: #1a1a1a;
    border-bottom: 1px solid #333;
  }
</style>
