# Security policy

## Reporting a vulnerability

Report privately through GitHub's
[**Report a vulnerability**](https://github.com/Quartermaster-Labs/quartermaster/security/advisories/new)
form. Please don't open a public issue for a security bug.

Include what you'd want if you were fixing it: the version (`/api/version` or
`quartermaster -version`), how quartermaster was started (flags, especially
`-listen` / `-playground-port` / `-admin-allow` / `-admin-open`), and the request
or steps that reproduce it.

You'll get an acknowledgement within a few days. This is a small project without
a paid security team, so please don't expect an enterprise SLA — but real issues
get fixed and credited.

## Supported versions

The latest release only. Fixes ship in a new release rather than as patches to
older tags.

## What quartermaster's security model actually is

quartermaster runs inference servers on your machine and gives you a web UI to
configure them. Knowing where the boundaries are is most of the story:

**The admin surface is your machine.** The dashboard, the ops endpoints
(`/logs`, `/unload`, `/running`, `/upstream/…`) and the config editor
(`/api/settings`, `/api/models/…`, `/api/backends/…`, `/api/hub/…`) carry **no
authentication at all**. They are gated by *remote address*: loopback only, plus
anything you add with `-admin-allow <cidr>`. This is deliberate — API keys gate
the inference API, so enabling them can never lock you out of your own UI.

By design, anything that reaches the admin surface can run commands as you: the
config editor sets model launch command lines, and the backend installer
registers executables. **Treat admin access as equivalent to a shell.** Widening
it with `-admin-allow` or `-admin-open` means trusting every host you let in.

**The inference API is what you expose.** Bind it beyond loopback and gate it
with `apiKeys` (optionally scoped per model with `apiKeyModels`). The admin
surface stays restricted to this host automatically when you do.

**The playground login is not a security boundary.** It exists to keep chat
history separate per person on a shared machine. Passwords are hashed, and the
session cookie is HMAC-signed so it can't be forged — but anyone who can reach
the port can create an account, and a playground user reaches the inference API,
the chat tools' outbound fetches, and the model list. Don't put it on the open
internet; a tailnet or LAN you trust is the intended deployment.

### In scope

- Reaching an admin/config/ops endpoint from an address the admin gate should
  have refused
- Bypassing `apiKeys` or per-key model scoping on the inference API
- Reading or writing another playground user's chats, media or preferences
- Forging a playground session cookie
- Path traversal out of the models root, the media directory or the data dir
- SSRF past the `fetch_page` guard into loopback/link-local ranges
- Anything that turns a *model's* output (tool arguments, generated text) into
  command execution or file access

### Not in scope

- Admin endpoints being unauthenticated on loopback, or reachable from a range
  you added with `-admin-allow` / `-admin-open` — that's the documented design
- The playground login being registration-open or lacking rate limiting
- Anything requiring an attacker who can already write your config file, your
  models folder, or the binaries quartermaster launches
- Vulnerabilities in llama.cpp, stable-diffusion.cpp or other backends —
  report those upstream
- Denial of service by loading a model that doesn't fit in VRAM
