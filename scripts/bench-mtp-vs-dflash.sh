#!/usr/bin/env bash
# Bench MTP vs DFlash (vs no-spec baseline) speculative decoding for one model.
#
# Spawns a STANDALONE llama-server per spec variant, sequentially (never two
# alive at once), via scripts/qm-adhoc-spawn.sh: it asks a running
# quartermaster's PUT /api/models/{model}/adhoc-cmd for a command properly
# re-sized for that variant's spec flags (auto-recomputed --n-cpu-moe etc.,
# not a raw config.yaml scrape), spawns it standalone, and never touches
# quartermaster's own router. Pure ad-hoc/one-time load -- nothing persists.
#
# This does NOT touch a running quartermaster's loaded model. Free your VRAM
# first (unload whatever quartermaster has loaded) so the standalone server
# has room.
#
# usage:
#   scripts/bench-mtp-vs-dflash.sh [--model KEY] [--qm-url URL] [--port N]
#                                  [--reps N] [--variants "none mtp dflash"]
set -euo pipefail

model_key="qwen3.6-35b-a3b-ud-q4_k_xl"
qm_url="http://127.0.0.1:8080"
port="18099"
reps=3
variants="none mtp dflash"
out="$(dirname "$0")/bench-mtp-vs-dflash-results.csv"
adhoc="$(dirname "$0")/qm-adhoc-spawn.sh"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --model) model_key="$2"; shift 2 ;;
    --qm-url) qm_url="$2"; shift 2 ;;
    --port) port="$2"; shift 2 ;;
    --reps) reps="$2"; shift 2 ;;
    --variants) variants="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

host="127.0.0.1:${port}"
trap '"$adhoc" --kill --port "$port" >/dev/null 2>&1 || true' EXIT

