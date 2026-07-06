# Third-party chat templates

## `qwen-fixed-chat-template.jinja`

Source: https://huggingface.co/froggeric/Qwen-Fixed-Chat-Templates
Author: froggeric
License: Apache-2.0 (inherited from Qwen)
Version pinned: `qwen3.6-froggeric-v21.3`

Fixes prefix-cache-breaking history mutation and agentic tool-call bugs in the
official Qwen 3.5/3.6 chat templates. Applied automatically by `internal/autogen`
to Qwen 3.5/3.6 GGUF models (see `autogen.qwenFixedChatTemplate` in `generate.go`).
