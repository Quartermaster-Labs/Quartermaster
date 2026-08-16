<script lang="ts">
  import { HelpCircle } from "lucide-svelte";
  import { tip } from "../lib/tooltip";

  // A standalone ⓘ affordance. The tooltip itself is `lib/tooltip.ts` — this is
  // only the glyph you hang it on. It used to draw its own bubble in hardcoded
  // bg-gray-900/text-white with `whitespace-nowrap`, so it ignored the theme and
  // ran off-screen on any multi-line string.
  interface Props {
    content: string;
  }

  let { content }: Props = $props();
</script>

<span class="inline-flex align-middle shrink-0 text-txtsecondary cursor-help hover:text-txtmain" use:tip={content}>
  <HelpCircle size={13} />
</span>
