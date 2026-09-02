<script lang="ts">
  import { onMount } from "svelte";
  import { Eye, EyeOff } from "lucide-svelte";
  import { login, signup, anyAccounts } from "../stores/playgroundAuth";

  // Two panes, one component: the fields are identical and the only differences
  // are the endpoint, the confirm box and the copy.
  let mode = $state<"login" | "signup">("login");
  let username = $state("");
  let password = $state("");
  let confirm = $state("");
  let reveal = $state(false);
  let error = $state("");
  let busy = $state(false);

  const signingUp = $derived(mode === "signup");
  const mismatch = $derived(signingUp && confirm.length > 0 && confirm !== password);
  const tooShort = $derived(signingUp && password.length > 0 && password.length < 6);
  const ready = $derived(
    username.trim().length > 0 &&
      password.length > 0 &&
      (!signingUp || (password.length >= 6 && confirm === password)),
  );

  // First launch has no accounts at all, so asking for a password nobody has set
  // is a dead end. Open on the sign-up pane instead. Only flips the default —
  // once the user has touched the switch, whatever they picked stands.
  let touched = $state(false);
  onMount(async () => {
    const any = await anyAccounts();
    if (any === false && !touched) mode = "signup";
  });

  function switchMode(next: "login" | "signup") {
    touched = true;
    mode = next;
    error = "";
    confirm = "";
  }

  async function submit(e: Event) {
    e.preventDefault();
    if (!ready) return;
    busy = true;
    error = "";
    try {
      if (signingUp) await signup(username.trim(), password);
      else await login(username.trim(), password);
    } catch (err) {
      error = err instanceof Error ? err.message : signingUp ? "sign up failed" : "login failed";
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
      {signingUp
        ? "Create an account to keep your chat history separate from other users on this machine."
        : "Sign in to pick up your chat history."}
    </p>
    <input
      class="px-3 py-2 rounded-md border border-card-border bg-background text-txtmain focus:outline-none focus:border-primary"
      placeholder="Username"
      autocomplete="username"
      bind:value={username}
    />
    <div class="relative">
      <input
        type={reveal ? "text" : "password"}
        class="w-full pl-3 pr-10 py-2 rounded-md border border-card-border bg-background text-txtmain focus:outline-none focus:border-primary"
        placeholder="Password"
        autocomplete={signingUp ? "new-password" : "current-password"}
        bind:value={password}
      />
      <button
        type="button"
        tabindex="-1"
        aria-label={reveal ? "Hide password" : "Show password"}
        title={reveal ? "Hide password" : "Show password"}
        class="absolute right-1 top-1/2 -translate-y-1/2 p-1.5 rounded text-txtsecondary hover:text-txtmain transition-colors"
        onclick={() => (reveal = !reveal)}
      >
        {#if reveal}
          <EyeOff size={16} />
        {:else}
          <Eye size={16} />
        {/if}
      </button>
    </div>
    {#if signingUp}
      <input
        type={reveal ? "text" : "password"}
        class="px-3 py-2 rounded-md border border-card-border bg-background text-txtmain focus:outline-none focus:border-primary"
        placeholder="Confirm password"
        autocomplete="new-password"
        bind:value={confirm}
      />
    {/if}
    {#if tooShort}
      <p class="text-xs text-txtsecondary">Use at least 6 characters.</p>
    {:else if mismatch}
      <p class="text-xs text-red-500">Passwords do not match.</p>
    {/if}
    {#if error}
      <p class="text-xs text-red-500">{error}</p>
    {/if}
    <button
      type="submit"
      disabled={busy || !ready}
      class="px-3 py-2 rounded-md bg-primary text-white font-medium hover:opacity-90 transition-opacity disabled:opacity-40"
    >
      {#if busy}
        {signingUp ? "Creating account…" : "Signing in…"}
      {:else}
        {signingUp ? "Create account" : "Sign in"}
      {/if}
    </button>
    <button
      type="button"
      class="text-xs text-txtsecondary hover:text-txtmain transition-colors"
      onclick={() => switchMode(signingUp ? "login" : "signup")}
    >
      {signingUp ? "Already have an account? Sign in" : "No account? Create one"}
    </button>
    <p class="pt-1 border-t border-card-border text-[0.7rem] leading-relaxed text-txtsecondary">
      This is a local account on this machine only. It keeps each person's chats, settings and
      generated media separate; nothing is sent anywhere and there is no password recovery.
    </p>
  </form>
</div>
