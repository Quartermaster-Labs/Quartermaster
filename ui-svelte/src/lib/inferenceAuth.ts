// Auto-attaches an API key to the local Playground's inference requests so the
// in-browser playground keeps working when keys gate the inference API. The
// dashboard is local/admin, so it reads the key straight from the open
// /api/apikeys list — preferring a full-access (unscoped) key so the playground
// can reach every model. The server auto-manages a hidden built-in full-access
// key whenever user keys are all scoped, so an unscoped key is always present
// when auth is on. No keys (or auth off) => no header, requests go through
// unauthenticated as before.

let key: string | null = null;

export async function refreshInferenceKey(): Promise<void> {
  try {
    const res = await fetch("/api/apikeys");
    if (!res.ok) {
      key = null;
      return;
    }
    const keys: { name: string; key: string; models: string[] }[] = (await res.json()) || [];
    const unscoped = keys.find((k) => !k.models || k.models.length === 0);
    key = (unscoped ?? keys[0])?.key ?? null;
  } catch {
    key = null;
  }
}

// Merge the bearer auth header into a headers object when a key is cached.
export function inferenceHeaders(base: Record<string, string> = {}): Record<string, string> {
  return key ? { ...base, Authorization: `Bearer ${key}` } : base;
}
