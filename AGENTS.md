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
- **Findings lifecycle:** `lt-triage` files `loadtest-finding` issues;
  fix PRs (in dating-coach) must re-run the suite against their head SHA
  plus all app-repo required checks before merge — see README "Findings".
- Keep `PAYMENTS_ENABLED=false` in `.env`; lt-payments flips it per
  run once the Stripe provider exists.

## Workflow skills — apply every session

Invoke these whenever their trigger matches — they are part of the working
protocol, not optional references (shared with the dating-coach app repo via
`.agents/skills/`):

- `dev-cycle` — end-to-end loop for any code-change task.
- `coding-efficiency` — every code-change task; smallest correct diff.
- `pr-granularity` — any change that splits into logical units.
- `review` — self-review the final `git diff` before reporting; `/review <PR#>` for GitHub PRs.
- `dating-coach-verify` — before reporting changes that touch dating-coach services.
- `testing-dating-coach` / `running-dating-coach-in-docker` — when bringing up or deploying the stack this repo tests/operates.