# Pull the DFlash draft path via quartermaster's own config API (gives the
# real gguf path regardless of what spec backend is currently active), then
# glob the model's dir for a *DFlash*/*dflash* sidecar.
main_gguf=$(curl -sf -m 10 "${qm_url}/api/models/${model_key}/config" | jq -r '.gguf // empty')
main_gguf="${main_gguf//\\//}"  # normalize Windows backslashes: safe for both bash glob and JSON
draft_md=""
if [[ -n "$main_gguf" ]]; then
  draft_md=$(ls "$(dirname "$main_gguf")"/*[Dd][Ff]lash*.gguf 2>/dev/null | head -1 || true)
fi

# Sets the global $args array (not echoed -- --extra-args carries embedded
# spaces that word-splitting a printed string would mangle).
set_variant_args() {
  args=()
  case "$1" in
    none)   args=(--spec none) ;;
    mtp)    args=(--spec draft-mtp --spec-draft-n-max 2) ;;
    dflash)
      [[ -n "${draft_md:-}" ]] || { echo "ERR: no DFlash draft gguf found for dflash variant" >&2; return 1; }
      # n-max default 5: measured sweet spot on Qwen3.6-35B-A3B (own sweep of
      # 3/4/5/6 -- reasoning tg jumps ~15% at n=5 vs n=3/4 and ties n=6, while
      # n=5 also edges out n=6 on creative tg). Higher (12/15) over-drafts
      # (block diffusion proposes a full block; excess is rejected, wasting GPU).
      args=(--spec draft-dflash --spec-draft-n-max "${DFLASH_NMAX:-5}" --extra-args "-md ${draft_md} -ngld 99") ;;
    ngram)  # model-free n-gram lookup from context; no draft gguf. good for repetitive/code tokens.
      args=(--spec ngram-map-k4v --spec-default --spec-ngram-size-n 16 --spec-ngram-size-m 24 --spec-ngram-min-hits 1) ;;
    *) echo "unknown variant: $1" >&2; return 1 ;;
  esac
}

# --- bench plumbing (mirrors bench-spec.sh) ---------------------------------
if [[ ! -f "$out" ]]; then
  echo "timestamp,variant,profile,rep,prompt_n,prompt_ms,prompt_per_second,predicted_n,predicted_ms,predicted_per_second,accept_rate,error" > "$out"
fi

filler="The quick brown fox jumps over the lazy dog near the riverbank while clouds drift slowly across an autumn sky, and the town below hums with the quiet rhythm of everyday life. "
build_prompt() {
  local target_words=$1 words_per_rep=32 s="" i
  local reps_needed=$(( (target_words + words_per_rep - 1) / words_per_rep ))
  for ((i=0; i<reps_needed; i++)); do s+="$filler"; done
  printf '%s' "$s"
}
prompt_pp="$(build_prompt 8000)"
prompt_tg="Write a detailed story about a robot learning to paint, at least 400 words."
# Reasoning prompt: hard competition math -> long greedy chain-of-thought, the
# workload DFlash's block-diffusion draft is trained for (z-lab benchmark = math500).
prompt_reason="Find all positive integers n such that n^2 + 2n + 2024 is a perfect square. Then, separately, compute the remainder when 7^1000 is divided by 1000. Show every step of your reasoning in full detail, checking your work as you go."

run_one() {
  local variant=$1 profile=$2 prompt=$3 n_predict=$4 rep=$5
  local pf payf resp status body ts ep
  pf=$(mktemp); printf '%s' "$prompt" > "$pf"
  payf=$(mktemp)
  # reason profile -> chat endpoint (applies the reasoning chat template so
  # thinking engages, matching z-lab's DFlash benchmark: greedy + thinking).
  # Everything else -> raw /completion.
  if [[ "$profile" == reason* ]]; then
    ep="/v1/chat/completions"
    jq -n --arg m "$model_key" --rawfile p "$pf" --argjson n "$n_predict" \
      '{model:$m, messages:[{role:"user",content:$p}], max_tokens:$n, temperature:0, stream:false}' > "$payf"
  else
    ep="/completion"
    jq -n --arg m "$model_key" --rawfile p "$pf" --argjson n "$n_predict" \
      '{model:$m, prompt:$p, n_predict:$n, cache_prompt:false, temperature:0}' > "$payf"
  fi
  rm -f "$pf"
  ts=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  if ! resp=$(curl -sS -m 1800 -w '\n%{http_code}' -X POST "http://${host}${ep}" \
        -H 'Content-Type: application/json' --data-binary "@${payf}"); then
    rm -f "$payf"; echo "${ts},${variant},${profile},${rep},,,,,,,,\"curl failed\"" >> "$out"
    echo "[$variant $profile rep$rep] curl failed" >&2; return
  fi
  rm -f "$payf"
  status="${resp##*$'\n'}"; body="${resp%$'\n'*}"
  if [[ "$status" != "200" ]]; then
    local em; em=$(echo "$body" | tr '"' "'" | tr -d '\n')
    echo "${ts},${variant},${profile},${rep},,,,,,,,\"HTTP ${status}: ${em}\"" >> "$out"
    echo "[$variant $profile rep$rep] HTTP $status: $em" >&2; return
  fi
  local t; t=$(echo "$body" | jq -c '.timings // empty')
  [[ -n "$t" ]] || { echo "${ts},${variant},${profile},${rep},,,,,,,,\"no timings\"" >> "$out"; return; }
  local pn pms pps dn dms tgs acc
  pn=$(echo "$t" | jq -r '.prompt_n // ""'); pms=$(echo "$t" | jq -r '.prompt_ms // ""')
  pps=$(echo "$t" | jq -r '.prompt_per_second // ""'); dn=$(echo "$t" | jq -r '.predicted_n // ""')
  dms=$(echo "$t" | jq -r '.predicted_ms // ""'); tgs=$(echo "$t" | jq -r '.predicted_per_second // ""')
  # /completion JSON carries no draft stats; acceptance is scraped from the
  # server log per-variant after the run (see main loop). Leave column blank.
  echo "${ts},${variant},${profile},${rep},${pn},${pms},${pps},${dn},${dms},${tgs},," >> "$out"
  echo "[$variant $profile rep$rep] pp=${pps} tok/s (n=${pn}) tg=${tgs} tok/s (n=${dn})"
}

echo "== bench $model_key : variants=[$variants] port=$port reps=$reps =="
[[ -n "${draft_md:-}" ]] && echo "== dflash draft: $draft_md ==" || echo "== WARN: no DFlash gguf found; dflash variant will be skipped =="

for v in $variants; do
  set_variant_args "$v" || { echo "skip variant $v"; continue; }
  echo
  echo "=== variant: $v ==="
  spawn_out=$("$adhoc" --model "$model_key" --port "$port" --qm-url "$qm_url" "${args[@]}" 2>&1) || {
    echo "[$v] spawn failed:" >&2
    echo "$spawn_out" >&2
    echo "$(date -u +%FT%TZ),${v},startup,0,,,,,,,,\"spawn failed\"" >> "$out"
    continue
  }
  echo "  $spawn_out"
  srvlog="$(dirname "$adhoc")/adhoc-srv-${port}.log"
  run_one "$v" warmup "$prompt_tg" 16 0
  if [[ "${PP_ONLY:-0}" == 1 ]]; then
    for ((r=1; r<=3; r++)); do run_one "$v" pp_8k "$prompt_pp" 8 "$r"; done
  elif [[ "${REASON_ONLY:-0}" == 1 ]]; then
    for ((r=1; r<=2; r++)); do run_one "$v" reason "$prompt_reason" 1024 "$r"; done
  else
    for ((r=1; r<=2; r++)); do run_one "$v" pp_8k "$prompt_pp" 8 "$r"; done
    for ((r=1; r<=reps; r++)); do run_one "$v" tg "$prompt_tg" 256 "$r"; done
    for ((r=1; r<=2; r++)); do run_one "$v" reason "$prompt_reason" 1024 "$r"; done
  fi
  # Draft acceptance lives in the server log (the /completion JSON has no draft
  # stats), not in the HTTP timings. Scrape the last reported rate for this variant.
  acc=$(grep -oE 'draft acceptance = [0-9.]+ \( *[0-9]+ accepted / *[0-9]+ generated\), mean len = *[0-9.]+' "$srvlog" 2>/dev/null | tail -1) || true
  [[ -n "$acc" ]] && echo "[$v] ${acc}"
  "$adhoc" --kill --port "$port"
done

echo
echo "== summary (avg per variant/profile) =="
awk -F',' '
  NR==1{next}
  { v=$2; p=$3
    if ($7!=""){pp_s[v","p]+=$7; pp_n[v","p]++}
    if ($10!=""){tg_s[v","p]+=$10; tg_n[v","p]++}
    if ($11!=""){ac_s[v","p]+=$11; ac_n[v","p]++} }
  END{
    for (k in tg_n){split(k,a,","); printf "%-8s %-8s tg=%.1f tok/s", a[1],a[2], tg_s[k]/tg_n[k]; if(ac_n[k])printf " accept=%.2f", ac_s[k]/ac_n[k]; print ""}
    for (k in pp_n){split(k,a,","); printf "%-8s %-8s pp=%.1f tok/s\n", a[1],a[2], pp_s[k]/pp_n[k]}
  }' "$out" | sort

echo
echo "results: $out"
