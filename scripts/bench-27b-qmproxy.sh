#!/usr/bin/env bash
# Bench arg combos THROUGH the live quartermaster proxy (not a standalone
# server). Uses POST /api/models/{model}/adhoc-load to inject a combo's launch
# args into the running router (in-memory, nothing persisted), then drives real
# /v1/chat/completions + /completion requests at :1250 -- so promptCanon,
# slotcache and the reverse-proxy path are all in the measurement. DELETE
# /api/models/{model}/adhoc-load reverts at the end.
#
# Each combo is a name + a JSON variant patch (adhoc-cmd/variantDTO shape:
# spec, specDraftNMax, ub, extraArgs, ...). pp uses a fresh 8k prompt with
# cache_prompt=false; tg/reason use the chat endpoint.
#
# usage: scripts/bench-27b-qmproxy.sh [--model K] [--qm-url U] [--api-key KEY]
set -euo pipefail

model_key="qwen3.6-27b-ud-q4_k_xl-100k"
qm_url="http://127.0.0.1:1250"
apikey="qm-7f9ead612acb74ed4e6d23f0c758d129dbad9c434b4f17bb"  # pi-harness
out="$(dirname "$0")/bench-27b-qmproxy.csv"

# combos: "name|json-patch"
combos=(
  "none|{\"spec\":\"none\",\"ub\":1024}"
  "mtp-n2|{\"spec\":\"draft-mtp\",\"specDraftNMax\":2,\"ub\":1024}"
  "ngram-mod|{\"spec\":\"ngram-mod\",\"ub\":1024}"
  "ngram-map-k4v|{\"spec\":\"ngram-map-k4v\",\"ub\":1024}"
  "mtp-n2+ngram-mod|{\"spec\":\"draft-mtp+ngram-mod\",\"specDraftNMax\":2,\"ub\":1024}"
  "mtp-n2+ngram-map-k4v|{\"spec\":\"draft-mtp+ngram-map-k4v\",\"specDraftNMax\":2,\"ub\":1024}"
)

while [[ $# -gt 0 ]]; do
  case "$1" in
    --model) model_key="$2"; shift 2 ;;
    --qm-url) qm_url="$2"; shift 2 ;;
    --api-key) apikey="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

host="${qm_url#http://}"
AUTH=(-H "Authorization: Bearer ${apikey}")
trap 'curl -sS -m 60 -X DELETE "${qm_url}/api/models/${model_key}/adhoc-load" >/dev/null 2>&1 || true' EXIT

echo "combo,profile,rep,prompt_n,prompt_per_second,predicted_n,predicted_per_second,cached_n,error" > "$out"

filler="The quick brown fox jumps over the lazy dog near the riverbank while clouds drift slowly across an autumn sky, and the town below hums with the quiet rhythm of everyday life. "
build_prompt() { local tw=$1 wpr=32 s="" i n; n=$(( (tw+wpr-1)/wpr )); for ((i=0;i<n;i++)); do s+="$filler"; done; printf '%s' "$s"; }
prompt_pp="$(build_prompt 8000)"
prompt_tg="Write a detailed story about a robot learning to paint, at least 400 words."
prompt_reason="Find all positive integers n such that n^2 + 2n + 2024 is a perfect square. Then, separately, compute the remainder when 7^1000 is divided by 1000. Show every step of your reasoning in full detail, checking your work as you go."

