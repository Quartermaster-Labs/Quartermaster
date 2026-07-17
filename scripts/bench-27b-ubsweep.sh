#!/usr/bin/env bash
# ub-sweep for pp (prefill) on one model. Spawns standalone per ub via
# qm-adhoc-spawn.sh (VRAM-sized by quartermaster), sequential. spec=none so
# pp is isolated from draft overhead; also samples tg to confirm decode stays
# flat across ub (it should -- ub is prefill-only).
#
# usage: scripts/bench-27b-ubsweep.sh [--model K] [--qm-url U] [--port N] [--ubs "512 1024 2048"]
set -euo pipefail

model_key="qwen3.6-27b-ud-q4_k_xl-100k"
qm_url="http://127.0.0.1:1250"
port="18099"
ubs="512 1024 2048"
out="$(dirname "$0")/bench-27b-ubsweep.csv"
adhoc="$(dirname "$0")/qm-adhoc-spawn.sh"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --model) model_key="$2"; shift 2 ;;
    --qm-url) qm_url="$2"; shift 2 ;;
    --port) port="$2"; shift 2 ;;
    --ubs) ubs="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

host="127.0.0.1:${port}"
trap '"$adhoc" --kill --port "$port" >/dev/null 2>&1 || true' EXIT

echo "ub,profile,rep,prompt_n,prompt_per_second,predicted_n,predicted_per_second,error" > "$out"

filler="The quick brown fox jumps over the lazy dog near the riverbank while clouds drift slowly across an autumn sky, and the town below hums with the quiet rhythm of everyday life. "
build_prompt() { local tw=$1 wpr=32 s="" i n; n=$(( (tw+wpr-1)/wpr )); for ((i=0;i<n;i++)); do s+="$filler"; done; printf '%s' "$s"; }
prompt_pp="$(build_prompt 8000)"
prompt_tg="Write a detailed story about a robot learning to paint, at least 400 words."

run_one() { # ub profile prompt n_predict rep
  local ub=$1 profile=$2 prompt=$3 n=$4 rep=$5 pf payf resp status body ep
  pf=$(mktemp); printf '%s' "$prompt" > "$pf"; payf=$(mktemp)
  ep="/completion"
  jq -n --rawfile p "$pf" --argjson n "$n" '{prompt:$p, n_predict:$n, cache_prompt:false, temperature:0}' > "$payf"
  rm -f "$pf"
  if ! resp=$(curl -sS -m 1800 -w '\n%{http_code}' -X POST "http://${host}${ep}" \
        -H 'Content-Type: application/json' --data-binary "@${payf}"); then
    rm -f "$payf"; echo "${ub},${profile},${rep},,,,,\"curl failed\"" >> "$out"; return; fi
  rm -f "$payf"
  status="${resp##*$'\n'}"; body="${resp%$'\n'*}"
  if [[ "$status" != "200" ]]; then
    echo "${ub},${profile},${rep},,,,,\"HTTP ${status}\"" >> "$out"; echo "[ub$ub $profile rep$rep] HTTP $status" >&2; return; fi
  local t pn pps dn tgs; t=$(echo "$body" | jq -c '.timings // empty')
  [[ -n "$t" ]] || { echo "${ub},${profile},${rep},,,,,\"no timings\"" >> "$out"; return; }
  pn=$(echo "$t"|jq -r '.prompt_n//""'); pps=$(echo "$t"|jq -r '.prompt_per_second//""')
  dn=$(echo "$t"|jq -r '.predicted_n//""'); tgs=$(echo "$t"|jq -r '.predicted_per_second//""')
  echo "${ub},${profile},${rep},${pn},${pps},${dn},${tgs}," >> "$out"
  echo "[ub$ub $profile rep$rep] pp=${pps} tok/s (n=${pn})  tg=${tgs} tok/s (n=${dn})"
}

for ub in $ubs; do
  echo; echo "=== ub=$ub ==="
  spawn_out=$("$adhoc" --model "$model_key" --port "$port" --qm-url "$qm_url" --ub "$ub" --spec none 2>&1) || {
    echo "[ub$ub] spawn failed:" >&2; echo "$spawn_out" >&2
    echo "${ub},startup,0,,,,,\"spawn failed\"" >> "$out"; continue; }
  echo "  $spawn_out"
  run_one "$ub" warmup "$prompt_tg" 16 0
  for r in 1 2 3; do run_one "$ub" pp_8k "$prompt_pp" 8 "$r"; done
  for r in 1 2; do run_one "$ub" tg "$prompt_tg" 256 "$r"; done
  "$adhoc" --kill --port "$port"
done

echo; echo "== summary =="
awk -F',' 'NR==1{next}{if($5!=""){pp[$1]+=$5;pn[$1]++}if($7!=""){tg[$1]+=$7;tn[$1]++}}
  END{for(u in pn)printf "ub=%-5s pp=%.1f tok/s  tg=%.1f tok/s\n",u,pp[u]/pn[u],(tn[u]?tg[u]/tn[u]:0)}' "$out" | sort
echo; echo "results: $out"
