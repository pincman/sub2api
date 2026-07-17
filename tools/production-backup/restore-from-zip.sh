#!/usr/bin/env bash
# Extracts a backup ZIP using Python (the target image does not ship unzip) and
# delegates to the verified restore.sh stored inside the archive.
set -Eeuo pipefail
umask 077

ARCHIVE="${1:-}"
shift || true
[[ -n "$ARCHIVE" && -f "$ARCHIVE" ]] || { printf 'Usage: %s /path/to/sub2api-full-*.zip --yes [--restore-onepanel-db]\n' "$0" >&2; exit 2; }
[[ "$(id -u)" -eq 0 ]] || { printf 'restore must run as root\n' >&2; exit 1; }
command -v python3 >/dev/null || { printf 'python3 is required to extract the ZIP\n' >&2; exit 1; }

WORK_DIR="$(mktemp -d /root/.sub2api-restore.XXXXXX)"
cleanup() { rm -rf "$WORK_DIR"; }
trap cleanup EXIT

export ARCHIVE WORK_DIR
python3 - <<'PY'
import os
import pathlib
import zipfile

archive = pathlib.Path(os.environ['ARCHIVE'])
destination = pathlib.Path(os.environ['WORK_DIR']).resolve()
with zipfile.ZipFile(archive) as zf:
    for member in zf.infolist():
        target = (destination / member.filename).resolve()
        if target != destination and destination not in target.parents:
            raise SystemExit(f'unsafe ZIP member: {member.filename}')
    zf.extractall(destination)
PY

exec "$WORK_DIR/bundle/restore.sh" "$@"
