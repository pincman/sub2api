#!/usr/bin/env bash
# Root-owned, parameterless production updater for the custom fork. It is
# started by a narrowly-scoped sudo rule and never accepts a URL, tag, or path
# from the web request. This keeps the site process from obtaining root access.
set -Eeuo pipefail
umask 077

readonly APP_DIR="/opt/sub2api"
readonly BACKUP_ROOT="/root/sub2api-backups"
readonly MAINTENANCE_DIR="/root/sub2api-maintenance"
readonly BACKUP_SCRIPT="$MAINTENANCE_DIR/backup-target.sh"
readonly DEPLOY_SCRIPT="$MAINTENANCE_DIR/deploy-custom-binary.sh"
readonly REPOSITORY="pincman/sub2api"
readonly LOCK_FILE="/run/lock/sub2api-custom-update.lock"

mkdir -p "$(dirname "$LOCK_FILE")"
exec 9>"$LOCK_FILE"
flock -n 9 || { printf 'custom update is already running\n' >&2; exit 1; }

require_file() {
  [[ -x "$1" ]] || { printf 'required executable is missing: %s\n' "$1" >&2; exit 1; }
}

[[ "$(id -u)" -eq 0 ]]
require_file "$APP_DIR/sub2api"
require_file "$BACKUP_SCRIPT"
require_file "$DEPLOY_SCRIPT"
command -v curl >/dev/null
command -v python3 >/dev/null
command -v sha256sum >/dev/null
command -v tar >/dev/null

WORK_DIR="$(mktemp -d /root/.sub2api-custom-update.XXXXXX)"
cleanup() { rm -rf "$WORK_DIR"; }
trap cleanup EXIT

export REPOSITORY WORK_DIR
python3 - <<'PY'
import json
import os
import re
import sys
import urllib.request

repo = os.environ["REPOSITORY"]
work = os.environ["WORK_DIR"]
request = urllib.request.Request(
    f"https://api.github.com/repos/{repo}/releases/latest",
    headers={"Accept": "application/vnd.github+json", "User-Agent": "sub2api-custom-updater"},
)
with urllib.request.urlopen(request, timeout=30) as response:
    release = json.load(response)

tag = str(release.get("tag_name", ""))
if not re.fullmatch(r"v\d+\.\d+\.\d+-custom\.[0-9a-f]{7,40}", tag):
    raise SystemExit(f"latest release is not a trusted custom release: {tag!r}")

assets = release.get("assets") or []
archive = next((a for a in assets if re.fullmatch(r"sub2api_.+_linux_amd64\.tar\.gz", str(a.get("name", "")))), None)
checksum = next((a for a in assets if a.get("name") == "checksums.txt"), None)
if not archive or not checksum:
    raise SystemExit("trusted custom release is missing linux_amd64 archive or checksums.txt")

for name, asset in (("archive", archive), ("checksum", checksum)):
    url = str(asset.get("browser_download_url", ""))
    if not url.startswith("https://github.com/"):
        raise SystemExit(f"invalid {name} download host")
    with open(os.path.join(work, name + ".url"), "w", encoding="utf-8") as out:
        out.write(url + "\n")
    with open(os.path.join(work, name + ".name"), "w", encoding="utf-8") as out:
        out.write(str(asset["name"]) + "\n")
PY

archive_url="$(<"$WORK_DIR/archive.url")"
checksum_url="$(<"$WORK_DIR/checksum.url")"
archive_name="$(<"$WORK_DIR/archive.name")"
archive_path="$WORK_DIR/$archive_name"
checksums_path="$WORK_DIR/checksums.txt"

curl --fail --location --proto '=https' --tlsv1.2 --retry 3 --connect-timeout 15 \
  "$archive_url" -o "$archive_path"
curl --fail --location --proto '=https' --tlsv1.2 --retry 3 --connect-timeout 15 \
  "$checksum_url" -o "$checksums_path"

expected_sha="$(awk -v name="$archive_name" '$2 == name { print $1 }' "$checksums_path")"
[[ "$expected_sha" =~ ^[a-f0-9]{64}$ ]]
[[ "$(sha256sum "$archive_path" | awk '{print $1}')" == "$expected_sha" ]]

mkdir -p "$WORK_DIR/extracted"
tar -xzf "$archive_path" -C "$WORK_DIR/extracted"
candidate="$(find "$WORK_DIR/extracted" -type f -name sub2api -print -quit)"
[[ -n "$candidate" && -f "$candidate" ]]
candidate_sha="$(sha256sum "$candidate" | awk '{print $1}')"
staged_binary="$APP_DIR/sub2api.custom-release.new"
install -m 0755 "$candidate" "$staged_binary"

# Every one-click update creates a self-contained database, Redis, service,
# 1Panel, proxy, TLS, and application backup before it changes the binary.
"$BACKUP_SCRIPT"
"$DEPLOY_SCRIPT" "$staged_binary" "$candidate_sha"

printf 'custom_update_completed_at_utc=%s release=%s binary_sha256=%s\n' \
  "$(date -u -Is)" "$archive_name" "$candidate_sha"
