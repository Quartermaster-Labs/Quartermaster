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

async function credentials(path: string, username: string, password: string): Promise<void> {
  const r = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password }),
  });
  if (!r.ok) throw new Error((await r.text()).trim() || "request failed");
  const j = await r.json();
  me.set(j.username);
}

// Sign in to an existing account. An unknown username is an error here —
// creating one goes through signup().
export async function login(username: string, password: string): Promise<void> {
  return credentials("/auth/login", username, password);
}

// Create an account and sign into it. 409 when the name is taken.
export async function signup(username: string, password: string): Promise<void> {
  return credentials("/auth/signup", username, password);
}

export async function logout(): Promise<void> {
  try {
    await fetch("/auth/logout", { method: "POST" });
  } catch {
    // best-effort; clear locally regardless
  }
  me.set(null);
}
