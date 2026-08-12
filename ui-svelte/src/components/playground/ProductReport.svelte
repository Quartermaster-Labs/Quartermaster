<script lang="ts">
  import { ExternalLink, ImageOff, Store, Tag } from "lucide-svelte";
  import { proxiedImage, type Product, type ProductBlock } from "../../lib/productBlock";

  // Renders the shopping assistant's ```products block (see lib/productBlock.ts)
  // as a card grid: picture, badge, price, specs, one line of why, buy link.
  // Read-only — unlike AskWizard nothing here sends a message, so it renders on
  // any assistant turn, not just the last one.
  let { report }: { report: ProductBlock } = $props();

  // Images come off shop CDNs and fail for reasons we can't fix from here (dead
  // link, hotlink block, geo redirect). A per-card flag swaps in the monogram
  // rather than leaving the browser's broken-image glyph in a report.
  let failed = $state<Record<number, boolean>>({});

  function initials(name: string): string {
    return name
      .split(/\s+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((w) => w[0]?.toUpperCase() ?? "")
      .join("");
  }

  function host(url: string): string {
    try {
      return new URL(url).hostname.replace(/^www\./, "");
    } catch {
      return "";
    }
  }

  function shopLabel(p: Product): string {
    return p.shop || host(p.url);
  }
</script>

<div class="mt-3 flex flex-col gap-2.5">
  {#if report.pick}
    <p class="text-sm text-txtsecondary">{report.pick}</p>
  {/if}

  <!-- auto-fill, not a fixed column count: two options should not stretch into
       half-empty banners, and six should not squeeze into unreadable slivers. -->
  <div class="grid gap-2.5 [grid-template-columns:repeat(auto-fill,minmax(13rem,1fr))]">
    {#each report.products as p, i (p.name + i)}
      <div class="flex flex-col overflow-hidden rounded-xl border border-card-border bg-surface shadow-sm">
        <div class="relative flex h-32 items-center justify-center bg-background/60">
          {#if p.image && !failed[i]}
            <!-- Proxied, never hotlinked: see internal/server/imgproxy.go. -->
            <img
              src={proxiedImage(p.image)}
              alt={p.name}
              loading="lazy"
              class="h-full w-full object-contain p-2"
              onerror={() => (failed = { ...failed, [i]: true })}
            />
          {:else}
            <div class="flex flex-col items-center gap-1 text-txtsecondary/50">
              {#if p.image}
                <ImageOff class="h-5 w-5" />
              {:else}
                <span class="text-xl font-semibold tracking-wide">{initials(p.name)}</span>
              {/if}
            </div>
          {/if}
          {#if p.badge}
            <span
              class="absolute left-2 top-2 rounded-md bg-primary px-1.5 py-0.5 text-[0.6875rem] font-medium text-btn-primary-text shadow-sm"
            >
              {p.badge}
            </span>
          {/if}
        </div>

        <div class="flex min-w-0 flex-1 flex-col gap-1.5 p-3">
          <span class="text-sm font-medium leading-snug text-txtmain">{p.name}</span>

          <div class="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
            {#if p.price}
              <span class="inline-flex items-center gap-1 font-semibold text-txtmain">
                <Tag class="h-3 w-3 text-primary" />{p.price}
              </span>
            {/if}
            {#if shopLabel(p)}
              <span class="inline-flex items-center gap-1 text-txtsecondary">
                <Store class="h-3 w-3" />{shopLabel(p)}
              </span>
            {/if}
            {#if p.cite !== null}
              <span class="text-txtsecondary/70">[{p.cite}]</span>
            {/if}
          </div>

          {#if p.specs.length}
            <ul class="flex flex-col gap-0.5 text-xs text-txtsecondary">
              {#each p.specs as spec (spec)}
                <li class="flex gap-1.5"><span class="text-txtsecondary/50">•</span><span class="min-w-0">{spec}</span></li>
              {/each}
            </ul>
          {/if}

          {#if p.why}
            <p class="text-xs leading-relaxed text-txtmain/80">{p.why}</p>
          {/if}

          {#if p.url}
            <a
              href={p.url}
              target="_blank"
              rel="noopener noreferrer"
              class="mt-auto inline-flex items-center gap-1 pt-1.5 text-xs font-medium text-primary hover:underline"
            >
              View <ExternalLink class="h-3 w-3" />
            </a>
          {/if}
        </div>
      </div>
    {/each}
  </div>
</div>
