<script lang="ts">
  import { pendingConfirm } from "../lib/confirm";
  import { AlertTriangle, HelpCircle } from "lucide-svelte";

  // Renders whatever askConfirm() put in the store. Mounted once per app root
  // (dashboard + playground), never instantiated by a call site.
  //
  // Native <dialog>+showModal, not a z-indexed div: ModelConfigModal and
  // CaptureDialog are themselves showModal dialogs living in the browser's top
  // layer, and no z-index can put a normal-flow overlay above that. A dialog
  // opened later goes on top of one opened earlier, so this always wins.
  let dialogEl = $state<HTMLDialogElement | null>(null);
  let confirmBtn = $state<HTMLButtonElement | null>(null);

  function settle(ok: boolean): void {
    const req = $pendingConfirm;
    pendingConfirm.set(null);
    req?.resolve(ok);
  }

  // Open/close follows the store, and the confirm button takes focus so Enter
  // works immediately and the focus ring lands somewhere sensible.
  $effect(() => {
    const el = dialogEl;
    if (!el) return;
    if ($pendingConfirm) {
      if (!el.open) el.showModal();
      requestAnimationFrame(() => confirmBtn?.focus());
    } else if (el.open) {
      el.close();
    }
  });
</script>

{#if $pendingConfirm}
  {@const req = $pendingConfirm}
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
  <dialog
    bind:this={dialogEl}
    class="bg-surface text-txtmain rounded-lg border border-card-border shadow-xl w-full max-w-sm p-0 backdrop:bg-black/50 m-auto"
    aria-label={req.title}
    oncancel={(e) => {
      e.preventDefault();
      settle(false);
    }}
    onclick={(e) => e.target === dialogEl && settle(false)}
  >
    <div class="flex gap-3 p-4">
      <span class="mt-0.5 shrink-0 {req.danger ? 'text-error' : 'text-txtsecondary'}">
        {#if req.danger}
          <AlertTriangle size={20} />
        {:else}
          <HelpCircle size={20} />
        {/if}
      </span>
      <div class="min-w-0 flex flex-col gap-1">
        <p class="text-sm font-semibold text-txtmain">{req.title}</p>
        {#if req.body}
          <!-- whitespace-pre-line: callers pass the same "\n\n" separated text
               they used to hand window.confirm(). -->
          <p class="text-label whitespace-pre-line text-txtsecondary">{req.body}</p>
        {/if}
      </div>
    </div>
    <div class="flex justify-end gap-2 border-t border-card-border-inner px-4 py-3">
      {#if !req.acknowledge}
        <button class="btn btn--sm" onclick={() => settle(false)}>{req.cancelLabel ?? "Cancel"}</button>
      {/if}
      <button
        bind:this={confirmBtn}
        class="btn btn--sm {req.danger ? 'btn--danger' : 'btn--primary'}"
        onclick={() => settle(true)}
      >
        {req.confirmLabel ?? "Confirm"}
      </button>
    </div>
  </dialog>
{/if}
