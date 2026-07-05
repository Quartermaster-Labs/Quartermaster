#!/usr/bin/env bash
# Bench PP/TG throughput of a quartermaster-served model across spec-decode
# configs. Toggle --spec-type on the model yourself between runs (dashboard
# config editor), then re-run with a different --label. Rows append to one
# CSV so baseline/mtp/ngram land side by side.
set -euo pipefail

label=""
host="127.0.0.1:1250"
model="qwen3.6-35b-a3b-ud-q4_k_xl-100k"
reps=3
out="$(dirname "$0")/bench-spec-results.csv"
apikey="${QM_API_KEY:-}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --label) label="$2"; shift 2 ;;
    --host) host="$2"; shift 2 ;;
    --model) model="$2"; shift 2 ;;
    --reps) reps="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    --apikey) apikey="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

if [[ -z "$label" ]]; then
  echo "usage: $0 --label <baseline|mtp|ngram|...> [--host host:port] [--model name] [--reps N] [--out file.csv]" >&2
  exit 1
fi

if [[ ! -f "$out" ]]; then
  echo "timestamp,label,profile,rep,prompt_n,prompt_ms,prompt_per_second,predicted_n,predicted_ms,predicted_per_second,error" > "$out"
fi

# Filler paragraph repeated to build long prompts without an external file.
filler="The quick brown fox jumps over the lazy dog near the riverbank while clouds drift slowly across an autumn sky, and the town below hums with the quiet rhythm of everyday life. "

build_prompt() {
  local target_words=$1
  local words_per_rep=32
  local reps_needed=$(( (target_words + words_per_rep - 1) / words_per_rep ))
  local s=""
  for ((i=0; i<reps_needed; i++)); do s+="$filler"; done
  printf '%s' "$s"
}

prompt_pp_20k="$(build_prompt 20000)"
prompt_pp_60k="$(build_prompt 60000)"
prompt_tg_short="Write a short story about a robot learning to paint."

run_one() {
  local profile=$1 prompt=$2 n_predict=$3 rep=$4
  local promptfile payload
  promptfile=$(mktemp)
  printf '%s' "$prompt" > "$promptfile"
  local payloadfile
  payloadfile=$(mktemp)
  jq -n --arg model "$model" --rawfile prompt "$promptfile" --argjson n_predict "$n_predict" \
    '{model: $model, prompt: $prompt, n_predict: $n_predict, cache_prompt: false, temperature: 0}' > "$payloadfile"
  rm -f "$promptfile"

  local resp status ts
  ts=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  local authheader=()
  [[ -n "$apikey" ]] && authheader=(-H "Authorization: Bearer ${apikey}")
  if ! resp=$(curl -sS -m 1800 -w '\n%{http_code}' -X POST "http://${host}/completion" \
        -H 'Content-Type: application/json' "${authheader[@]}" --data-binary "@${payloadfile}"); then
    rm -f "$payloadfile"
    echo "${ts},${label},${profile},${rep},,,,,,,\"curl request failed\"" >> "$out"
    echo "[$profile rep $rep] curl request failed" >&2
    return
  fi
  rm -f "$payloadfile"
  status="${resp##*$'\n'}"
  body="${resp%$'\n'*}"

  if [[ "$status" != "200" ]]; then
    local errmsg
    errmsg=$(echo "$body" | tr '"' "'" | tr -d '\n')
    echo "${ts},${label},${profile},${rep},,,,,,,\"HTTP ${status}: ${errmsg}\"" >> "$out"
    echo "[$profile rep $rep] HTTP $status: $errmsg" >&2
    return
  fi

  local t
  t=$(echo "$body" | jq -c '.timings // empty')
  if [[ -z "$t" ]]; then
    echo "${ts},${label},${profile},${rep},,,,,,,\"no .timings in response\"" >> "$out"
    echo "[$profile rep $rep] no .timings in response" >&2
    return
  fi

  local prompt_n prompt_ms pp_s predicted_n predicted_ms tg_s
  prompt_n=$(echo "$t" | jq -r '.prompt_n // ""')
  prompt_ms=$(echo "$t" | jq -r '.prompt_ms // ""')
  pp_s=$(echo "$t" | jq -r '.prompt_per_second // ""')
  predicted_n=$(echo "$t" | jq -r '.predicted_n // ""')
  predicted_ms=$(echo "$t" | jq -r '.predicted_ms // ""')
  tg_s=$(echo "$t" | jq -r '.predicted_per_second // ""')

  echo "${ts},${label},${profile},${rep},${prompt_n},${prompt_ms},${pp_s},${predicted_n},${predicted_ms},${tg_s}," >> "$out"
  echo "[$profile rep $rep] pp=${pp_s} tok/s (n=${prompt_n}) tg=${tg_s} tok/s (n=${predicted_n})"
}

echo "== warmup (cold load if not already loaded) =="
run_one "warmup" "$prompt_tg_short" 16 0

echo "== PP-heavy 20k =="
for ((r=1; r<=reps; r++)); do run_one "pp_20k" "$prompt_pp_20k" 8 "$r"; done

echo "== PP-heavy 60k =="
for ((r=1; r<=reps; r++)); do run_one "pp_60k" "$prompt_pp_60k" 8 "$r"; done

echo "== TG-heavy =="
for ((r=1; r<=reps; r++)); do run_one "tg" "$prompt_tg_short" 256 "$r"; done

echo
echo "== summary (avg per profile, this run's label=$label) =="
awk -F',' -v lbl="$label" '
  NR==1 { next }
  $2==lbl {
    profile=$3
    if ($7 != "") { pp_sum[profile]+=$7; pp_n[profile]++ }
    if ($10 != "") { tg_sum[profile]+=$10; tg_n[profile]++ }
  }
  END {
    for (p in pp_n) printf "%-10s avg pp_tok_s=%.1f (n=%d)\n", p, pp_sum[p]/pp_n[p], pp_n[p]
    for (p in tg_n) printf "%-10s avg tg_tok_s=%.1f (n=%d)\n", p, tg_sum[p]/tg_n[p], tg_n[p]
  }
' "$out"

echo
echo "results appended to: $out"
