# Third-party chat templates

## `qwen-fixed-chat-template.jinja`

Source: https://huggingface.co/froggeric/Qwen-Fixed-Chat-Templates
Author: froggeric
License: Apache-2.0 (inherited from Qwen)
Version pinned: `qwen3.6-froggeric-v21.3`

Fixes prefix-cache-breaking history mutation and agentic tool-call bugs in the
official Qwen 3.5/3.6 chat templates.

**Not applied automatically.** Chat templates are user-managed: nothing in
`internal/autogen` selects a template for a model, and `--chat-template-file` is
emitted only when a model has a `chatTemplateFile` override. This file ships as a
sample you can point such an override at — note that it has no `reasoning_effort`
logic, so a model using it advertises no effort ladder.
