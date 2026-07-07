#!/usr/bin/env bash
# Bench -ub (micro-batch) sizes on the 100k-ctx tier of a model.
#
# Long-ctx entries (IsLong in compute-buffer-vram-estimate) get forced to
# ub=512 by autogen to keep the compute buffer small at c=102400. This sweeps
# ub=512(live default)/1024/2048 to see if raising ub is worth the VRAM
# (-b is emitted decoupled from -ub, auto-clamped >=ub, no manual override
# needed). KV cache is preallocated for the full -c regardless of prompt
# length, so even a pp_8k probe stresses the same VRAM envelope the live
# 100k config reserves.
#
# Spawns ONE standalone llama-server at a time via scripts/qm-adhoc-spawn.sh,
# which asks a running quartermaster's PUT /api/models/{model}/adhoc-cmd for a
# properly re-sized command (auto-recomputes --n-cpu-moe etc. for the new -ub,
# unlike a raw config.yaml cmd scrape) and never touches quartermaster's own
# router. Pure ad-hoc/one-time load -- nothing persists. Sequential, killed
# between variants.
#
# usage:
#   scripts/bench-ubatch-100k.sh [--model KEY] [--qm-url URL] [--port N]
#                                [--reps N] [--ub-list "512 1024 2048"]
set -euo pipefail

model_key="qwen3.6-35b-a3b-ud-q4_k_s-100k"
qm_url="http://127.0.0.1:8080"
port="18098"
reps=3
ub_list="512 1024 2048"
out="$(dirname "$0")/bench-ubatch-100k-results.csv"
adhoc="$(dirname "$0")/qm-adhoc-spawn.sh"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --model) model_key="$2"; shift 2 ;;
    --qm-url) qm_url="$2"; shift 2 ;;
    --port) port="$2"; shift 2 ;;
    --reps) reps="$2"; shift 2 ;;
    --ub-list) ub_list="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

host="127.0.0.1:${port}"
trap '"$adhoc" --kill --port "$port" >/dev/null 2>&1 || true' EXIT

# --- bench plumbing (mirrors bench-mtp-vs-dflash.sh) -------------------------
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
  local pn pms pps dn dms tgs
  pn=$(echo "$t" | jq -r '.prompt_n // ""'); pms=$(echo "$t" | jq -r '.prompt_ms // ""')
  pps=$(echo "$t" | jq -r '.prompt_per_second // ""'); dn=$(echo "$t" | jq -r '.predicted_n // ""')
  dms=$(echo "$t" | jq -r '.predicted_ms // ""'); tgs=$(echo "$t" | jq -r '.predicted_per_second // ""')
  echo "${ts},${variant},${profile},${rep},${pn},${pms},${pps},${dn},${dms},${tgs},," >> "$out"
  echo "[$variant $profile rep$rep] pp=${pps} tok/s (n=${pn}) tg=${tgs} tok/s (n=${dn})"
}

echo "== bench $model_key : ub-list=[$ub_list] port=$port reps=$reps =="

for ub in $ub_list; do
  v="ub${ub}"
  echo
  echo "=== variant: $v ==="
  spawn_out=$("$adhoc" --model "$model_key" --port "$port" --qm-url "$qm_url" --ub "$ub" 2>&1) || {
    echo "[$v] spawn failed (likely VRAM OOM at this ub):" >&2
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
    if ($10!=""){tg_s[v","p]+=$10; tg_n[v","p]++} }
  END{
    for (k in tg_n){split(k,a,","); printf "%-8s %-8s tg=%.1f tok/s\n", a[1],a[2], tg_s[k]/tg_n[k]}
    for (k in pp_n){split(k,a,","); printf "%-8s %-8s pp=%.1f tok/s\n", a[1],a[2], pp_s[k]/pp_n[k]}
  }' "$out" | sort

echo
echo "results: $out"
