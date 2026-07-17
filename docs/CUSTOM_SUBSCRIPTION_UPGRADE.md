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

## Merge an upstream release

Work from a clean tree and preserve both remotes:

```bash
git remote -v
git fetch origin --prune
git fetch upstream --tags --prune
git switch custom/subscription-upgrade
git pull --ff-only origin custom/subscription-upgrade
git merge --no-ff upstream/main
```

If the upstream default branch is not `main`, replace `upstream/main` with the
branch shown by `git remote show upstream`.

When resolving conflicts, preserve the behavior and schema documented here.
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

Commit the merge to the custom branch and push it to `origin`. Tag each deployed
commit (for example `custom-prod-YYYYMMDD-HHMM`) so production can be rolled
back to an exact source revision.

## In-app update notice

The upstream dashboard's **Update now** action downloads an official
`Wei-Shaw/sub2api` release and replaces `/opt/sub2api/sub2api`. It must never
be used on this fork because it cannot contain the custom upgrade code.

Custom production builds use `BuildType=custom`. They continue to show that an
upstream release exists and link to its changelog, but the binary update and
rollback API actions are blocked. To take an upstream release, use the merge
workflow above, test the merged fork, create the required full backup, and
deploy the resulting custom embedded binary with
`tools/production-backup/deploy-custom-binary.sh`.

## Production rule

Before every production deployment, create and validate a full backup archive
containing PostgreSQL dumps, Redis persistence, the complete application
directory, service definitions, reverse-proxy/SSL configuration, 1Panel
configuration, a checksum manifest, and the matching restore script. Keep a
verified copy off-server. Never deploy if archive validation or checksums fail.
