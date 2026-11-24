# loadtest guardrails

Load-testing harness. Each `cmd/lt-*` is a standalone component: its own
scenario, invariants, ramp, and report. `internal/harness` is thin plumbing
only (client, seed, metrics, dbstats, ramp, fakestripe) — scenario logic and
correctness assertions belong in the cmd, not the library.

## Run

```sh
# from the app repo root (sibling clone of this repo):
LT=../dating-coach-loadtest
docker compose --env-file $LT/.env \
  -f docker-compose.yml -f $LT/docker-compose.override.yml \
  --profile gateway up -d --build
# then in this repo:
go build ./... && go run ./cmd/lt-sanity
```

## Rules

- **Dev tool only** — never point a component at a production deployment.
- **No secrets in output:** reports must never contain JWTs, refresh tokens,
  verification codes, passwords, or message bodies; assert on ids/statuses.
- **Cleanup:** every run deletes its tagged fixtures (`*@loadtest.local`).
- **One component per PR** (per the pr-granularity playbook), stacked with
  `Depends on: #N` in the PR body.
- Keep `PAYMENTS_ENABLED=false` in `.env`; lt-payments flips it per
  run once the Stripe provider exists.
