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
export function memoryBlock(mems: MemoryEntry[]): string {
  if (!mems.length) return "";
  // Newest first: if the block has to be cut, the cut falls on the stalest facts.
  const sorted = [...mems].sort((a, b) => (b.updatedAt || 0) - (a.updatedAt || 0));
  const lines: string[] = [];
  let used = 0;
  let dropped = 0;
  for (const m of sorted) {
    const text = (m.text || "").trim();
    if (!text) continue;
    const line = `- [${m.id}] ${text.replace(/\n+/g, " ")}`;
    if (used + line.length > MEMORY_BLOCK_LIMIT) {
      dropped++;
      continue;
    }
    used += line.length + 1;
    lines.push(line);
  }
  if (!lines.length) return "";
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
      "Remember one durable fact about this user across conversations. Save when the user asks you to remember something, or when they state a lasting preference, constraint or detail about themselves or their setup that would change your answers later. Do NOT save: anything only relevant to this conversation, anything you can look up (their config - use quartermaster_inspect), or something already in your memory block. If a memory in that block is now wrong or outdated, pass its `id` to REPLACE it rather than saving a second, conflicting version. One fact per call, written in the third person about the user, self-contained enough to make sense with no conversation around it.",
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
