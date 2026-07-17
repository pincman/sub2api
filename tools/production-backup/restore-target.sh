#!/usr/bin/env bash
# Restores an extracted bundle created by backup-target.sh. Run as root.
set -Eeuo pipefail
umask 077

BUNDLE_DIR="$(cd "$(dirname "$0")" && pwd)"
CONFIRM=""
RESTORE_ONEPANEL_DB="false"

usage() {
  cat <<'EOF'
Usage: restore.sh --yes [--restore-onepanel-db]

Restores Sub2API, PostgreSQL logical dumps, the consistent Redis RDB snapshot,
systemd configuration, reverse-proxy/SSL configuration and 1Panel app config.
--restore-onepanel-db additionally restores 1Panel's own SQLite databases and
briefly stops the 1Panel services. It is normally unnecessary for an
application rollback.
EOF
}

for arg in "$@"; do
  case "$arg" in
    --yes) CONFIRM="yes" ;;
    --restore-onepanel-db) RESTORE_ONEPANEL_DB="true" ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done

[[ "$(id -u)" -eq 0 ]] || { printf 'restore must run as root\n' >&2; exit 1; }
[[ "$CONFIRM" == "yes" ]] || { usage >&2; exit 2; }
[[ -f "$BUNDLE_DIR/manifest.sha256" ]] || { printf 'missing manifest\n' >&2; exit 1; }

(cd "$BUNDLE_DIR" && sha256sum -c manifest.sha256)

# shellcheck disable=SC1091
. "$BUNDLE_DIR/metadata/backup.env"
APP_DIR="${app_dir:-/opt/sub2api}"
PANEL_ROOT="/opt/1panel"
PG_APP_DIR="$PANEL_ROOT/apps/postgresql/postgresql"
REDIS_APP_DIR="$PANEL_ROOT/apps/redis/redis"
PG_CONTAINER="${postgres_container:?missing postgres_container metadata}"
REDIS_CONTAINER="${redis_container:?missing redis_container metadata}"
PG_USER="${postgres_user:?missing postgres_user metadata}"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
ROLLBACK_ROOT="/root/sub2api-restore-safety-$STAMP"
mkdir -p "$ROLLBACK_ROOT"
chmod 700 "$ROLLBACK_ROOT"

systemctl stop sub2api.service

if [[ -d "$APP_DIR" ]]; then
  mv "$APP_DIR" "$ROLLBACK_ROOT/sub2api.before-restore"
fi
tar --zstd -xpf "$BUNDLE_DIR/payload/sub2api.tar.zst" -C /

# Restore the systemd unit and the 1Panel-managed reverse-proxy, TLS and
# database/Redis configuration. The database data directories are excluded.
tar --zstd -xpf "$BUNDLE_DIR/payload/host-and-panel-config.tar.zst" -C /
systemctl daemon-reload

if [[ "$RESTORE_ONEPANEL_DB" == "true" ]]; then
  systemctl stop 1panel-core.service 1panel-agent.service || true
  mv "$PANEL_ROOT/db" "$ROLLBACK_ROOT/onepanel-db.before-restore"
  mkdir -p "$PANEL_ROOT/db"
  tar --zstd -xpf "$BUNDLE_DIR/payload/onepanel-db.tar.zst" -C "$PANEL_ROOT/db"
  systemctl start 1panel-agent.service 1panel-core.service
fi

set -a
# shellcheck disable=SC1090
. "$PG_APP_DIR/.env"
PG_PASSWORD="$PANEL_DB_ROOT_PASSWORD"
set +a

docker inspect "$PG_CONTAINER" >/dev/null
docker inspect "$REDIS_CONTAINER" >/dev/null

deadline=$((SECONDS + 120))
until docker exec -e "PGPASSWORD=$PG_PASSWORD" "$PG_CONTAINER" pg_isready -U "$PG_USER" -d postgres >/dev/null 2>&1; do
  (( SECONDS < deadline )) || { printf 'PostgreSQL did not become ready\n' >&2; exit 1; }
  sleep 2
done

# Restore all user databases. postgres itself is intentionally not dropped;
# it is the administrative connection database required by pg_restore.
while IFS='|' read -r db_name _owner _size; do
  [[ -n "$db_name" && "$db_name" != "postgres" ]] || continue
  safe_name="$(printf '%s' "$db_name" | tr -c 'A-Za-z0-9_.-' '_')"
  dump="$BUNDLE_DIR/payload/postgres/${safe_name}.dump"
  [[ -f "$dump" ]] || { printf 'missing database dump: %s\n' "$db_name" >&2; exit 1; }
  docker exec -i -e "PGPASSWORD=$PG_PASSWORD" "$PG_CONTAINER" \
    pg_restore -U "$PG_USER" -d postgres --clean --if-exists --create < "$dump"
done < "$BUNDLE_DIR/payload/postgres/databases.txt"

# Use the synchronous RDB snapshot; remove the live AOF so Redis cannot load
# post-backup commands from a stale append-only log.
docker stop "$REDIS_CONTAINER"
rm -rf "$REDIS_APP_DIR/data/appendonlydir"
install -D -m 600 "$BUNDLE_DIR/payload/redis/dump.rdb" "$REDIS_APP_DIR/data/dump.rdb"
docker start "$REDIS_CONTAINER"

set -a
# shellcheck disable=SC1090
. "$REDIS_APP_DIR/.env"
set +a
deadline=$((SECONDS + 120))
until docker exec -e "REDISCLI_AUTH=$PANEL_REDIS_ROOT_PASSWORD" "$REDIS_CONTAINER" redis-cli PING | grep -qx PONG; do
  (( SECONDS < deadline )) || { printf 'Redis did not become ready\n' >&2; exit 1; }
  sleep 2
done

OPENRESTY_CONTAINER="$(docker ps --format '{{.Names}} {{.Image}}' | awk '$2 ~ /openresty/ {print $1; exit}')"
if [[ -n "$OPENRESTY_CONTAINER" ]]; then
  docker kill -s HUP "$OPENRESTY_CONTAINER" >/dev/null || true
fi

systemctl start sub2api.service
systemctl is-active --quiet sub2api.service

printf 'restore_completed_at_utc=%s\n' "$(date -u -Is)"
printf 'previous_application_saved_at=%s\n' "$ROLLBACK_ROOT/sub2api.before-restore"
