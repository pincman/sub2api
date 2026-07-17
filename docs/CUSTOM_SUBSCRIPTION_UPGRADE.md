# Custom subscription upgrade maintenance

This fork keeps the quota-value subscription upgrade feature on the long-lived
`custom/subscription-upgrade` branch. Do not replace the deployment with an
upstream release archive: doing so would omit the custom migration and code.

## Billing contract

The server is authoritative. For an active monthly-quota subscription:

```text
remaining credit = purchased source price × (purchased monthly quota - used monthly quota) ÷ purchased monthly quota
upgrade payment  = target price - remaining credit
```

The credit and payment are rounded to two decimal places. A successful upgrade
starts a fresh target term, resets daily/weekly/monthly subscription usage and
window timestamps, and moves API keys from the source group to the target
group. The source subscription is temporarily suspended while its payment
order is pending so the quoted credit cannot change; cancellation, provider
failure, or timeout restores it automatically.

The source purchase price is recovered from its completed payment order rather
than the plan's editable current price. For chained upgrades, the prior upgrade
snapshot's full target price and quota are used.

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
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /tmp/sub2api-linux-amd64 ./cmd/server
```

Commit the merge to the custom branch and push it to `origin`. Tag each deployed
commit (for example `custom-prod-YYYYMMDD-HHMM`) so production can be rolled
back to an exact source revision.

## Production rule

Before every production deployment, create and validate a full backup archive
containing PostgreSQL dumps, Redis persistence, the complete application
directory, service definitions, reverse-proxy/SSL configuration, 1Panel
configuration, a checksum manifest, and the matching restore script. Keep a
verified copy off-server. Never deploy if archive validation or checksums fail.

