#!/usr/bin/env bash
# Same deep-prefill pp bench as bench-kv-quant.sh, but through the LIVE
# quartermaster instance (port 1250) instead of a standalone spawn -- tests
# the real routed path: quartermaster's own spawn args, proxy/metrics/
# slotcache middleware overhead, whatever process state it's actually in.
#
# WARNING: this is a real inference call against your live quartermaster.
# It WILL load/swap-in this model on the shared instance, evicting whatever
# is currently loaded there. Free anything you care about first.
#
# Captures the model's live llama-server log via quartermaster's own
# /logs/stream/<model> SSE endpoint (open, no API key) while the request
# runs, then parses print_timing lines the same way as the standalone bench.
#
# usage: scripts/bench-kv-quant-live.sh [--host 127.0.0.1:1250] [--model KEY] [--key APIKEY] [--words N]
set -euo pipefail

host="127.0.0.1:1250"
model="qwen3.6-35b-a3b-ud-q4_k_xl-100k"
apikey="qm-7f9ead612acb74ed4e6d23f0c758d129dbad9c434b4f17bb"
words=24000
out="$(dirname "$0")/bench-kv-quant-live.log"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --host) host="$2"; shift 2 ;;
    --model) model="$2"; shift 2 ;;
    --key) apikey="$2"; shift 2 ;;
    --words) words="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

if ! curl -sf -m 3 "http://${host}/health" >/dev/null 2>&1; then
  echo "FATAL: quartermaster not reachable at ${host}" >&2
  exit 1
fi

filler="The quick brown fox jumps over the lazy dog near the riverbank while clouds drift slowly across an autumn sky, and the town below hums with the quiet rhythm of everyday life. "
build_prompt() {
  local target_words=$1 words_per_rep=32 s="" i
  local reps_needed=$(( (target_words + words_per_rep - 1) / words_per_rep ))
  for ((i=0; i<reps_needed; i++)); do s+="$filler"; done
  printf '%s' "$s"
}
deep_prompt="$(build_prompt "$words")"

echo "== live bench: model=$model host=$host words=$words =="
echo "== WARNING: this loads/evicts on your live quartermaster instance =="

# Stream this model's llama-server log in the background for the duration of
# the request (open endpoint, no key needed).
: > "$out"
curl -sN -m 1800 "http://${host}/logs/stream/${model}" >> "$out" 2>&1 &
log_pid=$!
cleanup() { kill "$log_pid" 2>/dev/null || true; }
trap cleanup EXIT

sleep 1  # let the stream attach before the request lands

pf=$(mktemp); printf '%s' "$deep_prompt" > "$pf"
payf=$(mktemp)
jq -n --arg m "$model" --rawfile p "$pf" '{model:$m, prompt:$p, n_predict:1, cache_prompt:false, temperature:0}' > "$payf"
rm -f "$pf"

resp=$(curl -sS -m 1800 -w '\n%{http_code}' -X POST "http://${host}/completion" \
  -H "Authorization: Bearer ${apikey}" \
  -H 'Content-Type: application/json' --data-binary "@${payf}")
rm -f "$payf"
status="${resp##*$'\n'}"; body="${resp%$'\n'*}"
echo "status=$status"
if [[ "$status" != "200" ]]; then
  echo "$body"
  exit 1
fi
echo "$body" | jq -c '.timings // empty'

sleep 1  # let trailing log lines flush before we stop the stream
kill "$log_pid" 2>/dev/null || true
trap - EXIT

echo "print_timing curve:"
awk '
  /print_timing:/ {
    match($0, /n_tokens *= *([0-9]+)/, nt)
    match($0, /t *= *([0-9.]+) *s/, tt)
    if (nt[1] != "" && tt[1] != "") {
      n=nt[1]+0; t=tt[1]+0
      marg = (t>pt) ? (n-pn)/(t-pt) : 0
      printf "    n=%-7d t=%-8.2fs cum=%-7.1f marg=%-7.1f\n", n, t, n/t, marg
      pn=n; pt=t
    }
  }
' "$out"

echo
echo "raw log: $out"
