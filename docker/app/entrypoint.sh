#!/bin/sh
# Seed the autogen control file on first run, then exec the server.
#
# quartermaster does not create this file for itself: on a desktop install the
# setup wizard writes it, and autogen.EnsureConfig treats a missing one as a
# hard error rather than inventing a models folder behind the user's back. In a
# container there is no wizard, and /data is whatever the user mounted, so
# without this a fresh `docker run` dies on its first line.
#
# Only the absence of the file triggers a write. An existing file is the
# user's, comments, overrides and all, and is never rewritten -- the sidecar
# beside it is also where the dashboard stores per-model settings.
#
# The body matches internal/setup's minimalGenerate: autogen fills every unset
# knob, so modelsRoot is genuinely all that is required.

set -e

GEN="${QM_GENERATE_FILE:-/data/config/quartermaster-generate.yaml}"

if [ ! -e "$GEN" ]; then
  mkdir -p "$(dirname "$GEN")"
  cat > "$GEN" <<YAML
# Quartermaster autogen control file, created on first container start.
# Every unset knob falls back to its built-in default; see
# quartermaster-generate.example.yaml in the repository for the annotated form.
settings:
  modelsRoot: "${QM_MODELS_DIR:-/data/models}"
YAML
  echo "quartermaster: seeded $GEN" >&2
fi

exec quartermaster "$@"
