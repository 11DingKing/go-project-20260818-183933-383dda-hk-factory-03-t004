# sitesync — eval build

`sitesync` is a Go backend service for field-deployment offline-sync and
reconciliation. Field engineers record offline operation logs, replay them in
chronological order once connectivity is restored, and the platform reconciles
those records against workshop-reported hours, escalates overdue trial
deployments, and settles reconciliation bills per running period.

## What this image is for

This is the **evaluation image**. It keeps the full Go toolchain so the grader
can run every standard Go command inside the container. It is intentionally a
single stage (not a stripped runtime image).

## Build

```bash
# linux/amd64 (default)
./build_eval_docker.sh sitesync-eval linux/amd64

# linux/arm64
./build_eval_docker.sh sitesync-eval linux/arm64
```

`build_eval_docker.sh` runs:

```bash
docker build --platform "$DOCKER_PLATFORM" -f eval.Dockerfile -t "$IMAGE_NAME" .
```

Dependencies are downloaded only during `RUN go mod download`, so the image
compiles offline afterwards and does not pin or copy a CPU-specific toolchain.

## Standard commands (run inside the container or on the host)

```bash
go build ./...
go vet ./...
go test ./...
go test -race ./...
go run ./cmd/server -config config.example.yaml
go run ./cmd/opsctl init
```

## Data directory

All state is persisted to a pure-Go SQLite file under the configured data
directory (`storage.data_dir`, default `./data`). The directory is created on
first run and the schema is applied with `CREATE TABLE IF NOT EXISTS`, so
restarts recover in-flight work from disk. The data directory and db file can be
overridden with `SITESYNC_STORAGE_DATA_DIR` / `SITESYNC_STORAGE_DB_FILE`.

## Running the service (port 48557)

```bash
# default port 48557, ./data/sitesync.db
go run ./cmd/server

# override via env
SITESYNC_SERVER_PORT=48557 SITESYNC_STORAGE_DATA_DIR=/tmp/sitesync-data \
  go run ./cmd/server

# or with a config file
go run ./cmd/server -config config.example.yaml
```

The HTTP API listens on `:48557` by default. Health: `GET /healthz`,
`GET /readyz`.

## Operations CLI

`opsctl` shares the same SQLite store as the server. Subcommands: `init`,
`import`, `export`, `verify`, `rebuild-index`, `diagnose`, `requeue`.

```bash
go run ./cmd/opsctl init
go run ./cmd/opsctl verify
go run ./cmd/opsctl diagnose
```
