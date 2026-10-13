<!-- Generated from internal/server/wiki_articles.json by ui-svelte/scripts/wiki-docs.mjs. Edit the JSON, not this file. -->

# Decision models (System One)

A **decision model** answers questions about a situation with **probabilities instead of prose**. You give it a *state* (what is going on) and a set of typed questions; it returns, for every question, how likely each answer is. It is the llama.cpp **System One** feature, served on `/v1/systemone` in the TypeSafe request shape. Use one where an app needs a classification it can branch on (route this ticket, is this message spam, how urgent is it) and a chat reply would have to be parsed and could not be trusted to stay on the menu.

**Question types:**
- **Choice**: pick one of several named options (at least 2), each with an optional description. The answer names the winner and gives every option's probability.
- **Score**: rate on an ordered scale of 2 to 10 levels, each level described. The answer is the expected score plus the probability of each level.
- **True / false** (`noul`): the probability that a statement holds. Optional `true`/`false` descriptions say what each side means.

**Getting one:** the model browser has a **Decision** tab (Browse > Decision) that lists GGUF repos tagged `decision-model`, `typed-decisions` or `system-one`, Kev-4B among them. These repos are kept out of the LLM tab, since many of them also declare `text-generation`. Download it like any other model.

**How it is recognised:** by the GGUF header, not the filename. A model whose metadata carries `<arch>.decision.type` (`kev`, `laya`, `clef`, `vllm-d3`, `lfm2-d1`, ...) is generated as a decision model: it lands under the **Decision** category on the Models page, is advertised on `/v1/models` with `capabilities.decisions` and `decision_type`, and loads, swaps and shares the VRAM budget like any other llama-server model. Autogen launches it without `--embeddings` (llama-server turns on what it needs by itself), caps the window at 8192 tokens, and for the types that read the whole prompt in one batch (`laya`, `clef`, `vllm-d3`, `lfm2-d1-omni`) sets `-b`/`-ub` to the context size. An image projector next to the gguf is paired as for an LLM, which lets the model take images.

**Playground:** the **Decision** tab. Pick a model, write the state (plain text, or JSON, which is sent as JSON), build the questions, and run. Each answer is drawn as a probability bar per option or per score level. Question sets are saved per user, so a set you use often is there next time. With a projector paired, an **Attach** button adds images.

**API:** `POST /v1/systemone`, with the model in the body like every other route, and gated by API keys and their model scopes.

```
curl http://127.0.0.1:1250/v1/systemone \
  -H "Authorization: Bearer qm-..." -H "Content-Type: application/json" \
  -d '{"model":"kev-4b","state":"My order arrived broken and I want my money back.",
       "questions":{
         "intent":{"type":"choice","instructions":"What does the customer want?",
                   "criteria":{"refund":"Money back","replace":"A new item","info":null}},
         "urgency":{"type":"score","instructions":"How urgent is this?",
                    "criteria":["not urgent","soon","right now"]},
         "angry":{"type":"noul","instructions":"The customer is angry."}}}'
```

The reply carries one entry per question id under `answers`: a choice gives `choice` plus `probabilities`, a score gives `score`, `probabilities` per level and a `legend`, and a true/false gives `noul`, the probability of true. `files` (a list of image data URLs) is accepted only by a model with a projector.

**Model quirks** are the server's to enforce, and its 400 is shown verbatim. `lfm2-d1`, `lfm2-d1-omni` and `vllm-d3` accept a null state, and `vllm-d3` accepts a question with no instructions, so the playground leaves both checks to llama-server.

**Needs a recent llama.cpp.** The route arrived upstream in llama.cpp **b11361** (PR #29818). Forks number their own releases: lemonade's ROCm build has it from **b1343**. An older llama-server either refuses the gguf or loads it as a plain LLM and 404s every request. Quartermaster checks the backend binary (without launching it) and, when a decision model points at a build without the route, warns before loading it: **llama.cpp needs an update**, with a *Load anyway* button. The fix is updating the llama.cpp backend (see Backends), or pinning a newer one on that model.
