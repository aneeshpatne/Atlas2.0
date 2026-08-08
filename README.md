<div align="center">

# Atlas

**Turn a jailbroken Kindle into a scheduled news and clock display**

Atlas drives an e-ink Kindle over SSH: a live clock by default, genre-wise news passes on a wall-clock cadence, and interruptible alerts—all orchestrated as a long-running service with a gRPC API.

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

## Features

| Area | What the project provides |
| --- | --- |
| **Scheduled display window** | Daily start/stop in a configured timezone (default 07:00–23:00 Asia/Kolkata). Outside the window the panel is cleared and backlight is set to 0. |
| **Clock as default UI** | Live `HH:MM` clock with date chrome; minute-boundary updates use a single non-flashing GC16 regional refresh to limit ghosting. |
| **Genre-wise news passes** | Wall-clock cron (default every 15 minutes at `:00/:15/:30/:45`) walks Redis genres: genre title screen, then up to N stories per genre (default 10), each held for a configurable duration. |
| **Story presentation** | Editorial layout: genre label, title, description, source domains. Prefers Open Graph images as full-screen backgrounds; falls back to genre assets under `assets/genres`. |
| **Redis news queues** | Normalized genre queues are bounded (default 100), deduplicated for 24 hours, rotated with LMOVE, and protected by a poison-item dead letter. Genres are displayed deterministically. |
| **gRPC API** | `AddNews`, `AddAlert`, and `GetStatus`, plus standard gRPC health. Includes field validation, operation IDs, structured logging, panic recovery, deadlines, optional bearer authentication, and TLS/mTLS. |
| **Alert FIFO** | Bounded in-memory queue (default capacity 100). Alerts interrupt news, display for a fixed duration (default 30s), then resume the lifecycle. Rejected when full or when the service is shutting down. |
| **Lifecycle supervisor** | Single-owner state machine (`off` → `starting` → `running` / `pausing` / `alerting` → `stopping` / `failed`) owns display power, news worker, and alert presentation—no competing writers. |
| **CLI modes** | Root binary: `clock`, `story` (JSON from file or stdin), and `news` (continuous Redis-driven loop) for local control without the full service. |
| **Device control over SSH** | Rotation, backlight, clear, text, and image upload via OpenSSH + fbink. Startup fails fast if the Kindle is unreachable. |

> [!NOTE]
> **Completed:** news-screen service (scheduler, supervisor, gRPC), Redis news store, genre/story painting, clock default between passes, alert queue, CLI modes, unit tests for core packages.
>
> **Partial / placeholder:** clock dashboard climate and metric columns show `--` until a `MetricsProvider` is wired to live sensors.
>
> **Operational gaps (by design or deferred):** alert FIFO is in-memory only—accepted but unfinished alerts are lost on process crash; scheduled desired-on state is reconstructed from the clock on every boot. gRPC binds to loopback by default and supports bearer-token and TLS configuration for broader exposure.

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
  Alerts --> Msg[Full-screen message]
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
- **SSH via system OpenSSH** so LaunchAgents on macOS can reach LAN hosts without a Go TCP dial (which hits Local Network TCC restrictions).
- **gRPC unary interceptor** applies a default timeout when the client omits a deadline, logs request IDs, and recovers panics.
- **Protobuf is generated** into `gen/`; edit `api/proto` and regenerate—do not hand-edit stubs.

## Tech stack

