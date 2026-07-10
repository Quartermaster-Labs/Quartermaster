#!/usr/bin/env bash
# Bench KV cache quant (f16 vs q8_0) + the placement it unlocks, on the 100k-ctx
# Qwen3.6-35B-A3B variant. q8_0 KV frees enough VRAM to drop --n-cpu-moe 41->39
# (2 more expert layers on GPU) per the sizer's own EstimatePlan math.
#
# Spawns ONE standalone llama-server at a time (never two alive together),
# borrowing the real 100k cmd from config.yaml and swapping only -ctk/-ctv and
# --n-cpu-moe between variants; spec-decode flags are stripped so MTP doesn't
# muddy the pp-only comparison. Sends ONE long prompt as the FIRST request
# (no warmup) so the cold mmap-fault-in tax shows up exactly like real usage,
# then parses the server log's print_timing lines into cumulative + marginal
# (per-chunk) t/s so we can see the cold->warm curve, not just the misleading
# cumulative average llama-server prints.
#
# Does NOT touch a running quartermaster. Confirm nothing else has the GPU
# loaded first (tasklist for llama-server.exe) -- this spawns a real 35B.
#
# usage: scripts/bench-kv-quant.sh [--config path] [--port N] [--words N] [--variants "f16 q8_0"]
set -euo pipefail

model_key="qwen3.6-35b-a3b-ud-q4_k_xl-100k"
config="$(dirname "$0")/../build/quartermaster-windows/config/config.yaml"
port="18098"
words=24000   # ~30-32k tokens on this filler text, matching the real trace depth
variants="f16 q8_0"
out="$(dirname "$0")/bench-kv-quant-results.csv"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --config) config="$2"; shift 2 ;;
    --port) port="$2"; shift 2 ;;
    --words) words="$2"; shift 2 ;;
    --variants) variants="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

[[ -f "$config" ]] || { echo "config not found: $config" >&2; exit 1; }
host="127.0.0.1:${port}"

base_cmd=$(awk -v key="\"${model_key}\":" '
  index($0, key) { inblock=1; next }
  inblock && /^    cmd: >/ { incmd=1; next }
  inblock && incmd {
    if ($0 ~ /^      /) { sub(/^ +/, ""); printf "%s ", $0; next }
    else { exit }
  }
' "$config")
[[ -n "$base_cmd" ]] || { echo "no cmd found for model key: $model_key" >&2; exit 1; }
base_cmd="${base_cmd//\$\{PORT\}/$port}"

# Strip spec-decode flags (isolate KV-quant/placement effect on pp only).
base_cmd=$(echo "$base_cmd" | sed -E \
  -e 's/--spec-type +[^ ]+//g' \
  -e 's/--spec-draft-n-max +[^ ]+//g' \
  -e 's/  +/ /g')

variant_cmd() {
  case "$1" in
    f16)  echo "$base_cmd" | sed -E 's/-ctk f16 -ctv f16/-ctk f16 -ctv f16/; s/--n-cpu-moe 41/--n-cpu-moe 41/' ;;
    q8_0) echo "$base_cmd" | sed -E 's/-ctk f16 -ctv f16/-ctk q8_0 -ctv q8_0/; s/--n-cpu-moe 41/--n-cpu-moe 39/' ;;
    *) echo "unknown variant: $1" >&2; return 1 ;;
  esac
}

if [[ ! -f "$out" ]]; then
  echo "timestamp,variant,prompt_n,prompt_ms,prompt_per_second,error" > "$out"
fi

filler="The quick brown fox jumps over the lazy dog near the riverbank while clouds drift slowly across an autumn sky, and the town below hums with the quiet rhythm of everyday life. "
build_prompt() {
  local target_words=$1 words_per_rep=32 s="" i
  local reps_needed=$(( (target_words + words_per_rep - 1) / words_per_rep ))
  for ((i=0; i<reps_needed; i++)); do s+="$filler"; done
  printf '%s' "$s"
}
deep_prompt="$(build_prompt "$words")"

