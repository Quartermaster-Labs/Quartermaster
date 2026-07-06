#!/usr/bin/env bash
# Bench MTP vs DFlash (vs no-spec baseline) speculative decoding for one model.
#
# Spawns a STANDALONE llama-server per spec variant, sequentially (never two
# alive at once), reusing the exact -ngl / --n-cpu-moe offload from the model's
# entry in the generated config.yaml. That borrowed placement is the DFlash one
# (the heaviest: DFlash adds a separate draft model in VRAM), so every variant
# fits the same envelope and the run cannot OOM beyond what the config already
# proved safe. mtp/none therefore keep one extra layer on CPU than they'd strictly
# need -- a small, safe handicap; the comparison stays apples-to-apples because
# only the spec flags change between variants.
#
# This does NOT touch a running quartermaster. Free your VRAM first (unload
# whatever quartermaster has loaded) so the standalone server has room.
#
# usage:
#   scripts/bench-mtp-vs-dflash.sh [--model KEY] [--config path] [--port N]
#                                  [--reps N] [--variants "none mtp dflash"]
set -euo pipefail

model_key="qwen3.6-35b-a3b-ud-q4_k_xl"
config="$(dirname "$0")/../build/llama-quartermaster-windows/config/config.yaml"
port="18099"
reps=3
variants="none mtp dflash"
out="$(dirname "$0")/bench-mtp-vs-dflash-results.csv"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --model) model_key="$2"; shift 2 ;;
    --config) config="$2"; shift 2 ;;
    --port) port="$2"; shift 2 ;;
    --reps) reps="$2"; shift 2 ;;
    --variants) variants="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

[[ -f "$config" ]] || { echo "config not found: $config" >&2; exit 1; }
host="127.0.0.1:${port}"

