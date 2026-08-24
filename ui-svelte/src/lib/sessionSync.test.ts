import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { get, writable } from "svelte/store";
import { syncSessions } from "./sessionSync";

// A BroadcastChannel that delivers synchronously to the other channels of the
// same name, so a test can assert on the merge instead of on a timer. The real
// one is async and jsdom does not always carry it at all.
class FakeChannel {
  static open: FakeChannel[] = [];
  onmessage: ((e: { data: unknown }) => void) | null = null;
  constructor(public name: string) {
    FakeChannel.open.push(this);
  }
  postMessage(data: unknown) {
    const clone = JSON.parse(JSON.stringify(data));
    for (const c of FakeChannel.open) if (c !== this && c.name === this.name) c.onmessage?.({ data: clone });
  }
  close() {}
}

type S = { id: string; updatedAt: number; title?: string };

const flush = () => vi.advanceTimersByTime(500);

describe("syncSessions", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    FakeChannel.open = [];
    vi.stubGlobal("BroadcastChannel", FakeChannel);
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  // Two documents each own the whole list and PUT the whole list, so a session
  // one of them has never heard of is one it is about to delete from the server.
  it("carries an addition to the other document", () => {
    const a = writable<S[]>([{ id: "x", updatedAt: 1 }]);
    const b = writable<S[]>([{ id: "x", updatedAt: 1 }]);
    syncSessions("t", a, () => null);
    syncSessions("t", b, () => null);

    a.set([{ id: "x", updatedAt: 1 }, { id: "new", updatedAt: 2 }]);
    flush();

    expect(get(b).map((s) => s.id).sort()).toEqual(["new", "x"]);
  });

  it("keeps the newer copy of a session both documents hold", () => {
    const a = writable<S[]>([{ id: "x", updatedAt: 5, title: "fresh" }]);
    const b = writable<S[]>([{ id: "x", updatedAt: 1, title: "stale" }]);
    syncSessions("t", a, () => null);
    syncSessions("t", b, () => null);

    a.set([{ id: "x", updatedAt: 5, title: "fresh" }]);
    flush();

    expect(get(b)[0].title).toBe("fresh");
  });

  // A plain union would resurrect every deleted chat the moment another document
  // spoke, so deletions have to travel as deletions.
  it("propagates a deletion instead of resurrecting it", () => {
    const a = writable<S[]>([{ id: "x", updatedAt: 1 }, { id: "gone", updatedAt: 1 }]);
    const b = writable<S[]>([{ id: "x", updatedAt: 1 }, { id: "gone", updatedAt: 1 }]);
    syncSessions("t", a, () => null);
    syncSessions("t", b, () => null);

    a.set([{ id: "x", updatedAt: 1 }]);
    flush();

    expect(get(b).map((s) => s.id)).toEqual(["x"]);
  });

  // The generating session is written a token at a time locally; any copy that
  // arrives over the wire is stale by definition, deletion included.
  it("never lets a remote copy touch the session being generated", () => {
    const a = writable<S[]>([{ id: "live", updatedAt: 9, title: "streaming" }]);
    const b = writable<S[]>([{ id: "live", updatedAt: 1, title: "old" }]);
    syncSessions("t", a, () => "live");
    syncSessions("t", b, () => null);

    // b, which knows nothing of the turn, both downgrades and drops it.
    b.set([]);
    flush();

    expect(get(a)).toEqual([{ id: "live", updatedAt: 9, title: "streaming" }]);
  });

  // The race the tombstones exist for: the other document composed its list
  // BEFORE it heard about the delete, so its broadcast still carries the corpse.
  it("is not resurrected by a broadcast composed before the deletion", () => {
    const a = writable<S[]>([{ id: "x", updatedAt: 1 }, { id: "gone", updatedAt: 1 }]);
    const b = writable<S[]>([{ id: "x", updatedAt: 1 }, { id: "gone", updatedAt: 1 }]);
    syncSessions("t", a, () => null);
    syncSessions("t", b, () => null);

    a.set([{ id: "x", updatedAt: 1 }]); // deleted here
    flush();
    b.set([{ id: "x", updatedAt: 1 }, { id: "gone", updatedAt: 2 }]); // stale, and newer
    flush();

    expect(get(a).map((s) => s.id)).toEqual(["x"]);
  });

  it("does not echo an applied merge back and forth", () => {
    const a = writable<S[]>([]);
    const b = writable<S[]>([]);
    syncSessions("t", a, () => null);
    syncSessions("t", b, () => null);

    const seen = vi.fn();
    b.subscribe(seen);
    seen.mockClear();

    a.set([{ id: "x", updatedAt: 1 }]);
    flush();
    flush();

    // One application, not a volley: applying a received list must not
    // rebroadcast it.
    expect(seen).toHaveBeenCalledTimes(1);
  });
});
