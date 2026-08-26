#!/bin/bash

set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="$PROJECT_DIR/dist"
DEPLOY_HOST="${VOICE_QA_UPDATE_HOST:-root@124.223.218.142}"
REMOTE_DIR="${VOICE_QA_UPDATE_DIR:-/srv/voice-qa}"
SSH_CONTROL_PATH="${VOICE_QA_SSH_CONTROL_PATH:-}"
PUBLIC_MANIFEST_URL="${VOICE_QA_UPDATE_MANIFEST_URL:-https://124.223.218.142/voice-qa/latest.json}"
MANIFEST_PATH="$DIST_DIR/latest.json"
REMOTE_MANIFEST_SNAPSHOT="$(mktemp "${TMPDIR:-/tmp}/voice-qa-latest.XXXXXX.json")"

cleanup() {
    rm -f "$REMOTE_MANIFEST_SNAPSHOT"
}
trap cleanup EXIT

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

merge_remote_history() {
    if ! curl -fsSL --connect-timeout 10 --max-time 30 "$PUBLIC_MANIFEST_URL" -o "$REMOTE_MANIFEST_SNAPSHOT"; then
        echo "警告: 无法读取服务器历史，继续使用本地更新历史" >&2
        return
    fi

    python3 - "$MANIFEST_PATH" "$REMOTE_MANIFEST_SNAPSHOT" <<'PY'
import calendar
import datetime as dt
import json
import pathlib
import re
import sys

local_path, remote_path = map(pathlib.Path, sys.argv[1:])
local = json.loads(local_path.read_text(encoding="utf-8"))
remote = json.loads(remote_path.read_text(encoding="utf-8"))


def version_date(value):
    match = re.match(r"^(\d{4})\.(\d{2})(\d{2})", str(value or ""))
    if not match:
        return None
    try:
        return dt.date(int(match.group(1)), int(match.group(2)), int(match.group(3)))
    except ValueError:
        return None


def parse_date(value):
    try:
        return dt.date.fromisoformat(str(value or ""))
    except ValueError:
        return None


def note_items(value):
    source = value if isinstance(value, list) else str(value or "").splitlines()
    notes = []
    for item in source:
        text = re.sub(r"^\s*\d+[.、)]\s*", "", str(item)).strip()
        if text:
            notes.append(text)
    return notes


def as_history_entry(manifest):
    return {
        "version": manifest.get("version", ""),
        "date": (version_date(manifest.get("version")) or dt.date.min).isoformat(),
        "notes": note_items(manifest.get("notes", "")),
    }


def normalize(entry):
    if not isinstance(entry, dict):
        return None
    entry_version = str(entry.get("version", "")).strip()
    entry_date = parse_date(entry.get("date")) or version_date(entry_version)
    notes = note_items(entry.get("notes", []))
    if not entry_version or entry_date is None or not notes:
        return None
    return {
        "version": entry_version,
        "date": entry_date.isoformat(),
        "notes": notes,
    }


release_date = version_date(local.get("version")) or dt.date.today()
previous_year_day = min(release_date.day, calendar.monthrange(release_date.year - 1, release_date.month)[1])
cutoff = release_date.replace(year=release_date.year - 1, day=previous_year_day)
raw_history = [
    as_history_entry(local),
    *local.get("history", []),
    as_history_entry(remote),
    *remote.get("history", []),
]

history = []
seen = set()
for raw_entry in raw_history:
    entry = normalize(raw_entry)
    if entry is None or entry["version"] in seen:
        continue
    if parse_date(entry["date"]) < cutoff:
        continue
    seen.add(entry["version"])
    history.append(entry)
history.sort(key=lambda item: (item["date"], item["version"]), reverse=True)

local["history"] = history
local_path.write_text(json.dumps(local, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
PY
}

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

merge_remote_history
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
ssh "${SSH_ARGS[@]}" "$DEPLOY_HOST" "find '$REMOTE_DIR' -maxdepth 1 -type f -name '*.zip' ! -name '$ARCHIVE_NAME' -delete"

echo "部署完成: $PUBLIC_MANIFEST_URL"
echo "云服务器仅保留最新安装包: $ARCHIVE_NAME"
