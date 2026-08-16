<script lang="ts">
  import { tip } from "../lib/tooltip";
  // The publisher's picture, with a real fallback.
  //
  // Three things can go wrong and all three land on the same default tile: the
  // hub has no avatar for that namespace, the lookup fails, or the image 404s
  // after it loads. A broken-image glyph in a results list looks like a bug in
  // the page, so the tile is never absent — it is drawn while the lookup is
  // still in flight too, which also keeps rows from reflowing as avatars land.
  import { Box } from "lucide-svelte";
  import { getAuthorAvatar } from "../lib/hubApi";

  let { author = "", size = "w-8 h-8", source = "hf" }: { author?: string; size?: string; source?: string } = $props();

  // URLs that resolved but then failed to render, so onerror swaps in the tile
  // instead of leaving the browser's broken-image glyph.
  let broken = $state(new Set<string>());

  // A stable colour from the name rather than a flat grey, so the eye can still
  // tell one row's owner from another's at a glance.
  function hue(name: string): number {
    let h = 0;
    for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) % 360;
    return h;
  }
</script>

{#snippet fallback()}
  <span
    class="{size} rounded-md shrink-0 flex items-center justify-center text-white/85 border border-black/10"
    style="background: linear-gradient(140deg, hsl({hue(author)} 42% 42%), hsl({(hue(author) + 40) % 360} 42% 30%))"
    use:tip={author}
  >
    <Box class="w-1/2 h-1/2" strokeWidth={1.8} />
  </span>
{/snippet}

{#await getAuthorAvatar(author, source)}
  {@render fallback()}
{:then url}
  {#if url && !broken.has(url)}
    <img
      src={`/api/imgproxy?url=${encodeURIComponent(url)}`}
      alt=""
      loading="lazy"
      use:tip={author}
      class="{size} rounded-md object-cover shrink-0 border border-card-border bg-secondary"
      onerror={() => {
        broken = new Set(broken).add(url);
      }}
    />
  {:else}
    {@render fallback()}
  {/if}
{:catch}
  {@render fallback()}
{/await}
