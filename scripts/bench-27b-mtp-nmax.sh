#!/usr/bin/env bash
# mtp draft-n-max sweep on one model. Spawns standalone per n-max via
# qm-adhoc-spawn.sh, sequential. n-max affects DECODE (tg) only, so profiles
# are creative-tg + reason-tg (+ acceptance scraped from srv log). ub/pp left
# at autogen default.
#
# usage: scripts/bench-27b-mtp-nmax.sh [--model K] [--qm-url U] [--port N] [--nmaxes "2 3 4"]
set -euo pipefail

model_key="qwen3.6-27b-ud-q4_k_xl-100k"
qm_url="http://127.0.0.1:1250"
port="18099"
nmaxes="2 3 4"
out="$(dirname "$0")/bench-27b-mtp-nmax.csv"
adhoc="$(dirname "$0")/qm-adhoc-spawn.sh"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --model) model_key="$2"; shift 2 ;;
    --qm-url) qm_url="$2"; shift 2 ;;
    --port) port="$2"; shift 2 ;;
    --nmaxes) nmaxes="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

host="127.0.0.1:${port}"
trap '"$adhoc" --kill --port "$port" >/dev/null 2>&1 || true' EXIT

echo "nmax,profile,rep,predicted_n,predicted_per_second,error" > "$out"

prompt_tg="Write a detailed story about a robot learning to paint, at least 400 words."
prompt_reason="Find all positive integers n such that n^2 + 2n + 2024 is a perfect square. Then, separately, compute the remainder when 7^1000 is divided by 1000. Show every step of your reasoning in full detail, checking your work as you go."

run_chat() { # nmax profile prompt n_predict rep
  local nm=$1 profile=$2 prompt=$3 n=$4 rep=$5 pf payf resp status body
  pf=$(mktemp); printf '%s' "$prompt" > "$pf"; payf=$(mktemp)
  jq -n --rawfile p "$pf" --argjson n "$n" '{messages:[{role:"user",content:$p}], max_tokens:$n, temperature:0, stream:false}' > "$payf"
  rm -f "$pf"
  if ! resp=$(curl -sS -m 1800 -w '\n%{http_code}' -X POST "http://${host}/v1/chat/completions" \
        -H 'Content-Type: application/json' --data-binary "@${payf}"); then
    rm -f "$payf"; echo "${nm},${profile},${rep},,,\"curl failed\"" >> "$out"; return; fi
  rm -f "$payf"
  status="${resp##*$'\n'}"; body="${resp%$'\n'*}"
  if [[ "$status" != "200" ]]; then echo "${nm},${profile},${rep},,,\"HTTP ${status}\"" >> "$out"; echo "[n$nm $profile r$rep] HTTP $status" >&2; return; fi
  local t dn tgs; t=$(echo "$body"|jq -c '.timings // empty')
  [[ -n "$t" ]] || { echo "${nm},${profile},${rep},,,\"no timings\"" >> "$out"; return; }
  dn=$(echo "$t"|jq -r '.predicted_n//""'); tgs=$(echo "$t"|jq -r '.predicted_per_second//""')
  echo "${nm},${profile},${rep},${dn},${tgs}," >> "$out"
  echo "[n$nm $profile r$rep] tg=${tgs} tok/s (n=${dn})"
}

for nm in $nmaxes; do
  echo; echo "=== mtp n-max=$nm ==="
  spawn_out=$("$adhoc" --model "$model_key" --port "$port" --qm-url "$qm_url" --spec draft-mtp --spec-draft-n-max "$nm" 2>&1) || {
    echo "[n$nm] spawn failed:" >&2; echo "$spawn_out" >&2
    echo "${nm},startup,0,,,\"spawn failed\"" >> "$out"; continue; }
  echo "  $spawn_out"
  srvlog="$(dirname "$adhoc")/adhoc-srv-${port}.log"
  run_chat "$nm" warmup "$prompt_tg" 16 0
  for r in 1 2; do run_chat "$nm" creative "$prompt_tg" 256 "$r"; done
  for r in 1 2; do run_chat "$nm" reason "$prompt_reason" 1024 "$r"; done
  acc=$(grep -oE 'draft acceptance = [0-9.]+ \( *[0-9]+ accepted / *[0-9]+ generated\), mean len = *[0-9.]+' "$srvlog" 2>/dev/null | tail -1) || true
  [[ -n "$acc" ]] && echo "[n$nm] ${acc}"
  "$adhoc" --kill --port "$port"
done

echo; echo "== summary (avg tg per nmax/profile) =="
awk -F',' 'NR==1{next}{if($5!=""){s[$1","$2]+=$5;c[$1","$2]++}}
  END{for(k in c){split(k,a,",");printf "n=%-3s %-8s tg=%.1f tok/s\n",a[1],a[2],s[k]/c[k]}}' "$out" | sort
echo; echo "results: $out"