run_one() { # combo profile prompt n_predict rep
  local combo=$1 profile=$2 prompt=$3 n=$4 rep=$5 pf payf resp status body ep t
  pf=$(mktemp); printf '%s' "$prompt" > "$pf"; payf=$(mktemp)
  if [[ "$profile" == pp_* ]]; then
    ep="/completion"
    jq -n --arg m "$model_key" --rawfile p "$pf" --argjson n "$n" '{model:$m, prompt:$p, n_predict:$n, cache_prompt:false, temperature:0}' > "$payf"
  else
    ep="/v1/chat/completions"
    jq -n --arg m "$model_key" --rawfile p "$pf" --argjson n "$n" '{model:$m, messages:[{role:"user",content:$p}], max_tokens:$n, temperature:0, stream:false}' > "$payf"
  fi
  rm -f "$pf"
  if ! resp=$(curl -sS -m 1800 -w '\n%{http_code}' -X POST "http://${host}${ep}" \
        "${AUTH[@]}" -H 'Content-Type: application/json' --data-binary "@${payf}"); then
    rm -f "$payf"; echo "${combo},${profile},${rep},,,,,,\"curl failed\"" >> "$out"; return; fi
  rm -f "$payf"
  status="${resp##*$'\n'}"; body="${resp%$'\n'*}"
  if [[ "$status" != "200" ]]; then
    local em; em=$(echo "$body"|tr '"' "'"|tr -d '\n'|cut -c1-120)
    echo "${combo},${profile},${rep},,,,,,\"HTTP ${status}: ${em}\"" >> "$out"; echo "[$combo $profile r$rep] HTTP $status: $em" >&2; return; fi
  t=$(echo "$body"|jq -c '.timings // empty')
  [[ -n "$t" ]] || { echo "${combo},${profile},${rep},,,,,,\"no timings\"" >> "$out"; return; }
  local pn pps dn tgs cn
  pn=$(echo "$t"|jq -r '.prompt_n//""'); pps=$(echo "$t"|jq -r '.prompt_per_second//""')
  dn=$(echo "$t"|jq -r '.predicted_n//""'); tgs=$(echo "$t"|jq -r '.predicted_per_second//""')
  cn=$(echo "$t"|jq -r '.cache_n // .prompt_cache_n // ""')
  echo "${combo},${profile},${rep},${pn},${pps},${dn},${tgs},${cn}," >> "$out"
  echo "[$combo $profile r$rep] pp=${pps} (n=${pn}) tg=${tgs} (n=${dn}) cached=${cn}"
}

for entry in "${combos[@]}"; do
  name="${entry%%|*}"; patch="${entry#*|}"
  echo; echo "=== combo: $name  patch=$patch ==="
  ld=$(curl -sS -m 120 -X PUT "${qm_url}/api/models/${model_key}/adhoc-load" \
        -H 'Content-Type: application/json' --data-binary "$patch")
  if ! echo "$ld" | jq -e '.status=="loaded"' >/dev/null 2>&1; then
    echo "[!] adhoc-load failed: $ld" >&2
    echo "${name},load,0,,,,,,\"load failed: $(echo "$ld"|tr '"' "'"|tr -d '\n'|cut -c1-120)\"" >> "$out"; continue; fi
  echo "  loaded: $(echo "$ld"|jq -r '.cmd' | grep -oE '\-\-spec-type [a-z-]+|--spec-draft-n-max [0-9]+|-ub [0-9]+' | tr '\n' ' ')"
  # warmup also triggers the fresh spawn with the new args
  run_one "$name" warmup "$prompt_tg" 16 0
  for r in 1 2; do run_one "$name" pp_8k "$prompt_pp" 8 "$r"; done
  for r in 1 2; do run_one "$name" creative "$prompt_tg" 256 "$r"; done
  for r in 1 2; do run_one "$name" reason "$prompt_reason" 1024 "$r"; done
done

echo; echo "== summary (avg through qm proxy) =="
awk -F',' 'NR==1{next}
  {c=$1;p=$2; if($5!=""){pp[c","p]+=$5;ppn[c","p]++} if($7!=""){tg[c","p]+=$7;tgn[c","p]++}}
  END{for(k in tgn){split(k,a,",");printf "%-16s %-9s tg=%.1f tok/s\n",a[1],a[2],tg[k]/tgn[k]}
      for(k in ppn){split(k,a,",");printf "%-16s %-9s pp=%.1f tok/s\n",a[1],a[2],pp[k]/ppn[k]}}' "$out" | sort
echo; echo "results: $out (adhoc-load reverted on exit)"
