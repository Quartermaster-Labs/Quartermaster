#!/usr/bin/env bash
# Compare llama-server BACKEND BUILDS (e.g. ROCm/HIP vs Vulkan) on the SAME
# model, through the LIVE quartermaster proxy, with a big prefill + long
# generation so both pp (prompt reprocess) and tg (token gen) land side by side.
#
# Uses the optional per-request backend override: each /completion carries an
# `X-QM-Backend: <id>` header, so quartermaster loads the model on that backend's
# exe (real router, same VRAM group, visible on the dashboard) instead of its
# configured one. Switching backend id triggers a normal swap -- no standalone
# spawn, no manual kill, nothing bypasses the scheduler.
#
# Discover installed backend ids with:  curl -s $QM_URL/api/backends | jq .
#
# usage:
#   scripts/bench-backends.sh --backends "llama <vulkan-id>" \
#       [--model KEY] [--qm-url URL] [--prompt-words N] [--gen N] [--reps N] [--out CSV]
set -euo pipefail

model_key="qwen3.6-27b-uncensored-heretic-v2-native-mtp-preserved-q4_k_m-64k"
backends=""
qm_url="http://127.0.0.1:1250"
api_key="${QM_API_KEY:-}"
prompt_words=23000   # ~30k tokens of English filler (report actual prompt_n)
gen=10000            # n_predict
reps=1
out="$(dirname "$0")/bench-backends-results.csv"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --model) model_key="$2"; shift 2 ;;
    --backends) backends="$2"; shift 2 ;;
    --qm-url) qm_url="$2"; shift 2 ;;
    --prompt-words) prompt_words="$2"; shift 2 ;;
    --gen) gen="$2"; shift 2 ;;
    --reps) reps="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    --api-key) api_key="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

[[ -n "$backends" ]] || { echo "--backends \"id1 id2 ...\" required (see $qm_url/api/backends)" >&2; exit 1; }

if [[ ! -f "$out" ]]; then
  echo "timestamp,backend,rep,prompt_n,prompt_ms,prompt_per_second,predicted_n,predicted_ms,predicted_per_second,error" > "$out"
fi

filler="The quick brown fox jumps over the lazy dog near the riverbank while clouds drift slowly across an autumn sky, and the town below hums with the quiet rhythm of everyday life. "
build_prompt() {
  local target_words=$1 words_per_rep=32 s="" i
  local reps_needed=$(( (target_words + words_per_rep - 1) / words_per_rep ))
  for ((i=0; i<reps_needed; i++)); do s+="$filler"; done
  printf '%s' "$s"
}
big_prompt="$(build_prompt "$prompt_words")"

run_one() {
  local backend=$1 prompt=$2 n_predict=$3 rep=$4
  local promptfile payloadfile resp status body ts
  promptfile=$(mktemp); printf '%s' "$prompt" > "$promptfile"
  payloadfile=$(mktemp)
  jq -n --arg model "$model_key" --rawfile prompt "$promptfile" --argjson n_predict "$n_predict" \
    '{model: $model, prompt: $prompt, n_predict: $n_predict, cache_prompt: false, temperature: 0}' > "$payloadfile"
  rm -f "$promptfile"
  ts=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  local auth=(); [[ -n "$api_key" ]] && auth=(-H "Authorization: Bearer ${api_key}")
  if ! resp=$(curl -sS -m 3600 -w '\n%{http_code}' -X POST "${qm_url}/completion" \
        -H 'Content-Type: application/json' -H "X-QM-Backend: ${backend}" \
        "${auth[@]}" --data-binary "@${payloadfile}"); then
    rm -f "$payloadfile"
    echo "${ts},${backend},${rep},,,,,,,\"curl failed\"" >> "$out"
    echo "[$backend rep $rep] curl failed" >&2; return
  fi
  rm -f "$payloadfile"
  status="${resp##*$'\n'}"; body="${resp%$'\n'*}"
  if [[ "$status" != "200" ]]; then
    echo "${ts},${backend},${rep},,,,,,,\"HTTP ${status}\"" >> "$out"
    echo "[$backend rep $rep] HTTP $status: $(echo "$body" | tr '"\n' \"' '\" | head -c 200)" >&2; return
  fi
  local t; t=$(echo "$body" | jq -c '.timings // empty')
  if [[ -z "$t" ]]; then
    echo "${ts},${backend},${rep},,,,,,,\"no .timings\"" >> "$out"
    echo "[$backend rep $rep] no .timings" >&2; return
  fi
  local pn pms pps dn dms dps
  pn=$(echo "$t"|jq -r '.prompt_n//""');   pms=$(echo "$t"|jq -r '.prompt_ms//""');   pps=$(echo "$t"|jq -r '.prompt_per_second//""')
  dn=$(echo "$t"|jq -r '.predicted_n//""'); dms=$(echo "$t"|jq -r '.predicted_ms//""'); dps=$(echo "$t"|jq -r '.predicted_per_second//""')
  echo "${ts},${backend},${rep},${pn},${pms},${pps},${dn},${dms},${dps}," >> "$out"
  echo "[$backend rep $rep] pp=${pps} tok/s (n=${pn})  tg=${dps} tok/s (n=${dn})"
}

for backend in $backends; do
  echo "==== backend: $backend ===="
  echo "-- warmup (triggers swap/load on this backend) --"
  run_one "$backend" "Write one sentence about the sea." 8 0
  for ((r=1; r<=reps; r++)); do run_one "$backend" "$big_prompt" "$gen" "$r"; done
done

echo
echo "== summary =="
awk -F',' 'NR==1{next} $6!=""{pp[$2]+=$6;ppn[$2]++} $9!=""{tg[$2]+=$9;tgn[$2]++}
  END{for(b in ppn)printf "%-40s pp=%.1f tg=%.1f tok/s\n",b,pp[b]/ppn[b],(tgn[b]?tg[b]/tgn[b]:0)}' "$out"
echo "results: $out"
