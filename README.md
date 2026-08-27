<div align="center">

# Atlas

**Turn a jailbroken Kindle into a scheduled news and clock display**

Atlas is a Go service that turns a jailbroken Kindle into a scheduled editorial display. It combines a timezone-aware cron scheduler, a single-owner lifecycle supervisor, Redis-backed genre queues, signal-driven background work, and SSH/fbink rendering. With the defaults, the 07:00–23:00 active window provides 64 wall-clock news-pass opportunities per day; each active genre retains up to 100 stories and contributes up to 10 stories per pass. The gRPC surface exposes three application RPCs—news ingestion, alerts, and status—plus standard health checks.

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![gRPC](https://img.shields.io/badge/API-gRPC-244c5a?logo=grpc&logoColor=white)](https://grpc.io/)
[![Redis](https://img.shields.io/badge/Store-Redis-DC382D?logo=redis&logoColor=white)](https://redis.io/)
[![Platform](https://img.shields.io/badge/Display-Kindle%20e--ink-111111)](#architecture)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

</div>

---

## Overview

Atlas accepts news stories and short alerts, stores stories in Redis by genre, and paints them on a jailbroken Kindle over SSH. During the active daily window the device shows a live clock; on each configured wall-clock boundary it runs a genre-wise news pass (title screens, then stories with photo backgrounds), then restores the clock. Alerts can pause news, display a message for a fixed duration, and release the display back to the normal lifecycle. The result is a wall-mounted editorial screen that updates without apps, browsers, or a continuous GUI process on the Kindle itself.

The primary long-running binary is `cmd/news-screen`: timezone-aware cron scheduling, a single-owner lifecycle supervisor, and a gRPC API for news, alerts, and status. A simpler root CLI (`app.go`) supports one-shot `clock`, `story`, and continuous `news` modes for development and manual control. Rendering uses [fbink](https://github.com/NiLuJe/FBInk) on the device; the host uses system OpenSSH (not a pure-Go dial) so macOS LaunchAgents can reach LAN Kindles. Layouts are resolution-relative so the same code can target different Kindle panel sizes.

## Performance & scale

Repository-derived limits and capacity calculations from the default configuration. The repository does not contain production telemetry or a load-test result, so runtime latency, throughput, uptime, and cost impact are intentionally not presented as measured outcomes.

| Dimension | Figure | Source |
| --- | --- | --- |
| News-pass cadence | **64 pass opportunities/day** during the default 16-hour active window (15-minute wall-clock cadence; 16 × 4); requests outside the window are ignored | `internal/config/config.go`, `internal/scheduler/scheduler.go`, `internal/supervisor/supervisor.go` |
| Content-frame budget | Up to **44 content frames/pass** and **2,816 frames/day** when four genres are present in Redis and each has 10 stories (64 × 4 × (1 + 10)); nominal configured dwell time is **440 seconds/pass**, while wall time depends on SSH, image, and rendering operations | `cmd/news-screen/main.go`, `internal/dashboard/dashboard.go` |
| Clock refresh budget | Up to **960 minute-boundary clock updates/day** in the active window (16 × 60), each targeting one clock region with a non-flashing GC16 refresh | `internal/kindle/clock.go` |
| Redis retention | Each genre queue retains **100 stories by default**; pushes use an atomic Lua dedupe/append/trim script with a **24-hour dedupe TTL**, and malformed items go to a **100-entry** dead-letter list | `internal/news/store.go` |
| Image safety and memory bounds | **12 MiB** maximum download, **15 s** HTTP timeout, at most **5 redirects**, **40 million pixels** maximum decoded image area, and a prepared-image cache capped at **128 entries / 64 MiB** | `internal/kindle/story.go` |
| Alert handling | **100 waiting alerts** by default, **30 s** display duration, up to **2 s** clear timeout, and a **5 s** worker-stop timeout for news preemption | `internal/config/config.go`, `internal/supervisor/supervisor.go` |
| Recovery policy | Display startup makes **3 attempts** by default with **1 s → 2 s → 4 s** delays; failed news workers restart with exponential backoff capped at **60 s** | `internal/config/config.go`, `internal/supervisor/supervisor.go` |
| Concurrency and admission | Single-owner supervisor with a **256-slot** command queue and a buffer-1 refresh signal; gRPC is configured for **32 concurrent streams** and **1 MiB** maximum receive/send messages | `internal/supervisor/supervisor.go`, `cmd/news-screen/main.go` |
| API validation | Default **30 s** unary deadline; bearer token minimum **32 characters** with constant-time comparison; news title/description caps of **512 B / 4 KiB** and at most **10 sources** per item | `internal/config/config.go`, `internal/grpcserver` |
| Unattended operation | **16 h/day** active window by default; outside it the panel is cleared and backlight set to 0, and shutdown performs the same cleanup | `internal/scheduler`, `internal/kindledisplay/controller.go` |
| Test and delivery surface | **63 top-level test functions across 14 test files**; CI runs formatting, vet, Redis 7-backed tests, race tests, coverage generation, `govulncheck`, and generated-protobuf drift verification | repo tree, `.github/workflows/ci.yml` |

*Frame totals are derived from defaults under the stated four-genre/full-queue assumption. They count content frames, not individual fbink or SSH invocations.*

## Engineering highlights

- **Serialized lifecycle control:** a single supervisor goroutine owns display, news-worker, and alert state. Callers communicate through a bounded command channel, while refresh requests coalesce instead of creating unbounded work when a pass overlaps the next tick.
- **Atomic queue semantics:** Redis Lua scripts combine deduplication, append, and retention trimming; `LMOVE` rotates stories without deleting them, and invalid JSON is quarantined in a bounded dead-letter list.
- **Interruptible asynchronous workflow:** the live clock is the default screen, news passes run only on cron or ingestion signals, and alerts cancel the news worker, wait for its stop timeout, then resume or shut down according to lifecycle state.
- **Defensive edge processing:** image URLs require public HTTPS by default and are revalidated across redirects; downloaded data, decoded dimensions, prepared-image memory, gRPC messages, and user-provided fields all have explicit bounds.

## Features

| Area | What the project provides |
| --- | --- |
| **Scheduled display window** | Daily start/stop in a configured timezone (default 07:00–23:00 Asia/Kolkata). Outside the window the panel is cleared and backlight is set to 0. |
| **Clock as default UI** | Live `HH:MM` clock with date chrome; minute-boundary updates use a single non-flashing GC16 regional refresh to limit ghosting. |
| **Genre-wise news passes** | Wall-clock cron (default every 15 minutes at `:00/:15/:30/:45`) walks Redis genres: genre title screen, then up to N stories per genre (default 10), each held for a configurable duration. |
| **Story presentation** | Editorial layout: genre label, title, description, source domains. Prefers Open Graph images as full-screen backgrounds; falls back to genre assets under `assets/genres`. Story image fetches require public HTTPS by default (SSRF-safe); `-image-allow-private` is opt-in for trusted private hosts. |
| **Redis news queues** | Normalized genre queues are bounded (`-news-queue-limit`, default 100), deduplicated for 24 hours, rotated with LMOVE, and protected by a poison-item dead letter. Optional `-redis-password-file`, `-redis-db`, and `-redis-prefix` for auth, database selection, and key namespacing. |
| **gRPC API** | `AddNews`, `AddAlert`, and `GetStatus` (lifecycle, desired-on, worker flags, queue depth, last error/timestamps), plus standard gRPC health. Field validation, operation IDs / `CommandState`, alert `Severity`, structured logging, panic recovery, deadlines, bearer auth, and TLS/mTLS. |
| **Alert FIFO** | Bounded in-memory queue (default capacity 100). Alerts carry severity (`info` / `warning` / `critical`), interrupt news, display for a fixed duration (default 30s), then resume the lifecycle. Rejected when full or when the service is shutting down. |
| **Lifecycle supervisor** | Single-owner state machine (`off` → `starting` → `running` / `pausing` / `alerting` → `stopping` / `failed`) owns display power, news worker, and alert presentation—no competing writers. Start failures retry with backoff. |
| **CLI modes** | Root binary: `clock`, `story` (JSON from file or stdin), and `news` (continuous Redis-driven loop) for local control without the full service. Shares SSH/Redis/image policy flags with the service. |
| **Device control over SSH** | Rotation, backlight, clear, text, and image upload via system OpenSSH + fbink. Strict host-key verification via `known_hosts` by default; user, key path, and known-hosts file are configurable. Startup fails fast if the Kindle is unreachable. |

> [!NOTE]
> **Completed:** news-screen service (scheduler, supervisor, gRPC with `GetStatus`/severity), Redis news store (prefix/limit options), genre/story painting with public-image policy, clock default between passes, alert queue, OpenSSH host-key controls, bearer/TLS hardening for non-loopback gRPC, CLI modes, CI, and unit/integration tests for core packages.
>
> **Partial / placeholder:** clock dashboard climate and metric columns show `--` until a `MetricsProvider` is wired to live sensors.
>
> **Operational gaps (by design or deferred):** alert FIFO is in-memory only—accepted but unfinished alerts are lost on process crash; scheduled desired-on state is reconstructed from the clock on every boot.

## From input to result

```mermaid
flowchart LR
  subgraph Ingest
    A[gRPC AddNews] --> B[Redis genre queues]
    C[gRPC AddAlert] --> D[In-memory alert FIFO]
  end
  subgraph Schedule
    E[Cron: daily start/stop] --> F[Supervisor]
    G[Cron: news pass ticks] --> F
  end
  B --> F
  D --> F
  F --> H{Active window?}
  H -->|no| I[Clear panel + backlight 0]
  H -->|yes| J[Display on]
  J --> K[Live clock]
  G --> L[News pass]
  L --> M[Genre screens + stories]
  M --> K
  D --> N[Show alert]
  N --> K
  M --> O[Kindle via SSH / fbink]
  K --> O
  N --> O
```

News passes are signal-driven, not busy-polled: the worker idles on a live clock until a pass signal arrives (cron tick or `NotifyNewsChanged` after `AddNews`). If a tick arrives while a pass is still running, the signal is coalesced so one follow-up pass runs after the current cycle ends and the clock is restored briefly. Alerts preempt news by cancelling the news worker, waiting for a clean stop (with a worker-stop timeout), then presenting the alert head of the FIFO. Display start retries with backoff on failure.

## Product surface

What the Kindle actually shows, organized by product concern rather than packages:

```mermaid
flowchart TD
  Atlas[Atlas Kindle screen]
  Atlas --> Clock[Clock dashboard]
  Atlas --> News[News passes]
  Atlas --> Alerts[Alerts]
  Clock --> Time[Live time + date]
  Clock --> Climate[Climate + metrics placeholder]
  News --> Genre[Genre title + backdrop]
  News --> Story[Story card]
  Story --> Title[Title / description / sources]
  Story --> BG[OG image or genre fallback]
  Alerts --> Msg[Full-screen message + severity]
```

Genre backdrop files ship under `assets/genres` (`india`, `mumbai`, `world`, `misc`; matching is case-insensitive; unknown genres use `misc`).

## Architecture

```mermaid
flowchart TB
  subgraph Presentation
    CLI[Root CLI clock / story / news]
    SVC[cmd/news-screen]
    GRPC[gRPC NewsScreenService]
  end
  subgraph Domain
    SUP[Supervisor lifecycle]
    SCH[Cron scheduler]
    NWF[newsworkflow Worker]
    DASH[dashboard]
    ALR[alert presenter]
  end
  subgraph Device
    KDC[kindledisplay.Controller]
    KD[kindle.Device]
    SSH[sshclient OpenSSH]
    FB[Kindle fbink]
  end
  subgraph Data
    RS[news.Store]
    RD[(Redis)]
  end
  CLI --> KD
  CLI --> RS
  SVC --> SCH
  SVC --> SUP
  SVC --> GRPC
  GRPC --> SUP
  GRPC --> RS
  SCH --> SUP
  SUP --> KDC
  SUP --> NWF
  SUP --> ALR
  NWF --> DASH
  NWF --> KDC
  DASH --> RS
  DASH --> KD
  KDC --> KD
  KD --> SSH
  SSH --> FB
  RS --> RD
```

**Conventions that matter in this codebase:**

- **Single owner for lifecycle:** only the supervisor mutates display/news/alert run state via a command channel; external callers (`Reconcile`, `AddAlert`, cron hooks) send commands and wait for replies.
- **Interfaces at the edges:** `display.Controller`, `newsworker.Worker`, and `alert.Presenter` keep device and refresh strategies swappable for tests.
- **Redis is the news source of truth;** alerts are intentionally not durable.
- **SSH via system OpenSSH** (not a pure-Go SSH stack) so LaunchAgents on macOS can reach LAN hosts without a Go TCP dial (which hits Local Network TCC restrictions). Host keys are verified against `known_hosts` unless `-ssh-insecure-host-key` is set for local development.
- **gRPC interceptors** apply a default timeout when the client omits a deadline, log request IDs, recover panics, and optionally enforce bearer tokens (`-grpc-token-file`, minimum 32 characters). Non-loopback binds also require TLS and either a bearer token or mTLS (`-grpc-client-ca`).
- **Story image fetches** validate public destinations (and re-check redirects) unless `-image-allow-private` is enabled.
- **Protobuf is generated** into `gen/`; edit `api/proto` and regenerate—do not hand-edit stubs.

## Tech stack

| Layer | Technology |
| --- | --- |
| Language | [Go](https://go.dev/) 1.26 (module `github.com/aneeshpatne/atlas`) |
| API | [gRPC](https://grpc.io/) + [Protocol Buffers](https://protobuf.dev/) (`google.golang.org/grpc`, `protobuf`) |
| Persistence | [Redis](https://redis.io/) via [go-redis/v9](https://github.com/redis/go-redis) |
| Scheduling | [robfig/cron/v3](https://github.com/robfig/cron) (timezone-aware) |
| Device transport | System OpenSSH only (`/usr/bin/ssh` preferred, `PATH` fallback)—no pure-Go SSH library |
| Imaging | [golang.org/x/image](https://pkg.go.dev/golang.org/x/image) (JPEG/GIF/WebP decode, story background prep, e-ink tone mapping) |
| On-device UI | [fbink](https://github.com/NiLuJe/FBInk) over SSH; Instrument Serif / Helvetica fonts on the Kindle |
| Logging | `log/slog` JSON handler in the service binary |
| CI | GitHub Actions: `gofmt`, `go vet`, unit/race/coverage tests (Redis service), `govulncheck`, generated-proto drift check |
| Testing | Go standard library `testing` (unit + opt-in Redis integration via `ATLAS_TEST_REDIS_ADDR`) |

## Project structure

```text
.
├── app.go                      # CLI: clock | story | news
├── cmd/news-screen/            # Long-running service entrypoint
│   ├── main.go
│   ├── main_test.go
│   └── README.md               # Service-specific flags and lifecycle notes
├── api/proto/screen/v1/        # Protobuf sources (news, alerts, status)
├── gen/screen/v1/              # Generated pb / gRPC stubs
├── assets/genres/              # Genre backdrop images (india, mumbai, world, misc)
├── .github/workflows/ci.yml    # Format, vet, test, race, coverage, vuln, proto check
├── internal/
│   ├── config/                 # Defaults, validation, timeouts
│   ├── supervisor/             # Single-owner lifecycle state machine
│   ├── scheduler/              # Daily window + news-pass cron specs
│   ├── grpcserver/             # RPC handlers, bearer auth, unary interceptors
│   ├── news/                   # Redis store (prefix/limit options) and Story types
│   ├── screennews/             # Protobuf-to-domain Redis ingestion adapter
│   ├── dashboard/              # Genre cycles and story draining
│   ├── newsworkflow/           # Clock-default loop + pass signals
│   ├── newsworker/             # Supervisor-facing worker interface
│   ├── kindle/                 # Device commands, clock, story, genre paint
│   ├── kindledisplay/          # display.Controller + alert presenter
│   ├── display/                # Controller interface
│   ├── alert/                  # Alert model (severity) and timed presentation
│   ├── sshclient/              # OpenSSH wrapper (context APIs, known_hosts)
│   └── redis/                  # Thin Redis client
└── story.json                  # Sample story payload for CLI story mode
```

## Requirements

- **Go** 1.26+ (module declares `go 1.26.4`)
- **Redis** reachable at the address you pass (default `localhost:6379`)
- **Jailbroken Kindle** (tested on a PaperWhite 3, 1448×1072 @ 300 DPI, fbink v1.25.0) with:
  - SSH reachable at the configured address (default `192.168.0.10:22`) and user (default `root` via `-ssh-user`)
  - Private key at `~/.ssh/id_ed25519` by default (`-ssh-key`)
  - A matching entry in `~/.ssh/known_hosts` by default (`-ssh-known-hosts`); use `-ssh-insecure-host-key` only for local development
  - [fbink](https://github.com/NiLuJe/FBInk) at `/mnt/us/usbnet/bin/fbink`
  - Fonts used by rendering (e.g. `/mnt/us/fonts/InstrumentSerif-Regular.ttf`, optional Helvetica under `/usr/java/lib/fonts/`)
- **Network access** from the host to the Kindle (LAN); for story backgrounds, outbound HTTPS to fetch **public** Open Graph images (private/link-local destinations are blocked unless `-image-allow-private` is set)
- **Genre assets directory** (default `assets/genres`) present when running news-screen
- **Optional secrets as files:** Redis password (`-redis-password-file`) and gRPC bearer token (`-grpc-token-file`) must be readable files; the service rejects world/group-readable secret files

**Not required for unit tests:** a physical Kindle or Redis (tests use fakes and pure logic). **Required for real display:** reachable Kindle + key; **for news mode / service:** Redis as well. Set `ATLAS_TEST_REDIS_ADDR` to exercise the Redis integration test. The service is intended to run on a host machine that keeps SSH open to the device, not on the Kindle itself.

## Getting started

1. **Clone the repository**

   ```bash
   git clone https://github.com/aneeshpatne/atlas.git
   cd atlas
   ```

2. **Install dependencies**

   ```bash
   go mod download
   ```

3. **Ensure Redis is running** (news mode and the service)

   ```bash
   redis-cli ping   # expect PONG
   ```

4. **Confirm SSH to the Kindle**

   ```bash
   ssh -i ~/.ssh/id_ed25519 root@192.168.0.10 true
   ```

5. **Run the full news-screen service**

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

   Optional hardening / multi-tenant Redis:

   ```bash
   go run ./cmd/news-screen \
     -kindle-address 192.168.0.10:22 \
     -redis redis.example:6379 \
     -redis-password-file /run/secrets/redis_password \
     -redis-db 1 \
     -redis-prefix atlas:prod: \
     -grpc 0.0.0.0:50050 \
     -grpc-tls-cert /etc/atlas/tls.crt \
     -grpc-tls-key /etc/atlas/tls.key \
     -grpc-token-file /run/secrets/grpc_token \
     -assets assets/genres
   ```

6. **Or use the CLI for a single mode**

   ```bash
   # Live clock
   go run . -address 192.168.0.10:22 clock

   # One story from JSON
   go run . -address 192.168.0.10:22 -story-file story.json story

   # Continuous news loop from Redis
   go run . -address 192.168.0.10:22 -redis localhost:6379 news
   ```

7. **Regenerate protobuf stubs** (after editing protos)

   ```bash
   PATH="$(go env GOPATH)/bin:$PATH" go generate ./api/proto
   ```

> [!IMPORTANT]
> gRPC binds to `127.0.0.1:50050` by default. A non-loopback bind requires **TLS** plus either `-grpc-token-file` (token ≥ 32 characters) or mutual TLS (`-grpc-client-ca`). SSH host verification is strict by default (`-ssh-known-hosts`); `-ssh-insecure-host-key` disables it for development only. Story image downloads reject private/local destinations unless `-image-allow-private` is set.

## Running tests

**Command line** (from the repository root):

```bash
go test ./...
```

Run a single package with verbose output:

```bash
go test ./internal/supervisor -v
go test ./internal/kindle -count=1
```

**What the suite covers:** config and scheduler behavior; supervisor FIFO, cancellation, backoff, and shutdown; gRPC validation, bearer auth, `GetStatus`, and error mapping; newsworkflow signaling; backdrop/story rendering and public-image policy; SSH quoting, host/port validation, and context cancellation; and opt-in Redis integration (prefix/limit/dedupe). Set `ATLAS_TEST_REDIS_ADDR` (for example `127.0.0.1:6379`) to run the Redis integration test locally.

The current tree contains **63 top-level test functions across 14 test files**. The CI workflow generates coverage but does not enforce a minimum coverage threshold; it also runs the race detector and the Redis integration path.

GitHub Actions (`.github/workflows/ci.yml`) runs formatting, vet, unit tests against a Redis service, the race detector, coverage, `govulncheck`, and a generated-protobuf drift check.

## Roadmap

- Wire a live sensor implementation into the clock's `MetricsProvider` interface.
- Optional durable alert queue so accepted alerts survive process restarts.
- End-to-end tests against a complete fake Kindle/fbink environment.

## License

This project is licensed under the [Apache License 2.0](LICENSE).

You may use, modify, and redistribute the software, including in commercial products, provided you include a copy of the license, state significant changes to modified files, and retain existing copyright and attribution notices. Contributions are accepted under the same terms unless otherwise stated. The software is provided on an “AS IS” basis, without warranties. The patent grant terminates if you initiate patent litigation over the work.

---

<div align="center">
  Built in Go for Redis-backed news and SSH-driven Kindle e-ink—quiet by default, loud when it matters.
</div>
