// Svelte action: sizes a textarea to its content, growing/shrinking on input.
// Used by inline edit boxes that should open at the same size as the
// rendered message they replace, not a fixed row count.
export function autogrow(node: HTMLTextAreaElement) {
  function resize() {
    node.style.height = "auto";
    node.style.height = node.scrollHeight + "px";
  }
  // Deferred, not called inline: bind:value on this node may apply in the same
  // tick as this action's mount but after it (attribute/binding order isn't a
  // safe assumption) — resizing immediately can measure an empty textarea.
  queueMicrotask(resize);
  node.addEventListener("input", resize);
  return {
    destroy() {
      node.removeEventListener("input", resize);
    },
  };
}