| Layer | Technology |
| --- | --- |
| Language | [Go](https://go.dev/) 1.26 (module `github.com/aneeshpatne/atlas`) |
| API | [gRPC](https://grpc.io/) + [Protocol Buffers](https://protobuf.dev/) (`google.golang.org/grpc`, `protobuf`) |
| Persistence | [Redis](https://redis.io/) via [go-redis/v9](https://github.com/redis/go-redis) |
| Scheduling | [robfig/cron/v3](https://github.com/robfig/cron) (timezone-aware) |
| Device transport | System OpenSSH (`/usr/bin/ssh` preferred); [golang.org/x/crypto](https://pkg.go.dev/golang.org/x/crypto) present as a module dependency |
| Imaging | [golang.org/x/image](https://pkg.go.dev/golang.org/x/image) (JPEG/GIF/WebP decode, story background prep) |
| On-device UI | [fbink](https://github.com/NiLuJe/FBInk) over SSH; Instrument Serif / Helvetica fonts on the Kindle |
| Logging | `log/slog` JSON handler in the service binary |
| Testing | Go standard library `testing` |

## Project structure

```text
.
├── app.go                      # CLI: clock | story | news
├── cmd/news-screen/            # Long-running service entrypoint
│   ├── main.go
│   └── README.md               # Service-specific flags and lifecycle notes
├── api/proto/screen/v1/        # Protobuf sources (news, alerts, status)
├── gen/screen/v1/              # Generated pb / gRPC stubs
├── assets/genres/              # Genre backdrop images (india, mumbai, world, misc)
├── internal/
│   ├── config/                 # Defaults, validation, timeouts
│   ├── supervisor/             # Single-owner lifecycle state machine
│   ├── scheduler/              # Daily window + news-pass cron specs
│   ├── grpcserver/             # RPC handlers + unary interceptor
│   ├── news/                   # Redis store and Story types
│   ├── screennews/             # Protobuf-to-domain Redis ingestion adapter
│   ├── dashboard/              # Genre cycles and story draining
│   ├── newsworkflow/           # Clock-default loop + pass signals
│   ├── newsworker/             # Supervisor-facing worker interface
│   ├── kindle/                 # Device commands, clock, story, genre paint
│   ├── kindledisplay/          # display.Controller + alert presenter
│   ├── display/                # Controller interface
│   ├── alert/                  # Alert model and timed presentation
│   ├── sshclient/              # OpenSSH wrapper
│   └── redis/                  # Thin Redis client
└── story.json                  # Sample story payload for CLI story mode
```

## Requirements

- **Go** 1.26+ (module declares `go 1.26.4`)
- **Redis** reachable at the address you pass (default `localhost:6379`)
- **Jailbroken Kindle** with:
  - SSH as `root` (default address `192.168.0.10:22`)
  - Private key at `~/.ssh/id_ed25519` by default (configurable with `-ssh-key`)
  - A matching entry in `~/.ssh/known_hosts` (or explicit development-only `-ssh-insecure-host-key`)
  - [fbink](https://github.com/NiLuJe/FBInk) at `/mnt/us/usbnet/bin/fbink`
  - Fonts used by rendering (e.g. `/mnt/us/fonts/InstrumentSerif-Regular.ttf`, optional Helvetica under `/usr/java/lib/fonts/`)
- **Network access** from the host to the Kindle (LAN); for story backgrounds, outbound HTTPS to fetch public Open Graph images
- **Genre assets directory** (default `assets/genres`) present when running news-screen

**Not required for unit tests:** a physical Kindle or Redis (tests use fakes and pure logic). **Required for real display:** reachable Kindle + key; **for news mode / service:** Redis as well. The service is intended to run on a host machine that keeps SSH open to the device, not on the Kindle itself.

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
     -redis localhost:6379 \
     -timezone Asia/Kolkata \
     -grpc 127.0.0.1:50050 \
     -news-refresh 15m \
     -news-per-genre 10 \
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
> gRPC binds to `127.0.0.1:50050` by default. A non-loopback bind requires TLS plus either `-grpc-token-file` or mutual TLS (`-grpc-client-ca`). SSH host verification is strict by default; the user, key, and known-hosts file are configurable.

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

**What the suite covers:** config and scheduler behavior; supervisor FIFO, cancellation, backoff, and shutdown; gRPC validation, authentication primitives, status, and error mapping; newsworkflow signaling; backdrop/story rendering; SSH quoting and cancellation; and opt-in Redis integration behavior. Set `ATLAS_TEST_REDIS_ADDR` to run the Redis integration test locally.

GitHub Actions runs formatting, vet, unit and Redis integration tests, the race detector, coverage, vulnerability scanning, and generated-protobuf drift checks.

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
