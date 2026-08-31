<script lang="ts">
  // The three system verbs for a frameless window, in the corner Windows always
  // puts them. Shared by the wizard's header and the app's title bar so the two
  // windows cannot drift into having different close buttons.
  //
  // Renders nothing outside the native window: in a browser tab these would be
  // three buttons that either lie (there is no window to minimise) or destroy
  // the user's tab.
  import { Minus, Square, X } from "lucide-svelte";
  import * as native from "../lib/native";
</script>

{#if native.isNative}
  <!-- stopPropagation on mousedown, or the drag region underneath swallows the
       press before it can ever become a click. -->
  <div class="winbtns flex shrink-0 self-start" onmousedown={(e) => e.stopPropagation()} role="presentation">
    <button class="winbtn" aria-label="Minimize" onclick={native.minimizeWindow}>
      <Minus size={14} />
    </button>
    <button class="winbtn" aria-label="Maximize" onclick={native.toggleMaximize}>
      <Square size={12} />
    </button>
    <button class="winbtn winbtn--close" aria-label="Close" onclick={native.closeWindow}>
      <X size={14} />
    </button>
  </div>
{/if}

<style>
  /* Immune to the interface size. :root carries `zoom: var(--qm-scale)` (see
     stores/uiScale.ts) and zoom compounds down the tree, so the reciprocal here
     multiplies back to exactly 1 and these three stay 46x34 whatever the rest of
     the app is doing. The title bar around them still scales -- it is the app's
     own chrome -- but these are SYSTEM verbs, drawn at the one size Windows
     draws them at in every other window, and a caption button that changes size
     with a zoom setting is the one thing on the desktop reached for by muscle
     memory that has moved. Fallback 1 because the wizard shares this component
     and never sets the variable.

     `self-start` in the markup, not `items-center` from the bar: once the bar
     scales and these do not, centring leaves them floating in the middle of a
     tall strip with a band of chrome above them. Windows hangs its caption
     buttons off the top-right corner and lets the slack fall below, so this
     hangs them off the same corner. (Inert in the wizard, where the wrapper is
     absolutely positioned rather than a flex item.) */
  .winbtns {
    zoom: calc(1 / var(--qm-scale, 1));
  }

  /* Sized and coloured like the real Windows caption buttons rather than like
     the app's own .btn: these are system verbs, and a user reaches for them by
     muscle memory. 46 wide is what Windows 11 itself draws at 100%.

     32 tall, not the 34 this used to carry: pinned to the top of the bar, an
     over-tall button hangs its hover fill two pixels into the content below,
     which the old vertical centring hid by splitting the overhang. 32 is the
     bar's own height, so at 100% they fill it exactly. */
  .winbtn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 46px;
    height: 32px;
    color: var(--color-txtsecondary);
    background: transparent;
    border: none;
    border-radius: 0;
    cursor: pointer;
  }
  .winbtn:hover {
    background: var(--color-secondary);
    color: var(--color-txtmain);
  }
  /* Close is the one button Windows colours, and the accent red is the same in
     both themes because it is the system's, not ours. */
  .winbtn--close:hover {
    background: #c42b1c;
    color: #fff;
  }
</style>
