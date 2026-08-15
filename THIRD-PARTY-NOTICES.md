# Third-party notices

quartermaster itself is MIT-licensed (see [`LICENSE.md`](LICENSE.md)). It began
as a fork of [llama-swap](https://github.com/mostlygeek/llama-swap) (MIT,
Copyright © 2024 Benson Wong), whose copyright notice is retained there.

This file lists what a quartermaster **binary** carries inside it, plus the
assets it downloads and the separate programs it launches. Licenses below were
read from each dependency's own license file / `package.json` at the pinned
version; where a project's own text is required for redistribution, follow the
link.

---

## Bundled in the binary — Go modules

Direct dependencies from [`go.mod`](go.mod). Transitive dependencies keep their
own licenses; `go mod download` fetches the full set with license files intact.

| Module | Version | License |
| --- | --- | --- |
| github.com/billziss-gh/golib | v0.2.0 | MIT |
| github.com/charmbracelet/bubbles | v1.0.0 | MIT |
| github.com/charmbracelet/bubbletea | v1.3.10 | MIT |
| github.com/charmbracelet/lipgloss | v1.1.0 | MIT |
| github.com/fxamacker/cbor/v2 | v2.9.1 | MIT |
| github.com/getlantern/systray | v1.2.2 | Apache-2.0 |
| github.com/gin-gonic/gin | v1.10.0 | MIT |
| github.com/google/jsonschema-go | v0.4.3 | MIT |
| github.com/klauspost/compress | v1.18.5 | BSD-3-Clause |
| github.com/shirou/gopsutil/v4 | v4.26.4 | BSD-3-Clause |
| github.com/stretchr/testify | v1.11.1 | MIT |
| github.com/tidwall/gjson | v1.18.0 | MIT |
| github.com/tidwall/sjson | v1.2.5 | MIT |
| golang.org/x/crypto | v0.45.0 | BSD-3-Clause |
| golang.org/x/net | v0.47.0 | BSD-3-Clause |
| golang.org/x/sys | v0.41.0 | BSD-3-Clause |
| gopkg.in/yaml.v3 | v3.0.1 | MIT (with Apache-2.0 portions — see the module's LICENSE) |

## Bundled in the binary — web UI

The Svelte app in [`ui-svelte/`](ui-svelte/) is built into `internal/server/ui_dist`
and embedded with `go:embed`, so these ship inside the binary as compiled assets.

| Package | Version | License |
| --- | --- | --- |
| chart.js | 4.5.1 | MIT |
| highlight.js | 11.11.1 | BSD-3-Clause |
| katex | 0.16.47 | MIT |
| lucide-svelte | 0.563.0 | ISC |
| mermaid | 11.16.1 | MIT |
| pdfjs-dist | 6.2.108 | Apache-2.0 |
| rehype-katex | 7.0.1 | MIT |
| rehype-stringify | 10.0.1 | MIT |
| remark-gfm | 4.0.1 | MIT |
| remark-math | 6.0.0 | MIT |
| remark-parse | 11.0.0 | MIT |
| remark-rehype | 11.1.2 | MIT |
| svelte-spa-router | 4.0.2 | MIT |
| unified | 11.0.5 | MIT |
| unist-util-visit | 5.1.0 | MIT |

Built with Svelte 5 (MIT), Vite 8 (MIT), Tailwind CSS 4 (MIT) and TypeScript
(Apache-2.0). Build tools are not redistributed in the binary.

**Apache-2.0 notice (pdfjs-dist):** Copyright © Mozilla Foundation, licensed
under the Apache License, Version 2.0. A copy of the license is available at
<https://www.apache.org/licenses/LICENSE-2.0>. The upstream `LICENSE` and
`NOTICE` files ship inside the `pdfjs-dist` package.

## Model weights and templates

| Asset | Source | License |
| --- | --- | --- |
| `titlegen-flan-t5-small-q8_0.gguf` — the chat-title model, downloaded on first use (see [`internal/server/assets/README.md`](internal/server/assets/README.md)) | [google/flan-t5-small](https://huggingface.co/google/flan-t5-small), converted to GGUF and quantized to Q8_0 | Apache-2.0 |
| `templates/qwen-fixed-chat-template.jinja` | [froggeric/Qwen-Fixed-Chat-Templates](https://huggingface.co/froggeric/Qwen-Fixed-Chat-Templates) | Apache-2.0 (inherited from Qwen) — see [`templates/CREDITS.md`](templates/CREDITS.md) |

**Apache-2.0 notice (flan-t5-small):** Copyright © Google LLC, licensed under
the Apache License, Version 2.0, available at
<https://www.apache.org/licenses/LICENSE-2.0>. The GGUF conversion is a
derivative of those weights and carries the same license.

## Programs quartermaster launches (not bundled, not modified)

These are separate executables you install — via the Windows installer, the
unified Docker image, or by hand. quartermaster starts them as subprocesses; it
does not link against them, and each keeps its own license.

| Program | Project | License |
| --- | --- | --- |
| `llama-server`, `llama-completion` | [ggml-org/llama.cpp](https://github.com/ggml-org/llama.cpp) | MIT |
| `sd-server` | [leejet/stable-diffusion.cpp](https://github.com/leejet/stable-diffusion.cpp) | MIT |
| `whisper-server` | [ggml-org/whisper.cpp](https://github.com/ggml-org/whisper.cpp) | MIT |
| `tts-server` | [ServeurpersoCom/qwentts.cpp](https://github.com/ServeurpersoCom/qwentts.cpp) | MIT |
| `yt-dlp` | [yt-dlp/yt-dlp](https://github.com/yt-dlp/yt-dlp) | Unlicense |
| `realesrgan-ncnn-vulkan` | [xinntao/Real-ESRGAN-ncnn-vulkan](https://github.com/xinntao/Real-ESRGAN-ncnn-vulkan) | MIT |

The **models** you run are yours to license: quartermaster neither ships nor
relicenses model weights, and a downloaded GGUF carries whatever terms its
publisher set (some are not redistributable, some restrict commercial use).

---

Spotted something wrong or missing? Open an issue — license mistakes are bugs.
