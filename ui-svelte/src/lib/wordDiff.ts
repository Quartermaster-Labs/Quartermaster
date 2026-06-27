// Word-level diff for the rewrite view. LCS over whitespace/word tokens so the
// side-by-side comparison can highlight exactly what changed.
//
// ponytail: O(n*m) DP — fine for prose (a few thousand tokens). For pathological
// inputs we bail to a single replace op (see CAP) instead of allocating a huge table.

export type DiffOp = { type: "equal" | "insert" | "delete"; value: string };

const CAP = 2_000_000; // n*m ceiling (~1400×1400 tokens) before falling back to plain replace

function tokenize(text: string): string[] {
  // Keep words and runs of whitespace as separate tokens so spacing/newlines survive.
  return text.match(/\S+|\s+/g) ?? [];
}

function merge(ops: DiffOp[]): DiffOp[] {
  const out: DiffOp[] = [];
  for (const op of ops) {
    const last = out[out.length - 1];
    if (last && last.type === op.type) last.value += op.value;
    else out.push({ ...op });
  }
  return out;
}

export function diffWords(a: string, b: string): DiffOp[] {
  const A = tokenize(a);
  const B = tokenize(b);
  const n = A.length;
  const m = B.length;

  if (n === 0 && m === 0) return [];
  if (n * m > CAP) {
    // Too large to diff cheaply — show it as a wholesale replacement.
    const ops: DiffOp[] = [];
    if (a) ops.push({ type: "delete", value: a });
    if (b) ops.push({ type: "insert", value: b });
    return ops;
  }

  // LCS length table, built bottom-up.
  const dp: number[][] = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      dp[i][j] = A[i] === B[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    }
  }

  const ops: DiffOp[] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (A[i] === B[j]) {
      ops.push({ type: "equal", value: A[i] });
      i++;
      j++;
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      ops.push({ type: "delete", value: A[i] });
      i++;
    } else {
      ops.push({ type: "insert", value: B[j] });
      j++;
    }
  }
  while (i < n) ops.push({ type: "delete", value: A[i++] });
  while (j < m) ops.push({ type: "insert", value: B[j++] });
  return merge(ops);
}
