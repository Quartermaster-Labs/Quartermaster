import type { ToolDef } from "./types";

// Assistant memory: short standing facts about the user that outlive one chat
// ("prefers metric", "runs an RX 7900 XTX"). Stored per user server-side
// (internal/server/memories.go) and editable in Settings → Memory.
//
// Recall is by INJECTION, not by a read tool: memoryBlock() renders the whole
// list into the system prompt every turn. A read tool would sit in the KV-stable
// prefix of every conversation and still only fire when the model thought to call
// it — which is the exact failure memory exists to prevent. The write tools below
// are the only memory surface the model gets.
//
// Duplicates are the STORE's problem, not the model's: internal/server/memories.go
// folds a restatement into the entry that already exists, so nothing here asks the
// model to audit its own block before saving. A paraphrase too loose to fold is
// not left to pile up either — the store names the near-identical entry in the
// tool RESULT, and the description below says what to do about it. Pointing at one
// specific pair after the fact beats asking for a pre-flight scan of the whole
// block: the model is deciding between two texts it can see, and it only has to
// decide when there is actually something to decide. The block is rendered
// append-only (see memoryBlock) so an ordinary save changes bytes only at its
// tail.
export type MemoryEntry = {
  id: string;
  text: string;
  tags?: string[];
  source: "user" | "assistant";
  createdAt: number;
  updatedAt: number;
};

// memoryBlockLimit caps the injected characters. Past it the newest entries are
// injected and the rest are COUNTED, never silently dropped — a model told it has
// the whole list when it has a slice answers confidently from the slice.
export const MEMORY_BLOCK_LIMIT = 8000;

// memoryBlock renders the system-prompt section. Every line carries its id
// because that id is the only handle memory_save/memory_delete have for editing
// an existing entry. Returns "" when there is nothing to inject, so an empty
// memory adds no prefix at all.
//
// Two orders, deliberately. The BUDGET is spent newest-updated first, so a block
// that has to be cut loses its stalest facts. The RENDER order is by creation,
// oldest first, which makes the block append-only: a new memory lands at the end
// and every line above it stays byte-identical. Sorting the output by recency
// instead reshuffles the whole block on every write, moving the point where the
// prompt diverges from the KV cache up to line one for a one-line change.
export function memoryBlock(mems: MemoryEntry[]): string {
  if (!mems.length) return "";
  const byRecency = [...mems].sort((a, b) => (b.updatedAt || 0) - (a.updatedAt || 0));
  const kept: MemoryEntry[] = [];
  let used = 0;
  let dropped = 0;
  for (const m of byRecency) {
    const text = (m.text || "").trim();
    if (!text) continue;
    const line = `- [${m.id}] ${text.replace(/\n+/g, " ")}`;
    if (used + line.length > MEMORY_BLOCK_LIMIT) {
      dropped++;
      continue;
    }
    used += line.length + 1;
    kept.push(m);
  }
  if (!kept.length) return "";
  // createdAt is unix SECONDS, so two memories saved in the same second tie —
  // break on id to keep the order deterministic across renders.
  kept.sort((a, b) => (a.createdAt || 0) - (b.createdAt || 0) || a.id.localeCompare(b.id));
  const lines = kept.map((m) => `- [${m.id}] ${(m.text || "").trim().replace(/\n+/g, " ")}`);
  const tail = dropped
    ? `\n(${dropped} older ${dropped === 1 ? "memory is" : "memories are"} not shown here - say so if the answer depends on something you might be missing.)`
    : "";
  return `What you remember about this user (from earlier conversations):\n${lines.join("\n")}${tail}`;
}

export const MEMORY_SAVE_TOOL: ToolDef = {
  type: "function",
  function: {
    name: "memory_save",
    description:
      "Remember one durable fact about this user across conversations. Save when the user asks you to remember something, or when they state a lasting preference, constraint or detail about themselves or their setup that would change your answers later. Save it the moment you see it - you do not need to check your memory block first, because a fact you already know in roughly these words is folded into the entry that holds it rather than stored twice. Different wording can slip past that, so if the result of a save names an existing memory that says something close, deal with it immediately: same fact means saving one combined text onto the older memory's id and deleting the new one; two different facts means keeping both. Do NOT save anything only relevant to this conversation, or anything you can look up (their config - use quartermaster_inspect). If a memory in your block is now wrong or outdated, pass its `id` to REPLACE it rather than saving a second, conflicting version. One fact per call, written in the third person about the user, self-contained enough to make sense with no conversation around it.",
    parameters: {
      type: "object",
      properties: {
        text: {
          type: "string",
          description:
            "The fact, in one or two sentences (max 800 characters). Self-contained: 'Prefers metric units' not 'yes, that one'. Include the reason when it is what makes the fact useful.",
        },
        id: {
          type: "string",
          description:
            "Only to correct or update an existing memory: its id, exactly as shown in square brackets in your memory block. Omit to save a new one. An id that matches nothing is an error, not a new memory.",
        },
        tags: {
          type: "array",
          items: { type: "string" },
          description: "Optional short labels for the user's own filtering, e.g. ['hardware'].",
        },
      },
      required: ["text"],
    },
  },
};

export const MEMORY_DELETE_TOOL: ToolDef = {
  type: "function",
  function: {
    name: "memory_delete",
    description:
      "Forget one memory. Use when the user asks you to forget something, or when a memory has been superseded by a fact that cannot coexist with it. To correct a memory, prefer memory_save with its id - that keeps one entry instead of deleting and re-adding.",
    parameters: {
      type: "object",
      properties: {
        id: {
          type: "string",
          description: "The id of the memory to delete, exactly as shown in square brackets in your memory block.",
        },
      },
      required: ["id"],
    },
  },
};

export const MEMORY_TOOLS: ToolDef[] = [MEMORY_SAVE_TOOL, MEMORY_DELETE_TOOL];
