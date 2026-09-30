import { writable } from "svelte/store";
import type { HubFileEntry } from "./hubApi";

// The browser-rendered stand-in for the host's native file dialog.
//
// Every Browse button asks the server for a NATIVE dialog first. When that
// cannot reach the user (headless server, or the dashboard open from another
// machine) the server answers 501, and the picker functions in stores/api.ts
// open this instead. It lists folders through GET /api/pick/browse, which is
// confined to the models and backends folders; a path outside them stays a
// typed one, so the dialog also takes a pasted path.
//
// Same shape as lib/confirm.ts: a single <WebPickerHost /> at the dashboard
// root renders whatever is in the store and settles the pending promise.

export interface WebPickRequest {
  /** "folder" picks a directory; anything else is a server pickSpecs kind. */
  kind: string;
  title: string;
  /** Where to open: a form field's current value is fine (a file opens its folder). */
  start?: string;
}

interface PendingPick extends WebPickRequest {
  resolve: (path: string | null) => void;
}

export const pendingPick = writable<PendingPick | null>(null);

/** Open the web picker. Resolves the chosen absolute path, or null on cancel. */
export function openWebPicker(req: WebPickRequest): Promise<string | null> {
  return new Promise((resolve) => {
    pendingPick.update((prev) => {
      prev?.resolve(null);
      return { ...req, resolve };
    });
  });
}

export interface PickRoot {
  id: string;
  label: string;
  path: string;
}

export interface PickListing {
  roots: PickRoot[];
  root: string;
  path: string;
  rel: string;
  parent: string;
  entries: HubFileEntry[];
  truncated?: boolean;
  /** Files were hidden by the kind's extension filter. */
  filtered?: boolean;
}

export async function listPickFolder(opts: {
  kind: string;
  root?: string;
  path?: string;
  all?: boolean;
}): Promise<PickListing> {
  const q = new URLSearchParams({ kind: opts.kind });
  if (opts.root) q.set("root", opts.root);
  if (opts.path) q.set("path", opts.path);
  if (opts.all) q.set("all", "1");
  const r = await fetch(`/api/pick/browse?${q}`);
  if (!r.ok) {
    // Errors come back as {error, src}; keep the raw text if it is anything else.
    const text = await r.text().catch(() => "");
    let msg = text || `${r.status}`;
    try {
      const body = JSON.parse(text);
      if (body?.error) msg = body.error;
    } catch {
      // not JSON
    }
    throw new Error(msg);
  }
  return (await r.json()) as PickListing;
}
