#!/usr/bin/env bash
# Creates one verified, self-contained ZIP backup for the 1Panel + systemd
# deployment used by this server. Run as root on the target host.
set -Eeuo pipefail
umask 077

APP_DIR="${APP_DIR:-/opt/sub2api}"
PANEL_ROOT="${PANEL_ROOT:-/opt/1panel}"
BACKUP_ROOT="${BACKUP_ROOT:-/root/sub2api-backups}"
PG_APP_DIR="$PANEL_ROOT/apps/postgresql/postgresql"
REDIS_APP_DIR="$PANEL_ROOT/apps/redis/redis"
OPENRESTY_APP_DIR="$PANEL_ROOT/apps/openresty/openresty"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$BACKUP_ROOT"
chmod 700 "$BACKUP_ROOT"
WORK_DIR="$(mktemp -d "$BACKUP_ROOT/.sub2api-backup-${STAMP}.XXXXXX")"
BUNDLE_DIR="$WORK_DIR/bundle"
ARCHIVE="$BACKUP_ROOT/sub2api-full-${STAMP}.zip"

cleanup() {
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

die() {
  printf 'backup failed: %s\n' "$*" >&2
  exit 1
}

require_root() {
  [[ "$(id -u)" -eq 0 ]] || die "run this script as root"
}

wait_for_redis_save() {
  local deadline info
  docker exec -e "REDISCLI_AUTH=$PANEL_REDIS_ROOT_PASSWORD" "$REDIS_CONTAINER" redis-cli SAVE >/dev/null
  deadline=$((SECONDS + 120))
  while (( SECONDS < deadline )); do
    info="$(docker exec -e "REDISCLI_AUTH=$PANEL_REDIS_ROOT_PASSWORD" "$REDIS_CONTAINER" redis-cli INFO persistence | tr -d '\r')"
    if grep -qx 'rdb_bgsave_in_progress:0' <<<"$info" && grep -qx 'rdb_last_bgsave_status:ok' <<<"$info"; then
      return 0
    fi
    sleep 1
  done
  die "Redis SAVE did not complete successfully within 120 seconds"
}

copy_path() {
  local source="$1" destination="$2"
  [[ -e "$source" ]] || return 0
  mkdir -p "$destination"
  rsync -a "$source" "$destination/"
}

copy_file() {
  local source="$1" destination="$2"
  [[ -f "$source" ]] || return 0
  mkdir -p "$(dirname "$destination")"
  cp -a "$source" "$destination"
}

require_root
command -v docker >/dev/null || die "docker is required"
command -v tar >/dev/null || die "tar is required"
command -v zstd >/dev/null || die "zstd is required"
command -v python3 >/dev/null || die "python3 is required to create and verify the ZIP archive"
[[ -x "$APP_DIR/sub2api" ]] || die "missing application binary: $APP_DIR/sub2api"
[[ -f "$PG_APP_DIR/.env" ]] || die "missing PostgreSQL 1Panel environment file"
[[ -f "$REDIS_APP_DIR/.env" ]] || die "missing Redis 1Panel environment file"

mkdir -p "$BACKUP_ROOT" "$BUNDLE_DIR"/{metadata,payload/postgres,payload/redis,files}
chmod 700 "$BACKUP_ROOT" "$WORK_DIR" "$BUNDLE_DIR"

# 1Panel stores the container names and passwords in root-readable .env files.
set -a
# shellcheck disable=SC1090
. "$PG_APP_DIR/.env"
PG_CONTAINER="$CONTAINER_NAME"
PG_USER="$PANEL_DB_ROOT_USER"
PG_PASSWORD="$PANEL_DB_ROOT_PASSWORD"
# shellcheck disable=SC1090
. "$REDIS_APP_DIR/.env"
REDIS_CONTAINER="$CONTAINER_NAME"
set +a

docker inspect "$PG_CONTAINER" >/dev/null || die "PostgreSQL container is unavailable: $PG_CONTAINER"
docker inspect "$REDIS_CONTAINER" >/dev/null || die "Redis container is unavailable: $REDIS_CONTAINER"

install -m 700 "$(dirname "$0")/restore-target.sh" "$BUNDLE_DIR/restore.sh"
install -m 700 "$(dirname "$0")/restore-from-zip.sh" "$BUNDLE_DIR/restore-from-zip.sh"

{
  printf 'created_at_utc=%s\n' "$(date -u -Is)"
  printf 'hostname=%s\n' "$(hostname -f 2>/dev/null || hostname)"
  printf 'app_dir=%s\n' "$APP_DIR"
  printf 'sub2api_sha256=%s\n' "$(sha256sum "$APP_DIR/sub2api" | awk '{print $1}')"
  printf 'systemd_main_pid=%s\n' "$(systemctl show sub2api.service -p MainPID --value 2>/dev/null || true)"
  printf 'postgres_container=%s\n' "$PG_CONTAINER"
  printf 'redis_container=%s\n' "$REDIS_CONTAINER"
  printf 'postgres_user=%s\n' "$PG_USER"
} > "$BUNDLE_DIR/metadata/backup.env"

systemctl cat sub2api.service > "$BUNDLE_DIR/metadata/sub2api.service.rendered"
systemctl show sub2api.service > "$BUNDLE_DIR/metadata/sub2api.service.show"
docker ps --no-trunc > "$BUNDLE_DIR/metadata/docker-ps.txt"
docker inspect "$PG_CONTAINER" "$REDIS_CONTAINER" > "$BUNDLE_DIR/metadata/database-containers.json"
docker inspect $(docker ps -aq) > "$BUNDLE_DIR/metadata/all-containers.json" 2>/dev/null || true
df -hT > "$BUNDLE_DIR/metadata/filesystems.txt"
ss -lntp > "$BUNDLE_DIR/metadata/listening-ports.txt"

# Capture PostgreSQL roles plus an independently restorable dump for every
# non-template database. The application is not stopped: PostgreSQL's dump is
# transaction-consistent by design.
docker exec -e "PGPASSWORD=$PG_PASSWORD" "$PG_CONTAINER" \
  pg_dumpall -U "$PG_USER" --globals-only > "$BUNDLE_DIR/payload/postgres/globals.sql"
docker exec -e "PGPASSWORD=$PG_PASSWORD" "$PG_CONTAINER" \
  psql -U "$PG_USER" -d postgres -At -F '|' \
  -c "SELECT datname || '|' || pg_get_userbyid(datdba) || '|' || pg_database_size(datname) FROM pg_database WHERE datistemplate = false ORDER BY datname" \
  > "$BUNDLE_DIR/payload/postgres/databases.txt"

while IFS='|' read -r db_name db_owner db_size; do
  [[ -n "$db_name" ]] || continue
  safe_name="$(printf '%s' "$db_name" | tr -c 'A-Za-z0-9_.-' '_')"
  docker exec -e "PGPASSWORD=$PG_PASSWORD" "$PG_CONTAINER" \
    pg_dump -U "$PG_USER" -Fc --create --clean --if-exists -d "$db_name" \
    > "$BUNDLE_DIR/payload/postgres/${safe_name}.dump"
done < "$BUNDLE_DIR/payload/postgres/databases.txt"

# A synchronous Redis SAVE creates a point-in-time RDB snapshot. Redis AOF is
# intentionally not used for recovery because it may advance while files are
# copied; the RDB is consistent and matches the successful SAVE above.
wait_for_redis_save
rsync -a "$REDIS_APP_DIR/data/dump.rdb" "$BUNDLE_DIR/payload/redis/dump.rdb"
docker exec -e "REDISCLI_AUTH=$PANEL_REDIS_ROOT_PASSWORD" "$REDIS_CONTAINER" redis-cli DBSIZE \
  > "$BUNDLE_DIR/payload/redis/dbsize.txt"

# Preserve the application exactly (ownership, permissions, data and prior
# rollback materials), then preserve the 1Panel/host configuration separately.
tar --zstd -cpf "$BUNDLE_DIR/payload/sub2api.tar.zst" -C / opt/sub2api

copy_path /etc/systemd/system/sub2api.service "$BUNDLE_DIR/files/etc/systemd/system"
copy_file /etc/systemd/system/sub2api-custom-update.service "$BUNDLE_DIR/files/etc/systemd/system/sub2api-custom-update.service"
copy_file /etc/systemd/system/sub2api-custom-update.path "$BUNDLE_DIR/files/etc/systemd/system/sub2api-custom-update.path"
copy_file /etc/sudoers.d/sub2api-custom-update "$BUNDLE_DIR/files/etc/sudoers.d/sub2api-custom-update"
copy_path /etc/1panel "$BUNDLE_DIR/files/etc"
copy_file /usr/local/sbin/sub2api-custom-update "$BUNDLE_DIR/files/usr/local/sbin/sub2api-custom-update"
copy_path "$PANEL_ROOT/www" "$BUNDLE_DIR/files$PANEL_ROOT"
copy_path "$OPENRESTY_APP_DIR" "$BUNDLE_DIR/files$PANEL_ROOT/apps/openresty"
copy_path "$PG_APP_DIR/.env" "$BUNDLE_DIR/files$PANEL_ROOT/apps/postgresql/postgresql"
copy_path "$PG_APP_DIR/data.yml" "$BUNDLE_DIR/files$PANEL_ROOT/apps/postgresql/postgresql"
copy_path "$PG_APP_DIR/docker-compose.yml" "$BUNDLE_DIR/files$PANEL_ROOT/apps/postgresql/postgresql"
copy_file "$PG_APP_DIR/data/18/docker/postgresql.conf" "$BUNDLE_DIR/files$PG_APP_DIR/data/18/docker/postgresql.conf"
copy_file "$PG_APP_DIR/data/18/docker/postgresql.auto.conf" "$BUNDLE_DIR/files$PG_APP_DIR/data/18/docker/postgresql.auto.conf"
copy_file "$PG_APP_DIR/data/18/docker/pg_hba.conf" "$BUNDLE_DIR/files$PG_APP_DIR/data/18/docker/pg_hba.conf"
copy_file "$PG_APP_DIR/data/18/docker/pg_ident.conf" "$BUNDLE_DIR/files$PG_APP_DIR/data/18/docker/pg_ident.conf"
copy_path "$REDIS_APP_DIR/.env" "$BUNDLE_DIR/files$PANEL_ROOT/apps/redis/redis"
copy_path "$REDIS_APP_DIR/data.yml" "$BUNDLE_DIR/files$PANEL_ROOT/apps/redis/redis"
copy_path "$REDIS_APP_DIR/docker-compose.yml" "$BUNDLE_DIR/files$PANEL_ROOT/apps/redis/redis"
copy_path "$REDIS_APP_DIR/conf" "$BUNDLE_DIR/files$PANEL_ROOT/apps/redis/redis"
tar --zstd -cpf "$BUNDLE_DIR/payload/host-and-panel-config.tar.zst" -C "$BUNDLE_DIR/files" .
rm -rf "$BUNDLE_DIR/files"

mkdir -p "$BUNDLE_DIR/payload/onepanel-db"
rsync -a "$PANEL_ROOT/db/" "$BUNDLE_DIR/payload/onepanel-db/"
tar --zstd -cpf "$BUNDLE_DIR/payload/onepanel-db.tar.zst" -C "$BUNDLE_DIR/payload/onepanel-db" .
rm -rf "$BUNDLE_DIR/payload/onepanel-db"

(
  cd "$BUNDLE_DIR"
  find . -type f ! -name manifest.sha256 -print0 | sort -z | xargs -0 sha256sum > manifest.sha256
)

export BUNDLE_DIR ARCHIVE
python3 - <<'PY'
import os
import pathlib
import zipfile

bundle = pathlib.Path(os.environ['BUNDLE_DIR'])
archive = pathlib.Path(os.environ['ARCHIVE'])
with zipfile.ZipFile(archive, 'w', compression=zipfile.ZIP_STORED, allowZip64=True) as zf:
    for path in sorted(bundle.rglob('*')):
        if path.is_file():
            zf.write(path, path.relative_to(bundle.parent))
with zipfile.ZipFile(archive) as zf:
    broken = zf.testzip()
    if broken:
        raise SystemExit(f'ZIP CRC verification failed: {broken}')
PY
sha256sum "$ARCHIVE" > "$ARCHIVE.sha256"
cp "$BUNDLE_DIR/restore-from-zip.sh" "$BACKUP_ROOT/restore-sub2api-from-zip.sh"
chmod 700 "$BACKUP_ROOT/restore-sub2api-from-zip.sh"

printf 'backup_archive=%s\n' "$ARCHIVE"
printf 'backup_sha256=%s\n' "$ARCHIVE.sha256"
du -h "$ARCHIVE" "$ARCHIVE.sha256"
