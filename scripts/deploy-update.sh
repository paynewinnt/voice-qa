#!/bin/bash

set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="$PROJECT_DIR/dist"
DEPLOY_HOST="${VOICE_QA_UPDATE_HOST:-root@124.223.218.142}"
REMOTE_DIR="${VOICE_QA_UPDATE_DIR:-/srv/voice-qa}"
SSH_CONTROL_PATH="${VOICE_QA_SSH_CONTROL_PATH:-}"
MANIFEST_PATH="$DIST_DIR/latest.json"

SSH_ARGS=()
SCP_ARGS=()
if [ -n "$SSH_CONTROL_PATH" ]; then
    SSH_ARGS=(-o "ControlPath=$SSH_CONTROL_PATH")
    SCP_ARGS=(-o "ControlPath=$SSH_CONTROL_PATH")
fi

if [ ! -f "$MANIFEST_PATH" ]; then
    echo "缺少 $MANIFEST_PATH，请先构建 Windows GUI 包" >&2
    exit 1
fi

read_manifest() {
    python3 - "$MANIFEST_PATH" <<'PY'
import json
import re
import sys

with open(sys.argv[1], encoding="utf-8") as manifest_file:
    manifest = json.load(manifest_file)

name = manifest.get("url", "")
digest = manifest.get("sha256", "").lower()
if not re.fullmatch(r"[A-Za-z0-9._-]+\.zip", name):
    raise SystemExit("latest.json 中的 url 必须是安全的 zip 文件名")
if not re.fullmatch(r"[0-9a-f]{64}", digest):
    raise SystemExit("latest.json 中的 sha256 格式无效")
print(name)
print(digest)
PY
}

manifest_values="$(read_manifest)"
ARCHIVE_NAME="$(printf '%s\n' "$manifest_values" | sed -n '1p')"
EXPECTED_SHA256="$(printf '%s\n' "$manifest_values" | sed -n '2p')"
ARCHIVE_PATH="$DIST_DIR/$ARCHIVE_NAME"

if [ ! -f "$ARCHIVE_PATH" ]; then
    echo "缺少 $ARCHIVE_PATH" >&2
    exit 1
fi

if command -v shasum >/dev/null 2>&1; then
    ACTUAL_SHA256="$(shasum -a 256 "$ARCHIVE_PATH" | awk '{print $1}')"
else
    ACTUAL_SHA256="$(sha256sum "$ARCHIVE_PATH" | awk '{print $1}')"
fi
if [ "$ACTUAL_SHA256" != "$EXPECTED_SHA256" ]; then
    echo "zip SHA256 与 latest.json 不一致" >&2
    exit 1
fi

REMOTE_ARCHIVE_TMP="$REMOTE_DIR/.${ARCHIVE_NAME}.uploading"
REMOTE_MANIFEST_TMP="$REMOTE_DIR/.latest.json.uploading"

ssh "${SSH_ARGS[@]}" "$DEPLOY_HOST" "install -d -m 0755 '$REMOTE_DIR'"
scp "${SCP_ARGS[@]}" "$ARCHIVE_PATH" "$DEPLOY_HOST:$REMOTE_ARCHIVE_TMP"
ssh "${SSH_ARGS[@]}" "$DEPLOY_HOST" "printf '%s  %s\n' '$EXPECTED_SHA256' '$REMOTE_ARCHIVE_TMP' | sha256sum -c - && install -m 0644 '$REMOTE_ARCHIVE_TMP' '$REMOTE_DIR/$ARCHIVE_NAME' && rm -f '$REMOTE_ARCHIVE_TMP'"
scp "${SCP_ARGS[@]}" "$MANIFEST_PATH" "$DEPLOY_HOST:$REMOTE_MANIFEST_TMP"
ssh "${SSH_ARGS[@]}" "$DEPLOY_HOST" "install -m 0644 '$REMOTE_MANIFEST_TMP' '$REMOTE_DIR/latest.json' && rm -f '$REMOTE_MANIFEST_TMP'"

echo "部署完成: https://124.223.218.142/voice-qa/latest.json"
