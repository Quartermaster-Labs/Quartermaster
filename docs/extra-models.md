<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Hand-written models (other engines)

Quartermaster generates a model entry for every file it finds, but only for engines it knows (llama.cpp, vLLM, stable-diffusion.cpp and the other built-in backends). To serve a model through **any other OpenAI-compatible server**, write its entry by hand under `settings.extraModels` in `quartermaster-generate.yaml` (in the config folder). Each entry is copied into the generated config on every regeneration, so it survives restarts and model-folder changes, and it behaves like any other model: it shows in the catalog and the playground, loads on the first request, and swaps with your other models.

These entries can only be written in the file, never from the dashboard. An entry is a command line Quartermaster runs on your machine, so it belongs to whoever can edit that file.

## The rules

- An entry is a normal config model block: `cmd`, `proxy`, `checkEndpoint`, `env`, `ttl`, `healthCheckTimeout`, `capabilities` and the rest. Use `${PORT}` and Quartermaster fills in a free port.
- `cmd` is required, and so is **`estVramGB`** (or `estRamGB` for a server that runs on the CPU only). Quartermaster cannot measure a foreign engine, and this number is what makes it unload your other models to make room.
- An entry with a mistyped key, no `cmd`, no estimate, or a name another model already uses is left out. The reason is written into the generated `config.yaml` as `# SKIPPED extra model "name": ...`, so check there if a model does not appear.
- `cmd` is **not run through a shell**: `cd`, `&&` and pipes do not work. Write the program and every file it is given as absolute paths.

## Example: Strata

[Strata](https://github.com/Niko1221/Strata) runs the Qwen3.8-Flash-Next family on consumer GPUs and serves an OpenAI-compatible API. Install it and run its setup once (`START-HERE.bat --setup` on Windows, `./setup.sh --setup` on Linux), which writes a `strata-<model>.json` into the Strata folder. Then start its server directly, so Quartermaster can give it a port:

```yaml
settings:
  extraModels:
    strata:
      cmd: >
        C:/Strata/.venv/Scripts/python.exe C:/Strata/serve/server.py
        --engine strata --config C:/Strata/strata-coder-iq1_m.json
        --port ${PORT}
      proxy: http://127.0.0.1:${PORT}
      checkEndpoint: /health
      healthCheckTimeout: 300
      estVramGB: 24
      ttl: 1800
      env: ["STRATA_ALLOWED_HOSTS=my-pc"]
      # set up with images? let the playground offer attachments:
      # capabilities: { in: [text, image], out: [text] }
```

On Linux the program is `/path/to/Strata/.venv/bin/python` and the paths change to match. Save the file and `strata` appears in the model list within a few seconds: Quartermaster watches the file and reloads on its own, no restart needed.

- **`--config` must be an absolute path.** Strata reads it from the folder it was started in, and Quartermaster starts it in its own.
- **Loading takes 1.5-2 minutes.** Strata answers `/health` only once the model is in memory, so `healthCheckTimeout: 300` gives it room. Do not add Strata's `--lazy` or `--idle-unload`: the first makes the health check pass before anything is loaded, and Quartermaster's `ttl` already unloads it when idle.
- **`estVramGB` should be your whole card.** Strata fills free VRAM with its expert cache and needs the GPU to itself, so this makes Quartermaster unload every other model before starting it. Stopping it ends the server and its engine together and frees the card.
- **"Host ... is not allowed (DNS rebinding protection)"**: Strata only answers to host names it knows, and Quartermaster passes on the name the client used. Requests to `localhost` or an IP address work; from another machine using this PC's name they fail with a 403. Put the names your clients use in `STRATA_ALLOWED_HOSTS` (comma-separated), or in `"allowed_hosts"` in the Strata json.
- Strata accepts any model name, so requests sent to `strata` reach it unchanged.
- **Reasoning levels (Qwen3.8):** add `reasoningEffort: [xhigh, medium, low]` under `capabilities`. A client's `reasoning_effort` is only passed on to a model that lists its levels, so without it the setting is silently dropped.
- **Also start Strata by hand sometimes?** Quartermaster gives its copy a port of its own, so nothing stops a hand-started Strata and Quartermaster's from running at once and loading the model onto the card twice. Point `cmd` at a small wrapper script instead of `server.py`: it exits with an error when your hand-started Strata is already up (its usual port is taken), and otherwise starts `server.py` with the same arguments. The wrapper is still a program, not a shell line, so run a script through Strata's own python.

Strata keeps a page of its own on running behind llama-swap, which Quartermaster grew out of: [docs/LLAMA_SWAP.md](https://github.com/Niko1221/Strata/blob/main/docs/LLAMA_SWAP.md).
