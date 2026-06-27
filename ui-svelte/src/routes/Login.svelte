<script lang="ts">
  import { login } from "../stores/playgroundAuth";

  let username = $state("");
  let password = $state("");
  let error = $state("");
  let busy = $state(false);

  async function submit(e: Event) {
    e.preventDefault();
    if (!username.trim() || !password) return;
    busy = true;
    error = "";
    try {
      await login(username.trim(), password);
    } catch (err) {
      error = err instanceof Error ? err.message : "login failed";
    } finally {
      busy = false;
    }
  }
</script>

<div class="h-screen flex items-center justify-center bg-background p-4">
  <form
    onsubmit={submit}
    class="w-80 flex flex-col gap-3 p-6 rounded-lg border border-card-border bg-surface shadow-lg"
  >
    <div class="font-mono text-[0.65rem] uppercase tracking-[0.2em] text-primary">Quartermaster</div>
    <h1 class="font-mono text-lg font-bold text-txtmain">Playground</h1>
    <p class="text-xs text-txtsecondary">
      Sign in to keep your chat history. A new username registers automatically.
    </p>
    <input
      class="px-3 py-2 rounded-md border border-card-border bg-background text-txtmain focus:outline-none focus:border-primary"
      placeholder="Username"
      autocomplete="username"
      bind:value={username}
    />
    <input
      type="password"
      class="px-3 py-2 rounded-md border border-card-border bg-background text-txtmain focus:outline-none focus:border-primary"
      placeholder="Password"
      autocomplete="current-password"
      bind:value={password}
    />
    {#if error}
      <p class="text-xs text-red-500">{error}</p>
    {/if}
    <button
      type="submit"
      disabled={busy || !username.trim() || !password}
      class="px-3 py-2 rounded-md bg-primary text-white font-medium hover:opacity-90 transition-opacity disabled:opacity-40"
    >
      {busy ? "Signing in…" : "Sign in"}
    </button>
  </form>
</div>