wait_health() {
  local deadline=$((SECONDS + 600))
  while (( SECONDS < deadline )); do
    if curl -sf -m 3 "http://${host}/health" >/dev/null 2>&1; then return 0; fi
    if [[ -n "$srv_pid" ]] && ! kill -0 "$srv_pid" 2>/dev/null; then
      echo "[server pid $srv_pid exited before healthy]" >&2; return 1
    fi
    sleep 2
  done
  return 1
}
wait_down() {
  local deadline=$((SECONDS + 120))
  while (( SECONDS < deadline )); do
    curl -sf -m 2 "http://${host}/health" >/dev/null 2>&1 || return 0
    sleep 1
  done
  return 1
}

srv_pid=""
kill_server() {
  local winpid
  winpid=$(netstat -ano 2>/dev/null | grep -iE "[:.]${port}[[:space:]].*LISTENING" | awk '{print $NF}' | head -1)
  [[ -n "$winpid" ]] && taskkill //F //T //PID "$winpid" >/dev/null 2>&1 || true
  [[ -n "$srv_pid" ]] && kill -9 "$srv_pid" 2>/dev/null || true
  srv_pid=""
  wait_down || echo "WARN: server on ${host} still up after kill; ABORTING to avoid OOM" >&2
}
trap kill_server EXIT

echo "== bench kv-quant $model_key : variants=[$variants] port=$port words=$words =="

for v in $variants; do
  cmd="$(variant_cmd "$v")" || { echo "skip variant $v"; continue; }
  echo
  echo "=== variant: $v ==="
  if curl -sf -m 2 "http://${host}/health" >/dev/null 2>&1; then
    echo "FATAL: ${host} still serving before spawning '$v'; aborting to avoid OOM" >&2
    exit 1
  fi
  echo "  $cmd"
  srvlog="$(dirname "$out")/bench-kv-${v}.log"
  eval "$cmd" >"$srvlog" 2>&1 &
  srv_pid=$!
  if ! wait_health; then
    echo "[$v] server failed to become healthy; last srv log:" >&2
    tail -20 "$srvlog" >&2
    echo "$(date -u +%FT%TZ),${v},,,,\"health timeout\"" >> "$out"
    kill_server; continue
  fi

  pf=$(mktemp); printf '%s' "$deep_prompt" > "$pf"
  payf=$(mktemp)
  jq -n --arg m "$model_key" --rawfile p "$pf" \
    '{model:$m, prompt:$p, n_predict:1, cache_prompt:false, temperature:0}' > "$payf"
  rm -f "$pf"
  ts=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  if resp=$(curl -sS -m 1800 -w '\n%{http_code}' -X POST "http://${host}/completion" \
        -H 'Content-Type: application/json' --data-binary "@${payf}"); then
    status="${resp##*$'\n'}"; body="${resp%$'\n'*}"
    if [[ "$status" == "200" ]]; then
      t=$(echo "$body" | jq -c '.timings // empty')
      pn=$(echo "$t" | jq -r '.prompt_n // ""'); pms=$(echo "$t" | jq -r '.prompt_ms // ""')
      pps=$(echo "$t" | jq -r '.prompt_per_second // ""')
      echo "${ts},${v},${pn},${pms},${pps}," >> "$out"
      echo "[$v] final cumulative: n=${pn} tok in ${pms} ms = ${pps} tok/s"
    else
      em=$(echo "$body" | tr '"' "'" | tr -d '\n')
      echo "${ts},${v},,,,\"HTTP ${status}: ${em}\"" >> "$out"
      echo "[$v] HTTP $status: $em" >&2
    fi
  else
    echo "${ts},${v},,,,\"curl failed\"" >> "$out"
    echo "[$v] curl failed" >&2
  fi
  rm -f "$payf"

  echo "[$v] print_timing curve (cumulative n_tokens / t -> marginal t/s vs prev line):"
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
  ' "$srvlog"

  kill_server
done

echo
echo "results: $out"
