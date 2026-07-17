#!/usr/bin/env bash
# Atomically installs a staged Sub2API binary on the systemd target. The caller
# must copy the new binary to /opt/sub2api/sub2api.custom-<revision>.new first.
set -Eeuo pipefail
umask 077

NEW_BINARY="${1:?usage: deploy-custom-binary.sh /opt/sub2api/sub2api.custom-<revision>.new <sha256>}"
EXPECTED_SHA="${2:?usage: deploy-custom-binary.sh /opt/sub2api/sub2api.custom-<revision>.new <sha256>}"
APP_DIR="/opt/sub2api"
CURRENT_BINARY="$APP_DIR/sub2api"
BACKUP_BINARY="$APP_DIR/sub2api.pre-custom-$(date -u +%Y%m%dT%H%M%SZ)"
MIGRATION_FILE="182_subscription_upgrades.sql"
SWITCHED="false"

rollback() {
  local rc=$?
  trap - ERR
  if [[ "$SWITCHED" == "true" && -f "$BACKUP_BINARY" ]]; then
    systemctl stop sub2api.service || true
    rm -f "$CURRENT_BINARY"
    mv "$BACKUP_BINARY" "$CURRENT_BINARY"
    chown sub2api:sub2api "$CURRENT_BINARY"
    chmod 0755 "$CURRENT_BINARY"
    systemctl start sub2api.service || true
  fi
  exit "$rc"
}
trap rollback ERR

[[ "$(id -u)" -eq 0 ]]
[[ -f "$NEW_BINARY" ]]
[[ "$(sha256sum "$NEW_BINARY" | awk '{print $1}')" == "$EXPECTED_SHA" ]]

chown sub2api:sub2api "$NEW_BINARY"
chmod 0755 "$NEW_BINARY"
systemctl stop sub2api.service
mv "$CURRENT_BINARY" "$BACKUP_BINARY"
mv "$NEW_BINARY" "$CURRENT_BINARY"
SWITCHED="true"
systemctl start sub2api.service

healthy="false"
for _ in $(seq 1 60); do
  if curl -fsS --max-time 3 http://127.0.0.1:8081/health | grep -q '"status":"ok"'; then
    healthy="true"
    break
  fi
  sleep 1
done
[[ "$healthy" == "true" ]]
systemctl is-active --quiet sub2api.service

docker exec 1Panel-postgresql-edxZ psql -U gpt -d gpt -At \
  -c "SELECT filename FROM schema_migrations WHERE filename = '$MIGRATION_FILE'" | grep -qx "$MIGRATION_FILE"
docker exec 1Panel-postgresql-edxZ psql -U gpt -d gpt -At -F '|' \
  -c "SELECT column_name, data_type FROM information_schema.columns WHERE table_name = 'payment_orders' AND column_name IN ('upgrade_source_subscription_id', 'upgrade_snapshot') ORDER BY column_name" \
  | grep -qx 'upgrade_snapshot|jsonb'
docker exec 1Panel-postgresql-edxZ psql -U gpt -d gpt -At -F '|' \
  -c "SELECT column_name, data_type FROM information_schema.columns WHERE table_name = 'payment_orders' AND column_name IN ('upgrade_source_subscription_id', 'upgrade_snapshot') ORDER BY column_name" \
  | grep -qx 'upgrade_source_subscription_id|bigint'

route_status="$(curl -sS -o /tmp/sub2api-upgrade-route.out -w '%{http_code}' http://127.0.0.1:8081/api/v1/payment/subscriptions/1/upgrade-options)"
[[ "$route_status" == "401" ]]

trap - ERR
printf 'deployment_health=ok route_status=%s binary_sha256=%s rollback_binary=%s\n' "$route_status" "$EXPECTED_SHA" "$BACKUP_BINARY"
