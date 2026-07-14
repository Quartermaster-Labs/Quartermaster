#!/usr/bin/env bash
# Sweep --spec-draft-n-max (draft tokens proposed per verify step) on a
# spec-decode model to find the TG sweet spot. We baked in n-max=2 for MTP by
# assumption; this measures it. Higher n over-drafts (accept rate falls faster
# than chain length grows, wasting GPU verify compute); too low leaves speedup
# on the table -- the peak is model/quant/ctx specific.
#
# Same harness as bench-ubatch-100k.sh: spawns ONE standalone llama-server per
# n-max via scripts/qm-adhoc-spawn.sh, which asks a running quartermaster's
# PUT /api/models/{model}/adhoc-cmd for a properly VRAM-sized command (only the
# --spec-draft-n-max field is overridden; spec type / kv / offload inherit the
# model's live override). Never touches quartermaster's own router. Sequential,
# killed between variants.
#
# IMPORTANT: free quartermaster's VRAM first (POST /api/models/unload) -- a
# standalone spawn on top of a loaded model OOMs.
#
# Spec decode only helps DECODE. pp (prefill) is draft-irrelevant, kept as a
# sanity probe. The answer lives in the tg / reason TG columns.
#
# usage:
#   scripts/bench-spec-nmax.sh [--model KEY] [--qm-url URL] [--port N]
#                              [--reps N] [--nmax-list "1 2 3 4 5 6"]
set -euo pipefail

model_key="qwen3.6-27b-ud-q4_k_xl-100k"
qm_url="http://127.0.0.1:1250"
port="18100"
reps=3
nmax_list="1 2 3 4 5 6"
out="$(dirname "$0")/bench-spec-nmax-results.csv"
adhoc="$(dirname "$0")/qm-adhoc-spawn.sh"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --model) model_key="$2"; shift 2 ;;
    --qm-url) qm_url="$2"; shift 2 ;;
    --port) port="$2"; shift 2 ;;
    --reps) reps="$2"; shift 2 ;;
    --nmax-list) nmax_list="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

host="127.0.0.1:${port}"
trap '"$adhoc" --kill --port "$port" >/dev/null 2>&1 || true' EXIT

if [[ ! -f "$out" ]]; then
  echo "timestamp,variant,profile,rep,prompt_n,prompt_ms,prompt_per_second,predicted_n,predicted_ms,predicted_per_second,draft_accept,error" > "$out"
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
prompt_reason="Find all positive integers n such that n^2 + 2n + 2024 is a perfect square. Then, separately, compute the remainder when 7^1000 is divided by 1000. Show every step of your reasoning in full detail, checking your work as you go."

run_one() {
  local variant=$1 profile=$2 prompt=$3 n_predict=$4 rep=$5
  local pf payf resp status body ts ep
  pf=$(mktemp); printf '%s' "$prompt" > "$pf"
  payf=$(mktemp)
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
  # Draft accept rate: llama-server exposes it as draft_n / draft_n_accepted when
  # spec decode is active; blank if this build/timings block omits it.
  acc=$(echo "$t" | jq -r 'if (.draft_n_accepted and .draft_n and .draft_n>0) then (.draft_n_accepted/.draft_n) else "" end // ""')
  echo "${ts},${variant},${profile},${rep},${pn},${pms},${pps},${dn},${dms},${tgs},${acc}," >> "$out"
  echo "[$variant $profile rep$rep] pp=${pps} tok/s (n=${pn}) tg=${tgs} tok/s (n=${dn}) accept=${acc}"
}

echo "== bench $model_key : n-max=[$nmax_list] port=$port reps=$reps =="
echo "== (free quartermaster VRAM first: POST ${qm_url}/api/models/unload) =="

for nmax in $nmax_list; do
  v="nmax${nmax}"
  echo
  echo "=== variant: $v ==="
  spawn_out=$("$adhoc" --model "$model_key" --port "$port" --qm-url "$qm_url" --spec-draft-n-max "$nmax" 2>&1) || {
    echo "[$v] spawn failed (likely VRAM OOM):" >&2
    echo "$spawn_out" >&2
    echo "$(date -u +%FT%TZ),${v},startup,0,,,,,,,,\"spawn failed\"" >> "$out"
    continue
  }
  echo "  $spawn_out"
  run_one "$v" warmup "$prompt_tg" 16 0
  for ((r=1; r<=2; r++)); do run_one "$v" pp_8k "$prompt_pp" 8 "$r"; done
  for ((r=1; r<=reps; r++)); do run_one "$v" tg "$prompt_tg" 256 "$r"; done
  for ((r=1; r<=2; r++)); do run_one "$v" reason "$prompt_reason" 1024 "$r"; done
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
    for (k in tg_n){split(k,a,","); printf "%-8s %-8s tg=%.1f tok/s%s\n", a[1],a[2], tg_s[k]/tg_n[k], (ac_n[k]?sprintf("  accept=%.2f",ac_s[k]/ac_n[k]):"")}
    for (k in pp_n){split(k,a,","); printf "%-8s %-8s pp=%.1f tok/s\n", a[1],a[2], pp_s[k]/pp_n[k]}
  }' "$out" | sort

echo
echo "results: $out"
