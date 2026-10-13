# Technical Details

This document describes the runtime architecture, major subsystems, and operational assumptions of the Aetheria Social Backend based on the current repository layout and bootstrap code.

## 1. Purpose and scope

The service is a social backend for BasisVR. It provides:

- user lifecycle and profile management
- relationship and community/group handling
- activity feeds and social events
- presence and realtime messaging
- moderation and notification workflows
- optional federation via ActivityPub
- asset catalog integration
- metrics, tracing, and health-aware startup/shutdown

The main application entrypoint is `backend/cmd/api/main.go`, which wires together the database, observability stack, and the HTTP API router.

## 2. Runtime architecture

### High-level components

The repository is structured around a Go backend and a small supporting stack:

- `backend/cmd/api` – API server process
- `backend/cmd/delivery` – ActivityPub delivery worker
- `backend/cmd/retention` – cleanup/retention worker
- `backend/cmd/migrate` – migration runner
- `backend/internal` – domain logic, services, config, and adapters
- `backend/migrations` – schema migrations
- `backend/configs` – likely configuration templates and runtime config assets
- PostgreSQL 16 – transactional store
- Redis 7 – realtime pub/sub and replay cache
- MinIO – S3-compatible object storage
- Prometheus / Grafana / OpenTelemetry – observability stack

### Startup sequence

The application bootstrap in `backend/cmd/api/main.go` does the following:

1. Load configuration from environment variables.
2. Validate the configuration.
3. Initialize tracing.
4. Open the PostgreSQL connection.
5. Ensure the ActivityPub instance actor exists when federation is enabled.
6. Start the HTTP server with the API router.
7. Accept OS interrupt shutdown, then gracefully stop the HTTP server.

This means the server is intentionally designed as a long-lived service with explicit shutdown handling.

## 3. Repository structure

The Go backend is organized into internal packages by responsibility:

- `internal/activitypub` – federation and ActivityPub message handling
- `internal/actors` – actor identities and related logic
- `internal/api` – HTTP handlers, routing, and request pipeline
- `internal/auth` – authentication and token behavior
- `internal/common` – shared helpers
- `internal/community` – community/group logic
- `internal/config` – environment/config loading and validation
- `internal/database` – DB connection and access helpers
- `internal/events` – event definitions and event generation
- `internal/groups` – group behavior
- `internal/instances` – instance/federation metadata
- `internal/integration` – external integrations
- `internal/invites` – invite flows
- `internal/moderation` – moderation actions and policies
- `internal/notifications` – notification generation
- `internal/observability` – metrics, logs, and tracing
- `internal/presence` – presence tracking
- `internal/privacy` – privacy and consent constraints
- `internal/profiles` – user profile logic
- `internal/realtime` – realtime bus and websocket/event streaming
- `internal/relationships` – follows/friends/relationship states
- `internal/retention` – retention and cleanup workflows
- `internal/security` – security concerns and protection logic
- `internal/users` – user management
- `internal/worlds` – world/entity model support

This package split suggests a domain-oriented service rather than a monolithic controller-only server.

## 4. Service responsibilities

### API service (`backend/cmd/api`)

This is the main application entrypoint and HTTP server. It exposes the public social API and builds the router from `internal/api`. The service depends on:

- `internal/config` for runtime settings
- `internal/database` for persistence
- `internal/observability` for tracing/logging
- `internal/activitypub` for federation bootstrapping when enabled

### Delivery service (`backend/cmd/delivery`)

This worker is for sending ActivityPub payloads and handling federation delivery logic. It is included in the Compose stack and likely depends on the database and actor key encryption configuration.

### Retention service (`backend/cmd/retention`)

The retention worker periodically cleans up data according to configured policies. It is a background service, not a user-facing HTTP process.

### Migration runner (`backend/cmd/migrate`)

The Compose definition runs database migrations before starting the API process (`/app/migrate up && /app/api`). This is the expected pattern for schema evolution and deployment safety.

## 5. Persistence and realtime model

### PostgreSQL

The application stores durable social data in PostgreSQL, including user records, relationships, groups, feed content, and configuration. The Compose environment uses:

- database name: `basis_social`
- username: `basis`
- password: `basis`

### Redis

Redis is used as a realtime hub and ephemeral state store:

- realtime pub/sub channel: `basisvr:realtime:v1`
- replay retention, replay prefix, and replay limits are configured
- presence sweeps and time-based cleanup are configured in environment variables

This is important for low-latency social behavior such as online presence, event distribution, and buffering.

### MinIO

MinIO acts as the object-storage backend for file and asset use cases. It is S3-compatible and is included in the developer environment for local storage.

## 6. Security and identity model

Several configuration values indicate a security-conscious design:

- `JWT_SECRET` for authentication tokens
- `ACTOR_KEY_ENCRYPTION_KEY` for encrypting actor keys
- `TRUSTED_PROXY_CIDRS` for reverse-proxy trust boundaries
- `RATE_LIMIT_*` and `AUTH_RATE_LIMIT_REQUESTS` for abuse protection
- `CORS_ALLOWED_ORIGINS` for web origin control

The codebase and environment configuration also indicate that actor identity and federation are protected with encryption rather than plain-text storage.

## 7. Observability

The repository includes observability infrastructure:

- Prometheus for metrics collection
- Grafana for dashboards
- OpenTelemetry collector for tracing
- JSON logging and metrics toggles in the runtime config

This indicates the service is expected to operate in production-like environments with telemetry, not just local tests.

## 8. Local development environment

The root `docker-compose.yml` defines a complete local stack:

- PostgreSQL
- Redis
- MinIO
- backend API
- delivery worker
- retention worker
- optional Prometheus / Grafana / OTEL stack behind profiles

To run locally:

```bash
docker compose up --build
```

The API is exposed on `http://localhost:8080`. Related services expose PostgreSQL, Redis, and MinIO ports for local development.

The backend container command runs migrations and launches the API automatically:

```yaml
command: ["/bin/sh", "-c", "/app/migrate up && /app/api"]
```

## 9. Deployment assumptions

The system is designed to run with a multi-service deployment model, not as a single process.

The `deploy/README.md` describes a production Compose setup with:

- API service
- workers
- Caddy TLS termination
- external PostgreSQL and Redis
- migration-before-start behavior

This is consistent with the codebase structure and with the presence of separate `cmd/*` processes.

## 10. Operational notes

### Recommended development workflow

- keep local env values in the Compose stack for fast iteration
- use `migrate up` whenever schema changes are introduced
- validate ActivityPub and federation settings before enabling them in production
- ensure `ACTOR_KEY_ENCRYPTION_KEY` is stable and persistent across restarts
- protect `JWT_SECRET` and any runtime secrets from source control

### Typical troubleshooting checks

- verify Postgres health before the API starts
- verify Redis connectivity for realtime features
- confirm MinIO config if asset or file workflows fail
- inspect logs for config validation errors on startup
- ensure `ACTOR_KEY_ENCRYPTION_KEY` is set when federation features are enabled

## 11. Summary

This repository is a production-oriented social backend that combines:

- Go-based API architecture
- Postgres for durable storage
- Redis for realtime messaging and replay
- S3-compatible object storage via MinIO
- federation support via ActivityPub
- observability via Prometheus, Grafana, and OpenTelemetry
- worker services for delivery and retention

The code is intentionally modular and domain-driven, with a clear separation between HTTP API surfaces, persistence, realtime processing, and background jobs.
