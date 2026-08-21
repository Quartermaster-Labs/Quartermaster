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
  <div class="flex shrink-0" onmousedown={(e) => e.stopPropagation()} role="presentation">
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
  /* Sized and coloured like the real Windows caption buttons rather than like
     the app's own .btn: these are system verbs, and a user reaches for them by
     muscle memory. 46x34 is what Windows 11 itself draws at 100%. */
  .winbtn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 46px;
    height: 34px;
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
