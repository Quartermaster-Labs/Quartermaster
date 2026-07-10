#!/usr/bin/env bash
set -euo pipefail
port="18098"
host="127.0.0.1:${port}"
model_key="qwen3.6-35b-a3b-ud-q4_k_xl-100k"
words=24000

cmd='E:/Apps/LLM/llama-cpp/llama-server.exe -m E:/Apps/LLM/Models/unsloth/Qwen3.6-35B-A3B-MTP-GGUF/Qwen3.6-35B-A3B-UD-Q4_K_XL.gguf --port '"$port"' --host 127.0.0.1 -ngl 99 -c 102400 -ub 1024 -b 2048 -fa on -ctk f16 -ctv f16 --parallel 1 --kv-unified --no-warmup --no-webui --metrics --props --spec-type draft-mtp --spec-draft-n-max 2 --jinja --reasoning-format auto --reasoning-budget 16000 --chat-template-kwargs "{\"preserve_thinking\":true}" --ctx-checkpoints 0 -t 7 --n-cpu-moe 41 --slot-save-path "E:/Apps/LLM/quartermaster/build/quartermaster-windows/.cache/slotkv" --chat-template-file templates/qwen-fixed-chat-template.jinja'

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
    curl -sf -m 3 "http://${host}/health" >/dev/null 2>&1 && return 0
    kill -0 "$srv_pid" 2>/dev/null || { echo "server died before healthy" >&2; return 1; }
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
  wait_down || echo "WARN: server still up after kill" >&2
}
trap kill_server EXIT

srvlog="scripts/bench-kv-f16-mtp.log"
echo "== spawning real (unstripped) cmd with MTP spec =="
eval "$cmd" > "$srvlog" 2>&1 &
srv_pid=$!
wait_health || { tail -30 "$srvlog" >&2; exit 1; }

pf=$(mktemp); printf '%s' "$deep_prompt" > "$pf"
payf=$(mktemp)
jq -n --arg m "$model_key" --rawfile p "$pf" '{model:$m, prompt:$p, n_predict:1, cache_prompt:false, temperature:0}' > "$payf"
rm -f "$pf"
resp=$(curl -sS -m 1800 -w '\n%{http_code}' -X POST "http://${host}/completion" -H 'Content-Type: application/json' --data-binary "@${payf}")
rm -f "$payf"
status="${resp##*$'\n'}"; body="${resp%$'\n'*}"
echo "status=$status"
echo "$body" | jq -c '.timings // empty'

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
' "$srvlog"
