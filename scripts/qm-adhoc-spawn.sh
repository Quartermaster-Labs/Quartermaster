#!/usr/bin/env bash
# Spawn a STANDALONE llama-server for one model with ad-hoc flag overrides,
# properly VRAM-sized by quartermaster's own sizer via
# PUT /api/models/{model}/adhoc-cmd -- a pure-compute endpoint (no sidecar
# write, no config regen/reload, nothing persists). Unset flags inherit the
# model's normal effective override; given flags replace just that field.
#
# This does NOT touch a running quartermaster's own router/scheduler -- it
# just asks quartermaster for a correctly-sized command line, then spawns it
# standalone (same pattern as bench-mtp-vs-dflash.sh / bench-ubatch-100k.sh).
# Never run this while quartermaster already has something loaded that shares
# your VRAM headroom -- free it first.
#
# usage:
#   scripts/qm-adhoc-spawn.sh --model KEY --port N [--qm-url URL]
#       [--ctx N] [--ub N] [--threads N] [--kv-k Q] [--kv-v Q] [--spec TYPE]
#       [--vram-target GB] [--cpu-offload N] [--flash-attn on|off]
#       [--parallel N] [--spec-draft-n-max N] [--spec-default]
#       [--spec-ngram-size-n N] [--spec-ngram-size-m N] [--spec-ngram-min-hits N]
#       [--extra-args "..."]
#   scripts/qm-adhoc-spawn.sh --kill --port N
#
# On success (load mode) prints "pid=<pid> port=<port>" and returns with the
# server running -- caller drives its own bench loop, then runs --kill to
# free VRAM (or just let the model's normal ttl reap it).
set -euo pipefail

model_key=""
port=""
qm_url="http://127.0.0.1:8080"
kill_mode=0
declare -A patch=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --model) model_key="$2"; shift 2 ;;
    --port) port="$2"; shift 2 ;;
    --qm-url) qm_url="$2"; shift 2 ;;
    --kill) kill_mode=1; shift ;;
    --ctx) patch[ctx]="$2"; shift 2 ;;
    --ub) patch[ub]="$2"; shift 2 ;;
    --threads) patch[threads]="$2"; shift 2 ;;
    --kv-k) patch[kvK]="\"$2\""; shift 2 ;;
    --kv-v) patch[kvV]="\"$2\""; shift 2 ;;
    --spec) patch[spec]="\"$2\""; shift 2 ;;
    --vram-target) patch[vramTargetGB]="$2"; shift 2 ;;
    --cpu-offload) patch[cpuOffload]="$2"; shift 2 ;;
    --flash-attn) patch[flashAttn]="\"$2\""; shift 2 ;;
    --parallel) patch[parallel]="$2"; shift 2 ;;
    --spec-draft-n-max) patch[specDraftNMax]="$2"; shift 2 ;;
    --spec-default) patch[specDefault]="true"; shift ;;
    --spec-ngram-size-n) patch[specNgramSizeN]="$2"; shift 2 ;;
    --spec-ngram-size-m) patch[specNgramSizeM]="$2"; shift 2 ;;
    --spec-ngram-min-hits) patch[specNgramMinHits]="$2"; shift 2 ;;
    --extra-args) patch[extraArgs]="\"$2\""; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

[[ -n "$port" ]] || { echo "--port required" >&2; exit 1; }
host="127.0.0.1:${port}"

wait_health() {
  local deadline=$((SECONDS + 600))
  while (( SECONDS < deadline )); do
    if curl -sf -m 3 "http://${host}/health" >/dev/null 2>&1; then return 0; fi
    if [[ -n "${srv_pid:-}" ]] && ! kill -0 "$srv_pid" 2>/dev/null; then
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
kill_server() {
  local winpid
  winpid=$(netstat -ano 2>/dev/null | grep -iE "[:.]${port}[[:space:]].*LISTENING" | awk '{print $NF}' | head -1)
  [[ -n "$winpid" ]] && taskkill //F //T //PID "$winpid" >/dev/null 2>&1 || true
  [[ -n "${srv_pid:-}" ]] && kill -9 "$srv_pid" 2>/dev/null || true
  srv_pid=""
  wait_down || echo "WARN: server on ${host} still up after kill" >&2
}

if [[ "$kill_mode" == 1 ]]; then
  kill_server
  exit 0
fi

[[ -n "$model_key" ]] || { echo "--model required" >&2; exit 1; }

if curl -sf -m 2 "http://${host}/health" >/dev/null 2>&1; then
  echo "FATAL: ${host} already serving; aborting to avoid OOM" >&2
  exit 1
fi

# --- build the JSON patch body from given flags ------------------------------
body="{"
first=1
for k in "${!patch[@]}"; do
  [[ $first == 1 ]] || body+=","
  first=0
  body+="\"${k}\":${patch[$k]}"
done
body+="}"

# --- ask quartermaster for the properly-sized command -----------------------
cmd_json=$(curl -sS -m 30 -X PUT "${qm_url}/api/models/${model_key}/adhoc-cmd" \
  -H 'Content-Type: application/json' --data-binary "$body")
cmd=$(echo "$cmd_json" | jq -r '.cmd // empty')
[[ -n "$cmd" ]] || { echo "adhoc-cmd failed: $cmd_json" >&2; exit 1; }
cmd="${cmd//\$\{PORT\}/$port}"

# --- spawn standalone, wait healthy ------------------------------------------
srvlog="$(dirname "$0")/adhoc-srv-${port}.log"
eval "$cmd" >"$srvlog" 2>&1 &
srv_pid=$!
if ! wait_health; then
  echo "[server failed to become healthy (likely VRAM OOM); last srv log:]" >&2
  tail -30 "$srvlog" >&2
  kill_server
  exit 1
fi

echo "pid=${srv_pid} port=${port}"
