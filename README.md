# sitesync

A Go backend service for field-deployment **offline-sync and reconciliation**.
Robots are commissioned in offline workshops; engineers log operations while
disconnected and replay them in chronological order once reconnected. The
platform reconciles replayed records against workshop-reported hours, routes
conflicts to a human verifier or adjudicator, escalates overdue trial
deployments from the field engineer to the customer manager, and settles
reconciliation bills per running period.

## Architecture

- `internal/domain` — entities, value objects and explicit state machines
  (deployment, trial, record, batch, bill, inspection).
- `internal/service` — business orchestration: provisioning saga, offline
  backfill, conflict adjudication, trial escalation, reconciliation, queries.
- `internal/store` — pure-Go SQLite persistence (`modernc.org/sqlite`),
  optimistic locking, transaction helpers and repository implementations.
- `internal/scheduler` — background poller, escalator and restart recoverer
  (ticker-driven, graceful stop).
- `internal/httpapi` — chi router, handlers, middleware (request id, logging,
  panic recovery, no-cache).
- `internal/config` — YAML + env-var configuration.
- `cmd/server` — HTTP service entrypoint.
- `cmd/opsctl` — operations CLI sharing the same store.

Dependency direction is one-way: the HTTP layer never writes SQL, the domain
layer never imports HTTP or storage.

## Run

```bash
# default: port 48557, data dir ./data
go run ./cmd/server

# override port / data dir via env
SITESYNC_SERVER_PORT=48557 SITESYNC_STORAGE_DATA_DIR=./data go run ./cmd/server

# config file
go run ./cmd/server -config config.example.yaml
```

Health endpoints: `GET /healthz`, `GET /readyz`.

## Operations CLI

```bash
go run ./cmd/opsctl init        # create data dir + migrate
go run ./cmd/opsctl import --file masters.json
go run ./cmd/opsctl export --order <id> --out records.json
go run ./cmd/opsctl verify
go run ./cmd/opsctl rebuild-index
go run ./cmd/opsctl diagnose
go run ./cmd/opsctl requeue <failure-id>
```

## Build & test

```bash
go build ./...
go vet ./...
go test ./...
go test -race ./...
```

## Docker (evaluation image)

```bash
./build_eval_docker.sh sitesync-eval linux/amd64
./build_eval_docker.sh sitesync-eval linux/arm64
```

See `eval_README.md` for the evaluation image details.
