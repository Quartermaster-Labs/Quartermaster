<script lang="ts">
  // The Quartermaster build itself, in the same shape as the managed-backend
  // cards below it: what is installed, what is available, one button to move
  // between them.
  //
  // The sidebar already carries an update button, but it only exists WHEN an
  // update exists — so there was nowhere to answer "am I current?", and no way
  // to ask before the server's six-hour poll came round. This is that place.
  // The state machine is shared with the sidebar (stores/update), so an update
  // started here shows its progress there and vice versa.
  import { onMount } from "svelte";
  import { ArrowUpCircle, RefreshCw, ExternalLink, AlertTriangle, Check } from "lucide-svelte";
  import { tip } from "../lib/tooltip";
  import { versionInfo } from "../stores/api";
  import {
    updateStatus,
    updateBusy,
    updateChecking,
    updateCheckError,
    fetchUpdateStatus,
    checkForUpdates,
    applyUpdate,
    resumePolling,
    updateProgressLabel,
    updatePercent,
  } from "../stores/update";

  onMount(async () => {
    const st = await fetchUpdateStatus();
    // An apply that was started elsewhere (the sidebar, another tab) and is
    // still running: adopt it rather than showing an idle card with a button.
    resumePolling(st?.phase);
  });

  // The status is authoritative once read; /api/version is the fallback for the
  // first paint, before the fetch lands.
  let latest = $derived($updateStatus?.latest || $versionInfo.latest_version || "");
  let available = $derived($updateStatus?.available ?? $versionInfo.update_available ?? false);
  let blocked = $derived($updateStatus?.blocked || $versionInfo.update_blocked || "");
  let releaseURL = $derived($updateStatus?.release_url || $versionInfo.release_url || "");
  // undefined until the status arrives — treated as "assume it checks", so the
  // card does not flash "development build" at a release install on every open.
  let checks = $derived($updateStatus?.enabled ?? true);
  let manualRestart = $derived(($updateStatus?.restart ?? $versionInfo.update_restart ?? "auto") === "manual");

  let label = $derived(updateProgressLabel($updateStatus, $updateBusy));
  let pct = $derived(updatePercent($updateStatus));

  // Relative, because the absolute time answers a question nobody asked: what
  // matters is whether the answer on screen is minutes or days old.
  function agoLabel(iso: string | undefined): string {
    if (!iso) return "";
    const t = Date.parse(iso);
    if (!Number.isFinite(t)) return "";
    const secs = Math.max(0, Math.round((Date.now() - t) / 1000));
    if (secs < 60) return "just now";
    if (secs < 3600) return `${Math.floor(secs / 60)} min ago`;
    if (secs < 86400) return `${Math.floor(secs / 3600)} h ago`;
    return `${Math.floor(secs / 86400)} d ago`;
  }
  let checkedAgo = $derived(agoLabel($updateStatus?.checked_at));
</script>

<div class="mb-6">
  <div class="flex items-baseline gap-2 mb-1">
    <h6>Quartermaster</h6>
    <span class="font-mono text-[0.65rem] text-txtsecondary truncate">
      {$versionInfo.commit?.substring(0, 7) ?? ""}
      {$versionInfo.build_date && $versionInfo.build_date !== "unknown" ? `· ${$versionInfo.build_date}` : ""}
    </span>
  </div>
  <p class="text-[0.7rem] text-txtsecondary mb-3">
    Updates swap the running executable in place, with no installer and no wizard. The previous binary is kept beside it until
    the next start, so a bad update can be rolled back.
  </p>

  <section class="rounded-md border border-card-border bg-surface/40">
    <header class="flex items-center gap-2 px-3 py-2 border-b border-card-border">
      <span class="text-[0.8125rem] text-txtmain">Version</span>
      <span
        class="font-mono text-[0.6rem] rounded px-1.5 py-0.5 border border-card-border text-txtsecondary"
        use:tip={"The build running right now"}
      >{$versionInfo.version}</span>

      {#if !checks}
        <span class="font-mono text-[0.6rem] text-txtsecondary">development build (never checks)</span>
      {:else if available}
        <span class="font-mono text-[0.6rem] text-primary">update: {latest}</span>
      {:else if $updateStatus?.checked_at}
        <span class="font-mono text-[0.6rem] text-txtsecondary inline-flex items-center gap-1">
          <Check size={11} /> up to date
        </span>
      {/if}

      {#if releaseURL}
        <a
          href={releaseURL}
          target="_blank"
          rel="noreferrer"
          class="ml-auto shrink-0 text-txtsecondary hover:text-primary"
          use:tip={"Release notes"}
          aria-label="Open the release notes"
        ><ExternalLink size={13} /></a>
      {/if}
    </header>

    <div class="px-3 py-2.5 flex flex-col gap-2">
      <div class="flex flex-wrap items-center gap-2 font-mono text-xs">
        <button
          type="button"
          class="btn btn--sm inline-flex items-center gap-1 uppercase tracking-wide hover:border-primary hover:text-primary disabled:opacity-50"
          disabled={$updateChecking || !checks}
          use:tip={checks
            ? "Ask GitHub now instead of waiting for the six-hourly check"
            : "This build has no release to compare against"}
          onclick={() => checkForUpdates()}
        >
          <RefreshCw size={12} class={$updateChecking ? "animate-spin" : ""} />
          {$updateChecking ? "Checking…" : "Check for updates"}
        </button>

        {#if available && !blocked}
          <button
            type="button"
            class="btn btn--sm inline-flex items-center gap-1 uppercase tracking-wide border-primary text-primary hover:bg-primary/10 disabled:opacity-50"
            disabled={$updateBusy}
            use:tip={manualRestart
              ? "Install it now; this install is supervised, so it switches over on the next service restart"
              : "Download, verify and swap in the new binary, then restart"}
            onclick={() => applyUpdate()}
          ><ArrowUpCircle size={12} /> {$updateBusy ? label : `Update to ${latest}`}</button>
        {/if}

        {#if checkedAgo}
          <span class="text-[0.65rem] text-txtsecondary">checked {checkedAgo}</span>
        {/if}
      </div>

      {#if $updateBusy}
        <div class="flex flex-col gap-1">
          <div class="h-1.5 rounded bg-card-border overflow-hidden">
            <div class="h-full bg-primary transition-[width]" style={`width:${pct}%`}></div>
          </div>
          <span class="font-mono text-[0.65rem] text-txtsecondary">
            {label}{manualRestart ? " · restart the service afterwards to switch over" : ""}
          </span>
        </div>
      {/if}

      {#if $updateCheckError}
        <span class="font-mono text-[0.65rem] text-error">{$updateCheckError}</span>
      {/if}

      <!-- A release exists but this install cannot replace its own binary: a
           container image, or an install directory this process cannot write.
           Not an error — the update is real, it just has to come from wherever
           this copy was installed from. -->
      {#if available && blocked}
        <div class="flex items-start gap-2 rounded border border-warning/40 bg-warning/10 px-2 py-1.5 text-[0.7rem] text-txtmain">
          <AlertTriangle size={13} class="mt-0.5 shrink-0 text-warning" />
          <span>
            {latest} is available, but cannot be installed from here: {blocked}.
            {#if releaseURL}
              <a href={releaseURL} target="_blank" rel="noreferrer" class="underline hover:text-primary">Get it manually</a>.
            {/if}
          </span>
        </div>
      {/if}

      {#if !checks}
        <p class="text-[0.7rem] text-txtsecondary">
          A build from source reports no release version, so there is nothing to compare against and it is never told it
          is out of date. Released builds check on start and every six hours.
        </p>
      {/if}
    </div>
  </section>
</div>
