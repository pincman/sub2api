# Custom subscription upgrade maintenance

This fork keeps the quota-value subscription upgrade feature on the long-lived
`custom/subscription-upgrade` branch. Do not replace the deployment with an
upstream release archive: doing so would omit the custom migration and code.

## Billing contract

The server is authoritative. For an active monthly-quota subscription:

```text
remaining credit = source group's monthly plan price × (source group's monthly quota - used monthly quota) ÷ source group's monthly quota
upgrade payment  = target price - remaining credit
```

The credit and payment are rounded to two decimal places. A successful upgrade
starts a fresh target term, resets daily/weekly/monthly subscription usage and
window timestamps, and moves API keys from the source group to the target
group. The source subscription is temporarily suspended while its payment
order is pending so the quoted credit cannot change; cancellation, provider
failure, or timeout restores it automatically.

The source value is always resolved from the source subscription group. Its
shortest for-sale term (normally the 30-day plan) is the monthly price baseline,
and the group's monthly quota is the quota baseline. Therefore a gifted,
admin-assigned, redeemed, or directly purchased subscription receives exactly
the same upgrade quote when it belongs to the same group and has the same usage.

## Automated upstream release flow

`.github/workflows/sync-custom-release.yml` runs whenever the custom branch is
pushed and once per hour. It fetches the newest stable `Wei-Shaw/sub2api` tag.
When that tag is not already included, it merges it into
`custom/subscription-upgrade`, runs the frontend and backend tests, builds an
embedded Linux binary with `BuildType=custom`, and publishes an immutable
release in `pincman/sub2api`.

If Git cannot merge cleanly or any test/build step fails, nothing is pushed or
released. Production keeps using its existing custom binary. A maintainer only
needs to resolve that conflict on the custom branch; the next workflow run
resumes the normal release process.

When resolving a conflict, preserve the behavior and schema documented here.
The primary custom files are:

- `backend/migrations/182_subscription_upgrades.sql`
- `backend/internal/service/payment_subscription_upgrade.go`
- `backend/internal/service/payment_subscription_upgrade_test.go`
- `backend/ent/schema/payment_order.go` and its generated Ent files
- payment order creation, fulfillment, cancellation, refund, route, and
  WeChat resume integration under `backend/internal`
- the payment API/types/flow and `frontend/src/views/user/PaymentView.vue`

If Ent schema conflicts were resolved, regenerate from `backend` with the Go
version required by `backend/go.mod`:

```bash
go generate ./ent
```

Then verify before committing:

```bash
cd frontend
corepack pnpm@9.15.9 install --frozen-lockfile
corepack pnpm@9.15.9 lint:check
corepack pnpm@9.15.9 typecheck
corepack pnpm@9.15.9 test:run
corepack pnpm@9.15.9 build

cd ../backend
go test ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags embed -trimpath -o /tmp/sub2api-linux-amd64 ./cmd/server
```

Tag each manually deployed commit (for example `custom-prod-YYYYMMDD-HHMM`) so
production can be rolled back to an exact source revision.

## In-app update notice

Custom production builds use `BuildType=custom`. Their dashboard's **Update
now** button never downloads `Wei-Shaw/sub2api`. It starts the root-owned,
parameterless `sub2api-custom-update` helper instead. That helper accepts no
user-supplied URL or version; it downloads only the latest checked custom
release from `pincman/sub2api`, verifies its checksum, creates a complete ZIP
backup, atomically deploys the binary, and verifies health, homepage, schema,
and upgrade-route checks before reporting success.

The helper requires the narrowly-scoped sudo/systemd setup installed by the
production deployment procedure. Rollback remains intentionally manual through
the verified full-backup restore script, because it restores database and
configuration state together with the binary.

## Production rule

Before every production deployment, create and validate a full backup archive
containing PostgreSQL dumps, Redis persistence, the complete application
directory, service definitions, reverse-proxy/SSL configuration, 1Panel
configuration, a checksum manifest, and the matching restore script. Keep a
verified copy off-server. Never deploy if archive validation or checksums fail.