# --- extract the model's folded `cmd: >` block from config.yaml -------------
# Grab lines from `"<key>":` until the next same-indent `key:` (ttl:, or next
# model). Keep only the indented cmd continuation lines, join into one string.
base_cmd=$(awk -v key="\"${model_key}\":" '
  index($0, key) { inblock=1; next }
  inblock && /^    cmd: >/ { incmd=1; next }
  inblock && incmd {
    if ($0 ~ /^      /) { sub(/^ +/, ""); printf "%s ", $0; next }
    else { exit }
  }
' "$config")

[[ -n "$base_cmd" ]] || { echo "no cmd found for model key: $model_key" >&2; exit 1; }

# Resolve ${PORT} to our bench port.
base_cmd="${base_cmd//\$\{PORT\}/$port}"

# Pull the DFlash draft path: prefer the -md already in the cmd; else glob the
# main model's dir for a *DFlash*/*dflash* gguf.
main_gguf=$(echo "$base_cmd" | grep -oE '\-m [^ ]+' | head -1 | cut -d' ' -f2)
draft_md=$(echo "$base_cmd" | grep -oE '\-md [^ ]+' | head -1 | cut -d' ' -f2 || true)
if [[ -z "${draft_md:-}" && -n "$main_gguf" ]]; then
  draft_md=$(ls "$(dirname "$main_gguf")"/*[Dd][Ff]lash*.gguf 2>/dev/null | head -1 || true)
fi

# Strip ALL spec flags from the base so each variant starts clean.
strip_spec() {
  echo "$1" | sed -E \
    -e 's/--spec-type +[^ ]+//g' \
    -e 's/--spec-draft-n-max +[^ ]+//g' \
    -e 's/-md +[^ ]+//g' \
    -e 's/-ngld +[^ ]+//g' \
    -e 's/--spec-default//g' \
    -e 's/--spec-ngram-map-k4v-[a-z-]+ +[^ ]+//g' \
    -e 's/  +/ /g'
}
stripped_cmd="$(strip_spec "$base_cmd")"

variant_cmd() {
  case "$1" in
    none)   echo "$stripped_cmd --spec-type none" ;;
    mtp)    echo "$stripped_cmd --spec-type draft-mtp --spec-draft-n-max 2" ;;
    dflash)
      [[ -n "${draft_md:-}" ]] || { echo "ERR: no DFlash draft gguf found for dflash variant" >&2; return 1; }
      # n-max default 6 per the z-lab/Alittlehammmer model card. 15 over-drafts
      # (block diffusion proposes a full block; excess is rejected, wasting GPU).
      echo "$stripped_cmd --spec-type draft-dflash --spec-draft-n-max ${DFLASH_NMAX:-6} -md ${draft_md} -ngld 99" ;;
    ngram)  # model-free n-gram lookup from context; no draft gguf. good for repetitive/code tokens.
      echo "$stripped_cmd --spec-type ngram-map-k4v --spec-default --spec-ngram-map-k4v-size-n 16 --spec-ngram-map-k4v-size-m 24 --spec-ngram-map-k4v-min-hits 1" ;;
    *) echo "unknown variant: $1" >&2; return 1 ;;
  esac
}

# --- bench plumbing (mirrors bench-spec.sh) ---------------------------------
if [[ ! -f "$out" ]]; then
  echo "timestamp,variant,profile,rep,prompt_n,prompt_ms,prompt_per_second,predicted_n,predicted_ms,predicted_per_second,accept_rate,error" > "$out"
fi

filler="The quick brown fox jumps over the lazy dog near the riverbank while clouds drift slowly across an autumn sky, and the town below hums with the quiet rhythm of everyday life. "
build_prompt() {
  local target_words=$1 words_per_rep=32 s="" i
  local reps_needed=$(( (target_words + words_per_rep - 1) / words_per_rep ))
  for ((i=0; i<reps_needed; i++)); do s+="$filler"; done
  printf '%s' "$s"
}
prompt_pp="$(build_prompt 8000)"
prompt_tg="Write a detailed story about a robot learning to paint, at least 400 words."
# Reasoning prompt: hard competition math -> long greedy chain-of-thought, the
# workload DFlash's block-diffusion draft is trained for (z-lab benchmark = math500).
prompt_reason="Find all positive integers n such that n^2 + 2n + 2024 is a perfect square. Then, separately, compute the remainder when 7^1000 is divided by 1000. Show every step of your reasoning in full detail, checking your work as you go."

run_one() {
  local variant=$1 profile=$2 prompt=$3 n_predict=$4 rep=$5
  local pf payf resp status body ts ep
  pf=$(mktemp); printf '%s' "$prompt" > "$pf"
  payf=$(mktemp)
  # reason profile -> chat endpoint (applies the reasoning chat template so
  # thinking engages, matching z-lab's DFlash benchmark: greedy + thinking).
  # Everything else -> raw /completion.
  if [[ "$profile" == reason* ]]; then
    ep="/v1/chat/completions"
    jq -n --arg m "$model_key" --rawfile p "$pf" --argjson n "$n_predict" \
      '{model:$m, messages:[{role:"user",content:$p}], max_tokens:$n, temperature:0, stream:false}' > "$payf"
  else
    ep="/completion"
    jq -n --arg m "$model_key" --rawfile p "$pf" --argjson n "$n_predict" \
      '{model:$m, prompt:$p, n_predict:$n, cache_prompt:false, temperature:0}' > "$payf"
  fi
  rm -f "$pf"
  ts=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  if ! resp=$(curl -sS -m 1800 -w '\n%{http_code}' -X POST "http://${host}${ep}" \
        -H 'Content-Type: application/json' --data-binary "@${payf}"); then
    rm -f "$payf"; echo "${ts},${variant},${profile},${rep},,,,,,,,\"curl failed\"" >> "$out"
    echo "[$variant $profile rep$rep] curl failed" >&2; return
  fi
  rm -f "$payf"
  status="${resp##*$'\n'}"; body="${resp%$'\n'*}"
  if [[ "$status" != "200" ]]; then
    local em; em=$(echo "$body" | tr '"' "'" | tr -d '\n')
    echo "${ts},${variant},${profile},${rep},,,,,,,,\"HTTP ${status}: ${em}\"" >> "$out"
    echo "[$variant $profile rep$rep] HTTP $status: $em" >&2; return
  fi
  local t; t=$(echo "$body" | jq -c '.timings // empty')
  [[ -n "$t" ]] || { echo "${ts},${variant},${profile},${rep},,,,,,,,\"no timings\"" >> "$out"; return; }
  local pn pms pps dn dms tgs acc
  pn=$(echo "$t" | jq -r '.prompt_n // ""'); pms=$(echo "$t" | jq -r '.prompt_ms // ""')
  pps=$(echo "$t" | jq -r '.prompt_per_second // ""'); dn=$(echo "$t" | jq -r '.predicted_n // ""')
  dms=$(echo "$t" | jq -r '.predicted_ms // ""'); tgs=$(echo "$t" | jq -r '.predicted_per_second // ""')
  # /completion JSON carries no draft stats; acceptance is scraped from the
  # server log per-variant after the run (see main loop). Leave column blank.
  echo "${ts},${variant},${profile},${rep},${pn},${pms},${pps},${dn},${dms},${tgs},," >> "$out"
  echo "[$variant $profile rep$rep] pp=${pps} tok/s (n=${pn}) tg=${tgs} tok/s (n=${dn})"
}

wait_health() {
  local deadline=$((SECONDS + 600))
  while (( SECONDS < deadline )); do
    if curl -sf -m 3 "http://${host}/health" >/dev/null 2>&1; then return 0; fi
    # Bail fast if the server process already exited (crash on startup).
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
  # Kill by LISTENING port, not by $srv_pid: under git-bash, `eval ... &` gives a
  # bash/MSYS pid, and taskkill //PID on that wrong-namespace pid silently misses
  # the real llama-server.exe -> it leaks, the next variant then benches the stale
  # server (bad data) or a 2nd 35B spawns and OOMs. netstat gives the true Windows
  # pid owning our port; //T reaps the eval subshell wrapper too.
  local winpid
  winpid=$(netstat -ano 2>/dev/null | grep -iE "[:.]${port}[[:space:]].*LISTENING" | awk '{print $NF}' | head -1)
  [[ -n "$winpid" ]] && taskkill //F //T //PID "$winpid" >/dev/null 2>&1 || true
  [[ -n "$srv_pid" ]] && kill -9 "$srv_pid" 2>/dev/null || true
  srv_pid=""
  # HARD gate: do not spawn the next variant until the port is actually free
  # (server down => VRAM/RAM released). Prevents two 35B servers coexisting.
  wait_down || echo "WARN: server on ${host} still up after kill; ABORTING to avoid OOM" >&2
}
trap kill_server EXIT

echo "== bench $model_key : variants=[$variants] port=$port reps=$reps =="
echo "== borrowed offload from config (heaviest/DFlash placement) -> no OOM =="
[[ -n "${draft_md:-}" ]] && echo "== dflash draft: $draft_md ==" || echo "== WARN: no DFlash gguf found; dflash variant will be skipped =="

for v in $variants; do
  cmd="$(variant_cmd "$v")" || { echo "skip variant $v"; continue; }
  echo
  echo "=== variant: $v ==="
  # OOM guard: refuse to spawn if anything is already on the port (prev server
  # not fully dead) -> two 35B servers would blow past RAM and crash the box.
  if curl -sf -m 2 "http://${host}/health" >/dev/null 2>&1; then
    echo "FATAL: ${host} still serving before spawning '$v'; aborting to avoid OOM" >&2
    exit 1
  fi
  echo "  $cmd"
  srvlog="$(dirname "$out")/bench-srv-${v}.log"
  # eval (not bare $cmd): the config cmd contains quoted args like
  # --chat-template-kwargs "{\"preserve_thinking\":true}" that word-splitting
  # would mangle into literal-quote garbage and crash the server on parse.
  eval "$cmd" >"$srvlog" 2>&1 &
  srv_pid=$!
  if ! wait_health; then
    echo "[$v] server failed to become healthy; last srv log:" >&2
    tail -20 "$srvlog" >&2
    echo "$(date -u +%FT%TZ),${v},startup,0,,,,,,,,\"health timeout\"" >> "$out"
    kill_server; continue
  fi
  run_one "$v" warmup "$prompt_tg" 16 0
  if [[ "${PP_ONLY:-0}" == 1 ]]; then
    for ((r=1; r<=3; r++)); do run_one "$v" pp_8k "$prompt_pp" 8 "$r"; done
  elif [[ "${REASON_ONLY:-0}" == 1 ]]; then
    for ((r=1; r<=2; r++)); do run_one "$v" reason "$prompt_reason" 1024 "$r"; done
  else
    for ((r=1; r<=2; r++)); do run_one "$v" pp_8k "$prompt_pp" 8 "$r"; done
    for ((r=1; r<=reps; r++)); do run_one "$v" tg "$prompt_tg" 256 "$r"; done
    for ((r=1; r<=2; r++)); do run_one "$v" reason "$prompt_reason" 1024 "$r"; done
  fi
  # Draft acceptance lives in the server log (the /completion JSON has no draft
  # stats), not in the HTTP timings. Scrape the last reported rate for this variant.
  acc=$(grep -oE 'draft acceptance = [0-9.]+ \( *[0-9]+ accepted / *[0-9]+ generated\), mean len = *[0-9.]+' "$srvlog" 2>/dev/null | tail -1) || true
  [[ -n "$acc" ]] && echo "[$v] ${acc}"
  kill_server
done

echo
echo "== summary (avg per variant/profile) =="
awk -F',' '
  NR==1{next}
  { v=$2; p=$3
    if ($7!=""){pp_s[v","p]+=$7; pp_n[v","p]++}
    if ($10!=""){tg_s[v","p]+=$10; tg_n[v","p]++}
    if ($11!=""){ac_s[v","p]+=$11; ac_n[v","p]++} }
  END{
    for (k in tg_n){split(k,a,","); printf "%-8s %-8s tg=%.1f tok/s", a[1],a[2], tg_s[k]/tg_n[k]; if(ac_n[k])printf " accept=%.2f", ac_s[k]/ac_n[k]; print ""}
    for (k in pp_n){split(k,a,","); printf "%-8s %-8s pp=%.1f tok/s\n", a[1],a[2], pp_s[k]/pp_n[k]}
  }' "$out" | sort

echo
echo "results: $out"
