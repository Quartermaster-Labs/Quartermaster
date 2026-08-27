<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Playground accounts

The playground runs on its own port (`-playground-port`) and is gated behind a per-user account, separate from the open dashboard.

- **Sign in or create an account.** The login screen has two modes: **Sign in** for an existing account and **Create account** for a new one. Signing up is a deliberate, separate step - a login form that registered whatever you typed turned every typo into a new empty account and made "wrong password" indistinguishable from "no such user". Usernames are 1-40 characters (letters, digits, `_`, `-`) and a new password must be at least 6 characters.
- **Per-user storage.** Your chat, image and speech conversations, your playground preferences, your memories, and any media you generated are saved server-side under your account. Nothing is shared between users, and generated images/audio are stored as files in your own folder rather than inline in the history.
- **Your session lasts 30 days.** Login is kept in an HttpOnly, HMAC-signed cookie, so closing the browser doesn't sign you out - and nobody can become another user just by editing a cookie.
- **Passwords are hashed** (bcrypt) in `playground-data/users.json`; accounts created by older builds that stored plaintext are upgraded automatically the next time that user signs in.
- **Still not real authentication.** It exists to keep separate people's chats apart on a trusted machine or LAN - there is no password reset, no roles, and no rate limiting on the login. Don't expose the playground port to the internet, and don't reuse a password that matters.
- **Log out** from the side rail, next to your username (it asks for confirmation).
