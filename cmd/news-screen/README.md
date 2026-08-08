# News screen service

This command runs the timezone-aware scheduler, the single-owner lifecycle
supervisor, and the news/alert/status gRPC API. It uses the Redis news store
and Kindle SSH controller.

```bash
go run ./cmd/news-screen \
  -kindle-address 192.168.0.10:22 \
  -ssh-user root \
  -ssh-key ~/.ssh/id_ed25519 \
  -ssh-known-hosts ~/.ssh/known_hosts \
  -redis localhost:6379 \
  -timezone Asia/Kolkata \
  -grpc 127.0.0.1:50050 \
  -news-refresh 15m \
  -news-per-genre 10 \
  -news-queue-limit 100 \
  -assets assets/genres
```

## Flags (common)

| Flag | Default | Purpose |
| --- | --- | --- |
| `-kindle-address` | `192.168.0.10:22` | Kindle SSH `host:port` |
| `-ssh-user` | `root` | SSH username |
| `-ssh-key` | `~/.ssh/id_ed25519` | Private key path |
| `-ssh-known-hosts` | `~/.ssh/known_hosts` | Host key database |
| `-ssh-insecure-host-key` | `false` | Disable host verification (dev only) |
| `-redis` | `localhost:6379` | Redis address |
| `-redis-password-file` | _(empty)_ | File containing Redis password (must not be group/world-readable) |
| `-redis-db` | `0` | Redis database index |
| `-redis-prefix` | _(empty)_ | Key prefix (empty keeps legacy unprefixed keys) |
| `-news-queue-limit` | `100` | Max retained stories per genre |
| `-timezone` | `Asia/Kolkata` | Schedule timezone |
| `-active-start` / `-active-stop` | `07:00` / `23:00` | Daily display window (`HH:MM`) |
| `-grpc` | `127.0.0.1:50050` | gRPC listen address |
| `-grpc-token-file` | _(empty)_ | Bearer token file (required with TLS on non-loopback unless mTLS) |
| `-grpc-tls-cert` / `-grpc-tls-key` | _(empty)_ | Server TLS materials |
| `-grpc-client-ca` | _(empty)_ | Require and verify client certificates |
| `-news-refresh` | `15m` | Wall-clock news-pass interval (must divide 1h) |
| `-news-per-genre` | `10` | Stories shown per genre each pass |
| `-genre-hold` / `-story-hold` | `10s` / `10s` | On-screen hold times |
| `-assets` | `assets/genres` | Genre backdrop directory |
| `-image-allow-private` | `false` | Allow story image URLs that resolve to private/local networks |
| `-alert-duration` | `30s` | How long each alert stays on screen |
| `-alert-queue-capacity` | `100` | Max waiting alerts |
| `-allow-alerts-outside-window` | `false` | Temporarily wake the display for alerts outside the active window |

See `main.go` for timeouts and remaining operational flags.

## Display lifecycle

**Clock is the default screen.** While the service is inside the active window
(default **07:00–23:00** in the configured timezone), the Kindle shows a
live updating clock.

**News runs on the wall clock.** With the default `-news-refresh 15m`, a genre-wise
news pass starts at every `:00`, `:15`, `:30`, and `:45`. When a pass finishes,
the service **clears the panel and repaints the clock** immediately so time is
visible again as soon as news ends. If a pass is still running when the next
quarter-hour fires, the tick is coalesced: after the current pass, the clock is
restored briefly and another pass runs.

**Shutdown blanks the display.** On **SIGTERM/SIGINT** and at the **scheduled
stop** (default 23:00), the service clears the e-ink panel and sets brightness
to **0**.

Each news pass shows up to 10 stories per genre by default, with 10-second genre
and story holds.

## gRPC surface

| RPC | Role |
| --- | --- |
| `AddNews` | Push a story into the Redis genre queue (deduped, bounded) |
| `AddAlert` | Enqueue an in-memory alert with optional `Severity` |
| `GetStatus` | Lifecycle state, desired-on, worker flags, queue depth, last error/timestamps |
| gRPC Health | Standard health service |

Non-loopback `-grpc` binds require **TLS** and either a bearer token
(`-grpc-token-file`, at least 32 characters) or mutual TLS (`-grpc-client-ca`).
Loopback binds may run without TLS/token for local development.

## Backgrounds

- **Genre screens** always load a full-screen image from `-assets` (default
  `assets/genres`). Match is case-insensitive (`India` → `india.png`); unknown
  genres fall back to `misc.png`.
- **Story screens** prefer the source **ogurl** image as the full-screen
  background. If ogurl is missing or the download fails, the same genre asset
  is used as a fallback so stories still have a photo background.

Story image downloads require public HTTPS destinations and re-check every
redirect. `-image-allow-private` relaxes the address restriction for trusted
private image servers and should not be enabled for untrusted ingestion.

## Security defaults

- SSH host verification uses `~/.ssh/known_hosts` unless
  `-ssh-insecure-host-key` is set.
- gRPC listens on loopback by default (`127.0.0.1:50050`).
- Redis passwords and gRPC tokens are loaded from files with restrictive
  permissions (no group/other access).
- Story image fetches block private, link-local, and loopback destinations
  unless explicitly allowed.

## Persistence notes

News is persisted in Redis (optional password, DB, and key prefix). The bounded
alert FIFO is intentionally in memory, so accepted alerts that have not finished
are lost if the process crashes. Scheduled desired-on state is reconstructed from
the configured clock on every boot.

Regenerate protobuf code (do not edit generated files):

```bash
PATH="$(go env GOPATH)/bin:$PATH" go generate ./api/proto
```
