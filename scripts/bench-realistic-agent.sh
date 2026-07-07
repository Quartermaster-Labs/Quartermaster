#!/usr/bin/env bash
# Bench pp/tg under a REALISTIC growing multi-turn agent session -- NOT a
# single flat fresh-prompt burst like bench-ubatch-100k.sh /
# bench-mtp-vs-dflash.sh.
#
# Those benches send one big flat prompt to a just-spawned, empty-KV server
# (cache_prompt false) -- fixed per-request overhead (thread wake, paging in
# the CPU-offloaded expert weight blocks) amortizes over thousands of
# tokens, so pp tok/s looks great (675 tok/s on qwen3.6-35b-a3b-100k @
# ub1024). A real agent (pi) resends the FULL growing chat history each
# turn with cache_prompt reuse -- prompt_n per turn is only the new suffix
# (tens-hundreds of tokens), so the same fixed overhead dominates and
# measured pp tok/s craters (200-300 in practice). This script reproduces
# that shape: one persistent server, one growing conversation, N turns,
# each resending the full accumulated history so far.
#
# Turn content is pulled from real .go files in this repo, framed as tool
# output -- varied/non-repetitive, closer to what a coding agent actually
# sends than a repeated filler sentence.
#
# Hits quartermaster's OWN proxy (default :1250), same as pi does -- real
# router swap-in, real reverse-proxy path, not a standalone llama-server
# bypassing quartermaster entirely.
#
# usage:
#   scripts/bench-realistic-agent.sh [--model KEY] [--qm-url URL]
#                                     [--turns N] [--reply-tokens N]
set -euo pipefail

model_key="qwen3.6-35b-a3b-ud-q4_k_s-100k"
qm_url="http://127.0.0.1:1250"
turns=20
reply_tokens=300
apikey=""
out="$(dirname "$0")/bench-realistic-agent-results.csv"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --model) model_key="$2"; shift 2 ;;
    --qm-url) qm_url="$2"; shift 2 ;;
    --turns) turns="$2"; shift 2 ;;
    --reply-tokens) reply_tokens="$2"; shift 2 ;;
    --api-key) apikey="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

host="${qm_url#http://}"
authheader=()
[[ -n "$apikey" ]] && authheader=(-H "Authorization: Bearer ${apikey}")
trap 'curl -sS -m 30 "${authheader[@]}" -X POST "${qm_url}/api/models/unload/${model_key}" >/dev/null 2>&1 || true' EXIT

echo "timestamp,turn,prompt_n,prompt_ms,prompt_per_second,predicted_n,predicted_ms,predicted_per_second,total_ms,error" > "$out"
csv_row() { # ts turn pn pms pps dn dms tgs total_ms err
  printf '%s,%s,%s,%s,%s,%s,%s,%s,%s,"%s"\n' "$@" >> "$out"
}

# --- real, varied "tool output" chunks from the repo, not repeated filler ---
mapfile -t src_files < <(find "$(dirname "$0")/.." -name '*.go' -not -path '*/vendor/*' | shuf -n "$turns")
[[ ${#src_files[@]} -gt 0 ]] || { echo "no .go source files found for turn content" >&2; exit 1; }

# messages array lives on disk, not in a shell var -- an accumulated 20-turn
# history plus source-code chunks can run into tens of KB, past what's safe
# to round-trip through argv (Windows command-line limits). --slurpfile /
# --rawfile below read it straight off disk instead.
histfile=$(mktemp)
echo '[{"role":"system","content":"You are a careful coding assistant working inside a large repository. Use the tool output provided to answer concisely."}]' > "$histfile"

echo "== realistic-agent bench $model_key : turns=$turns reply_tokens=$reply_tokens via $qm_url =="

for ((turn=1; turn<=turns; turn++)); do
  f="${src_files[$(( (turn-1) % ${#src_files[@]} ))]}"
  uf=$(mktemp)
  { printf 'Tool result -- read %s:\n```go\n' "$f"; sed -n '1,60p' "$f"; printf '```\nBased on this, continue the task.'; } > "$uf"
  jq -n --slurpfile hist "$histfile" --rawfile c "$uf" '$hist[0] + [{role:"user",content:$c}]' > "${histfile}.next"
  mv "${histfile}.next" "$histfile"
  rm -f "$uf"

  payf=$(mktemp)
  jq -n --slurpfile hist "$histfile" --arg m "$model_key" --argjson n "$reply_tokens" \
    '{model:$m, messages:$hist[0], max_tokens:$n, temperature:0, stream:false, cache_prompt:true}' > "$payf"

  ts=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  t0=$(date +%s%N)
  if ! resp=$(curl -sS -m 1800 -w '\n%{http_code}' -X POST "http://${host}/v1/chat/completions" \
        "${authheader[@]}" -H 'Content-Type: application/json' --data-binary "@${payf}"); then
    rm -f "$payf"; csv_row "$ts" "$turn" "" "" "" "" "" "" "" "curl failed"
    echo "[turn $turn] curl failed" >&2; continue
  fi
  rm -f "$payf"
  t1=$(date +%s%N)
  total_ms=$(( (t1 - t0) / 1000000 ))
  status="${resp##*$'\n'}"; body="${resp%$'\n'*}"
  if [[ "$status" != "200" ]]; then
    em=$(echo "$body" | tr '"' "'" | tr -d '\n')
    csv_row "$ts" "$turn" "" "" "" "" "" "" "$total_ms" "HTTP ${status}: ${em}"
    echo "[turn $turn] HTTP $status: $em" >&2; continue
  fi

  bodyf=$(mktemp); printf '%s' "$body" > "$bodyf"
  t=$(jq -c '.timings // empty' "$bodyf")
  replyf=$(mktemp); jq -r '.choices[0].message.content // ""' "$bodyf" > "$replyf"
  rm -f "$bodyf"
  jq -n --slurpfile hist "$histfile" --rawfile c "$replyf" '$hist[0] + [{role:"assistant",content:$c}]' > "${histfile}.next"
  mv "${histfile}.next" "$histfile"
  rm -f "$replyf"

  if [[ -z "$t" ]]; then
    csv_row "$ts" "$turn" "" "" "" "" "" "" "$total_ms" "no timings"
    echo "[turn $turn] no timings, total=${total_ms}ms" >&2
    continue
  fi
  pn=$(echo "$t" | jq -r '.prompt_n // ""'); pms=$(echo "$t" | jq -r '.prompt_ms // ""')
  pps=$(echo "$t" | jq -r '.prompt_per_second // ""'); dn=$(echo "$t" | jq -r '.predicted_n // ""')
  dms=$(echo "$t" | jq -r '.predicted_ms // ""'); tgs=$(echo "$t" | jq -r '.predicted_per_second // ""')
  csv_row "$ts" "$turn" "$pn" "$pms" "$pps" "$dn" "$dms" "$tgs" "$total_ms" ""
  echo "[turn $turn] prompt_n=${pn} pp=${pps} tok/s  predicted_n=${dn} tg=${tgs} tok/s  total=${total_ms}ms"
done

rm -f "$histfile"

echo
echo "== trend: pp tok/s and prompt_n should drop as history grows =="
awk -F',' 'NR==1{next} {print}' "$out" | column -t -s','

echo
echo "results: $out"
