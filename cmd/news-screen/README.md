# News screen service

This command runs the timezone-aware scheduler, the single-owner lifecycle
supervisor, and the news/alert/status gRPC API. It uses the existing Redis news queues
and Kindle SSH controller.

```bash
go run ./cmd/news-screen \
  -kindle-address 192.168.0.10:22 \
  -redis localhost:6379 \
  -timezone Asia/Kolkata \
  -grpc 127.0.0.1:50050 \
  -news-refresh 15m \
  -news-per-genre 10
```

## Display lifecycle

**Clock is the default screen.** While the service is inside the active window
(default **07:00–23:00** / 11pm in the configured timezone), the Kindle shows a
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

## Backgrounds

- **Genre screens** always load a full-screen image from `-assets` (default
  `assets/genres`). Match is case-insensitive (`India` → `india.png`); unknown
  genres fall back to `misc.png`.
- **Story screens** prefer the source **ogurl** image as the full-screen
  background. If ogurl is missing or the download fails, the same genre asset
  is used as a fallback so stories still have a photo background.

The service defaults to `~/.ssh/id_ed25519` and strict host verification through
`~/.ssh/known_hosts`; both paths and the SSH user are configurable. gRPC binds to
loopback by default. Non-loopback binds require TLS plus either a bearer token
or client-certificate verification through `-grpc-client-ca`.

Story image downloads require public HTTPS destinations and re-check every
redirect. `-image-allow-private` relaxes the address restriction for trusted
private image servers and should not be enabled for untrusted ingestion.

News is persisted in the existing Redis store. The bounded alert FIFO is
intentionally in memory, so accepted alerts that have not finished are lost if
the process crashes. Scheduled state is reconstructed from the configured clock
on every boot.

Regenerate protobuf code (do not edit generated files):

```bash
PATH="$(go env GOPATH)/bin:$PATH" go generate ./api/proto
```
