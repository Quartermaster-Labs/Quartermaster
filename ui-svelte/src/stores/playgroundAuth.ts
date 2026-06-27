import { writable } from "svelte/store";

// Current playground user (username), or null when logged out. Not serious auth
// — it only keys server-side chat history per user.
export const me = writable<string | null>(null);

// The port the standalone playground app is served on (from /api/mode); used by
// the dashboard sidebar to link out to it. Empty when not configured.
export const playgroundPort = writable<string>("");

export async function checkMe(): Promise<void> {
  try {
    const r = await fetch("/auth/me");
    if (r.ok) {
      const j = await r.json();
      me.set(j.username ?? null);
    } else {
      me.set(null);
    }
  } catch {
    me.set(null);
  }
}

export async function login(username: string, password: string): Promise<void> {
  const r = await fetch("/auth/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password }),
  });
  if (!r.ok) throw new Error((await r.text()).trim() || "login failed");
  const j = await r.json();
  me.set(j.username);
}

export async function logout(): Promise<void> {
  try {
    await fetch("/auth/logout", { method: "POST" });
  } catch {
    // best-effort; clear locally regardless
  }
  me.set(null);
}
