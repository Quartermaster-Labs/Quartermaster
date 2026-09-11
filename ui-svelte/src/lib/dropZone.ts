// Drag-and-drop file attachment for the playground composers.
//
// The arriving-files pipeline already exists and is untouched by this: chat has
// ONE entry point (`processFiles` — picker, paste and now drop), the image tab
// has `attachFiles`. This action only delivers a `File[]` to it and paints the
// hover state while a drag is over the node.
//
// It is an action rather than four inline handlers because a naive `ondrop`
// does not work, for three separate reasons:
//
//  1. `dragover` MUST call `preventDefault()` or `drop` never fires at all. The
//     default action for a drag over a page is "this is not a drop target".
//  2. `dragenter`/`dragleave` fire for every CHILD the pointer crosses, so a
//     boolean set on enter and cleared on leave flickers off the moment the
//     cursor moves from the message list onto a chip inside it. A depth counter
//     is the fix; `drop` and a window-level `dragend` reset it, because a drag
//     that ends outside the node never sends a final `dragleave`.
//  3. A drop the page does NOT handle makes the browser NAVIGATE to the file.
//     In the playground that discards the open chat and aborts any turn
//     streaming into it, and in the app window there is no back button.
//     `guardWindowDrop()` turns a miss into a no-op.

export interface DropZoneOptions {
  /** Dropped files. `skipped` names what wasn't a file (a folder). */
  onFiles: (files: File[], skipped: string[]) => void;
  /** Hover state, for the "drop to attach" overlay. */
  onActive?: (active: boolean) => void;
  /** While false the node ignores drags (the window guard still blocks navigation). */
  enabled?: boolean;
}

/** True only for an OS/file drag — dragging a text selection must not arm the overlay. */
export function dragHasFiles(e: DragEvent): boolean {
  const types = e.dataTransfer?.types;
  return !!types && Array.from(types).includes("Files");
}

/**
 * Split a drop into real files and things that only look like one.
 *
 * A dropped FOLDER appears in `dataTransfer.files` as a zero-length, empty-type
 * File that throws on read — attaching it would produce a chip that fails after
 * the fact. Only `dataTransfer.items` carries the entry metadata that tells the
 * two apart, so the folder is named and rejected up front.
 */
export function collectDropFiles(dt: DataTransfer): { files: File[]; skipped: string[] } {
  const files: File[] = [];
  const skipped: string[] = [];
  const items = dt.items;
  if (items && items.length > 0 && typeof items[0]?.webkitGetAsEntry === "function") {
    for (const it of Array.from(items)) {
      if (it.kind !== "file") continue;
      const entry = it.webkitGetAsEntry();
      if (entry?.isDirectory) {
        skipped.push(entry.name);
        continue;
      }
      const f = it.getAsFile();
      if (f) files.push(f);
    }
    return { files, skipped };
  }
  return { files: Array.from(dt.files ?? []), skipped };
}

export function dropZone(node: HTMLElement, options: DropZoneOptions) {
  let opts = options;
  let depth = 0;
  let active = false;

  function setActive(v: boolean) {
    if (active === v) return;
    active = v;
    opts.onActive?.(v);
  }

  function reset() {
    depth = 0;
    setActive(false);
  }

  function live(e: DragEvent): boolean {
    return opts.enabled !== false && dragHasFiles(e);
  }

  function onEnter(e: DragEvent) {
    if (!live(e)) return;
    e.preventDefault();
    depth++;
    setActive(true);
  }

  function onOver(e: DragEvent) {
    if (!live(e)) return;
    // Claims the drop AND tells the window guard below to stand down
    // (it checks `defaultPrevented`).
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = "copy";
  }

  function onLeave(e: DragEvent) {
    if (!live(e)) return;
    depth = Math.max(0, depth - 1);
    if (depth === 0) setActive(false);
  }

  function onDrop(e: DragEvent) {
    if (!live(e)) return;
    e.preventDefault();
    reset();
    if (!e.dataTransfer) return;
    const { files, skipped } = collectDropFiles(e.dataTransfer);
    if (files.length > 0 || skipped.length > 0) opts.onFiles(files, skipped);
  }

  node.addEventListener("dragenter", onEnter);
  node.addEventListener("dragover", onOver);
  node.addEventListener("dragleave", onLeave);
  node.addEventListener("drop", onDrop);
  // A drag released outside the node (or cancelled with Escape) sends no
  // `dragleave`, which would leave the overlay stuck on until the next drag.
  window.addEventListener("dragend", reset);
  window.addEventListener("drop", reset);

  return {
    update(next: DropZoneOptions) {
      opts = next;
      if (next.enabled === false) reset();
    },
    destroy() {
      node.removeEventListener("dragenter", onEnter);
      node.removeEventListener("dragover", onOver);
      node.removeEventListener("dragleave", onLeave);
      node.removeEventListener("drop", onDrop);
      window.removeEventListener("dragend", reset);
      window.removeEventListener("drop", reset);
    },
  };
}

let guarded = false;

/**
 * Make a file dropped ANYWHERE else in the document a no-op instead of a
 * navigation. Idempotent, and deliberately never removed: it has to outlive
 * whichever component happened to call it.
 *
 * Both handlers bail on `defaultPrevented`, which a real `dropZone` has already
 * set by the time the event bubbles up here — so the guard never steals a drop
 * the composer wanted.
 */
export function guardWindowDrop(): void {
  if (guarded || typeof window === "undefined") return;
  guarded = true;
  window.addEventListener("dragover", (e: DragEvent) => {
    if (e.defaultPrevented) return;
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = "none";
  });
  window.addEventListener("drop", (e: DragEvent) => {
    if (e.defaultPrevented) return;
    e.preventDefault();
  });
}
