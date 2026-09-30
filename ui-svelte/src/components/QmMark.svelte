<script lang="ts">
  // The quartermaster mark (the helm) as inline SVG, so it can spin, roll and
  // take CSS like any icon. Same geometry as public/favicon.svg, which
  // packaging/icons/gen.py derives; keep the two in step if the art changes.
  let { class: cls = "" }: { class?: string } = $props();

  // The eight spokes and eight handles, as angles off +x. Generated rather than
  // pasted so the 16 near-identical paths do not drown the file.
  const spokes = [45, 135, 225, 315].map((a) => a * (Math.PI / 180));
  const handles = [0, 45, 90, 135, 180, 225, 270, 315].map((a) => a * (Math.PI / 180));
</script>

<svg class={cls} viewBox="0 0 256 256" fill="none" aria-hidden="true">
  {#each spokes as a (a)}
    <path d="M128 128 L{128 + 91 * Math.cos(a)} {128 + 91 * Math.sin(a)}" stroke="#B8410E" stroke-width="18" />
  {/each}
  <circle cx="128" cy="128" r="91" stroke="#FF6A2B" stroke-width="26" />
  {#each handles as a (a)}
    <path
      d="M{128 + 104 * Math.cos(a)} {128 + 104 * Math.sin(a)} L{128 + 119 * Math.cos(a)} {128 + 119 * Math.sin(a)}"
      stroke="#B8410E"
      stroke-width="18"
      stroke-linecap="round"
    />
  {/each}
  <circle cx="128" cy="128" r="25" fill="#FF6A2B" />
</svg>
