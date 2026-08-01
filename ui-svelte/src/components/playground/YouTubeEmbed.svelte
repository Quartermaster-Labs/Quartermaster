<script lang="ts">
  import { Play } from "lucide-svelte";
  import { fetchYouTubeMeta, type YouTubeMeta } from "../../lib/youtube";

  // Discord-style unfurl card for a YouTube link found in a message.
  // Metadata comes from the cached /api/youtube/meta route; the thumbnail is
  // hotlinked straight from i.ytimg.com (see youtube_meta.go for that choice).
  //
  // Rendered INSIDE the message bubble, not next to it: as a sibling with its own
  // filled surface and the same right-alignment it read as a second user turn.
  // `onDark` picks the surface for the user bubble's dark background.
  let { id, onDark = false }: { id: string; onDark?: boolean } = $props();

  let meta = $state<YouTubeMeta | null>(null);
  let failed = $state(false);
  // Thumbnails 404 for a handful of ids; fall back to a play-icon placeholder
  // rather than a broken-image glyph.
  let noThumb = $state(false);

  // Keyed on id so a re-used component instance refetches instead of showing
  // the previous video's card.
  $effect(() => {
    const want = id;
    meta = null;
    failed = false;
    noThumb = false;
    fetchYouTubeMeta(want).then((m) => {
      if (want !== id) return; // id changed while in flight
      if (m) meta = m;
      else failed = true;
    });
  });
</script>

<!-- A failed lookup renders nothing: the raw link is still in the message text,
     so an unfurl that didn't resolve should be invisible, not an error box. -->
{#if !failed}
  <a
    href={meta?.url ?? `https://www.youtube.com/watch?v=${id}`}
    target="_blank"
    rel="noreferrer noopener"
    class="group/yt mt-2 flex items-stretch gap-2.5 w-full max-w-sm rounded-lg border overflow-hidden transition-colors no-underline
      {onDark
      ? 'border-white/10 bg-white/[0.06] hover:bg-white/[0.11]'
      : 'border-card-border bg-surface/60 hover:bg-surface'}"
  >
    <!-- Fixed aspect, and the img is absolutely positioned so it fills the column
         however tall the card grows (items-stretch + a fixed-height img left a
         gap under the thumbnail whenever the title wrapped to two lines). -->
    <div class="relative w-28 shrink-0 self-stretch min-h-[63px] bg-black/40">
      {#if !noThumb}
        <img
          src={meta?.thumb ?? `https://i.ytimg.com/vi/${id}/mqdefault.jpg`}
          alt=""
          loading="lazy"
          referrerpolicy="no-referrer"
          class="absolute inset-0 w-full h-full object-cover"
          onerror={() => (noThumb = true)}
        />
      {/if}
      <span
        class="absolute inset-0 flex items-center justify-center text-white/85 group-hover/yt:text-white drop-shadow transition-colors"
      >
        <Play class="w-5 h-5 fill-current" />
      </span>
    </div>
    <div class="min-w-0 flex-1 py-1.5 pr-2.5 flex flex-col justify-center gap-0.5">
      <div class="text-[10px] uppercase tracking-wide {onDark ? 'text-white/45' : 'text-txtsecondary'}">
        YouTube
      </div>
      <!-- Placeholder bars while the lookup is in flight, so the card doesn't
           pop in and shove the message around. -->
      {#if meta}
        <div class="text-[0.8125rem] font-medium leading-snug line-clamp-2">{meta.title}</div>
        {#if meta.uploader}
          <div class="text-xs truncate {onDark ? 'text-white/55' : 'text-txtsecondary'}">
            {meta.uploader}
          </div>
        {/if}
      {:else}
        <div class="h-3.5 w-40 max-w-full rounded {onDark ? 'bg-white/15' : 'bg-txtsecondary/20'}"></div>
        <div class="h-3 w-24 max-w-full rounded {onDark ? 'bg-white/15' : 'bg-txtsecondary/20'}"></div>
      {/if}
    </div>
  </a>
{/if}
