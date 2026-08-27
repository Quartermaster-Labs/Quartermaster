<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Updating Quartermaster

Quartermaster can update itself (Windows release builds only). When a newer version is available, the **Sidebar** shows an update control.

- Clicking it downloads and launches the release installer, then the server shuts down gracefully so the installer can replace it.
- Both the dashboard and playground briefly drop while it restarts - reload the page once the installer finishes.
