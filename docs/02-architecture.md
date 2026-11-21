# Architecture

How the server is put together, what it may depend on, how it is configured and run, and how it is tested. The data model is in `03-data-model.md`, the API in `04-api.md`, playback in `05-playback.md`, the library pipeline in `06-library-and-metadata.md`.

**Revised, 20 November 2026,** for the official clients (D23), the design review (D34, D35) and the research that followed it (D38 to D45, P6): the clients in section 1, the web interface and discovery as parts of the server, the two settings kept in the database, the setup code, and what a browser playback costs. The research rigs are not part of the repository (D28).

## 1. Shape

```
Apps: TV, phone, tablet, desktop (mpv inside) ──┐
A browser (the web interface) ──────────────────┼─ HTTP ─► runnel, one process
                                                │           ├─ API: JSON, bearer tokens
                                                │           ├─ the web interface: static files inside the binary
                                                │           ├─ files: byte ranges, copied by the kernel
                                                │           ├─ transcode and remux: ffmpeg children, output relayed
                                                │           ├─ scanner and prober (ffprobe children)
                                                │           ├─ metadata workers; image, sidecar and subtitle caches on disk
                                                │           ├─ a responder that lets apps find the server (proposed, P6)
                                                │           └─ SQLite, one file
```

The clients are Runnel's own (`10-clients.md`). The apps play with mpv and can take any file as it is. A browser is fed MP4 made on request (`05-playback.md`, section 13).

## 2. Principles

1. **Thin server, capable client.** The server states facts about files. The client chooses how to play them. No device profiles on the server.
2. **Expensive work happens at scan time or in the background**, never between Play and the first frame.
3. **One process, one database file, one data directory.** ffmpeg and ffprobe run only as children and only when needed.
4. **Memory is bounded by construction.** Small SQLite caches, nothing library-sized held in memory, provider responses parsed as streams, no image decoding in the server process, a cap on every child.
5. **Read-only towards the media.** The server never writes into a library folder. Jellyfin, or anything else, can run beside it on the same files.
6. **Playback first.** Background work pauses while a playback starts.

## 3. Language and dependencies

Go, 1.24 or newer. Decided on 20 November 2026; the measurements behind it are in the research notes, section 5.2.

A third-party module needs a reason written into `08-decisions.md`. The standard library covers the rest:

| Need | From |
|---|---|
| HTTP server and routing, with method and path patterns | `net/http` |
| JSON | `encoding/json` |
| Logging | `log/slog` |
| Password hashing | `crypto/pbkdf2` with SHA-256 |
| Tokens and ids | `crypto/rand` |
| Compression | `compress/gzip` |
| Migrations and static files in the binary | `embed` |
| Child processes | `os/exec` |
| SQLite | `github.com/mattn/go-sqlite3`, built with the `sqlite_fts5` tag. It needs cgo. This is the driver the prototypes were measured with. |
| File watching | `golang.org/x/sys/unix` for inotify |
| Being found on the local network | A small multicast DNS responder of the server's own, on `net`. Proposed and not prototyped (P6). A module would need a decision. |
| The web interface | Not a Go dependency. It is built with Node into static files (Svelte, TypeScript, Vite: D41) and embedded with `embed`. Node is needed to build, never to run. |

The pure-Go SQLite driver would remove cgo. It was not measured, because it could not be downloaded in the test sandbox. The driver is used through `database/sql` only, so trying it later is a small change.

## 4. Layout

```
cmd/runnel/            main: subcommands, wiring, signals
internal/config/       flags and environment
internal/db/           open, pragmas, migrations, the write lock, the sequence
internal/db/migrations/  0001_init.sql, …
internal/model/        plain structs shared between packages
internal/scan/         walking, comparing, watching, fingerprints
internal/probe/        running ffprobe and mapping its output
internal/parse/        names: films, episodes, versions, parts, extras, external tracks
internal/library/      building items from files
internal/meta/         matching, providers, the provider cache, artwork, the Jellyfin import
internal/play/         sessions, the ffmpeg argument builder, relaying, warm-up, sidecars
internal/auth/         users, tokens, pairing
internal/api/          handlers, middleware, JSON shapes, ETags
internal/tasks/        the background queue and its workers
internal/discover/     the responder that announces the server on the local network (stage 6)
internal/web/          the built web interface, embedded, and the handler that serves it (stage 5)
web/                   the web interface's source (stage 5)
docs/                  these documents, in their public editions
```

Rules for the code:

- SQL lives in `internal/db` and nowhere else. Other packages call functions, not queries.
- `internal/play` builds ffmpeg arguments as arrays. Nothing ever builds a shell string.
- Packages do not hold global state. `main` builds one of each and passes them down.
- A `context.Context` is the first parameter of anything that waits.
- Errors are wrapped with `%w` and logged once, where they are handled.
- Times are Unix milliseconds everywhere, including in memory.
- Every source file starts with `// SPDX-License-Identifier: GPL-3.0-or-later` (D38), in the file's own comment style.
- Where the apps live, in this repository or in their own, is decided when stage 6 starts. The server and the web interface are here.

## 5. Processes and concurrency

| What | How many | Notes |
|---|---|---|
| HTTP handlers | One goroutine per request | The standard server. |
| Database writer | One connection, one user at a time | Every write is `BEGIN IMMEDIATE`. Writers queue in the process, not in SQLite. |
| Database readers | Up to 4 connections | WAL lets them read during a write. |
| Scanner | One | One root at a time. |
| Task workers | A few, by kind | See `06-library-and-metadata.md`, section 9. |
| ffprobe children | Up to 2 | Short-lived, about 54 MB each. |
| ffmpeg children | One per stream, plus one warm-up | See `05-playback.md`, section 4.4. A browser playback has two streams, video and audio. A stream that only copies takes no transcode slot. |
| Discovery responder | One goroutine | Answers multicast DNS for one service. Stage 6. |

**The playback gate.** A counter of sessions that are starting. While it is above zero, task workers finish what they hold and take nothing new. A session counts as starting from its request, or its `prepare`, until 10 seconds after its first byte.

**Shutdown.** On SIGTERM: stop accepting requests, kill every child, let the writer finish its transaction, close the database. Within 5 seconds.

## 6. Configuration

Flags and environment variables only. There is no configuration file: libraries, users and everything else that changes live in the database and are managed by subcommands or the admin API.

Two settings can also be changed while the server runs, by an admin in the web interface, and are then kept in the database: the server's name and the metadata key (D34). Where the environment sets one of them, the environment wins and the web interface shows it as fixed.

| Variable | Default | Meaning |
|---|---|---|
| `RUNNEL_LISTEN` | `:8610` | Address to listen on. |
| `RUNNEL_DATA` | `/var/lib/runnel` | Data directory. |
| `RUNNEL_FFMPEG`, `RUNNEL_FFPROBE` | found on `PATH` | Paths to the binaries. Jellyfin's ffmpeg build is a good choice where it is installed, since it is known to work with the GPU there. |
| `RUNNEL_HWACCEL` | `auto` | `auto`, `vaapi`, `qsv` or `none`. |
| `RUNNEL_VAAPI_DEVICE` | `/dev/dri/renderD128` | The render device. |
| `RUNNEL_TRANSCODE_SLOTS` | `1` | Transcodes at once. |
| `RUNNEL_FFMPEG_MAX_RSS_MB` | `1024` | A child above this is killed. |
| `RUNNEL_SIDECAR_CACHE_MB` | `4096` | Size cap of the sidecar cache. |
| `RUNNEL_TMDB_KEY` | none | The metadata key. It can be set in the web interface instead. Without a key nothing is fetched and items keep the titles from their names. |
| `RUNNEL_NAME` | none | The server's name, shown by clients. It can be set in the web interface instead. Without one it is the machine's host name. |
| `RUNNEL_DISCOVERY` | `on` | `off` stops the server from announcing itself on the local network. |
| `RUNNEL_LOG` | `info` | `debug`, `info`, `warn` or `error`. |

Subcommands of the one binary:

| Command | Does |
|---|---|
| `runnel serve` | Runs the server. |
| `runnel user add <name> [--admin]`, `user passwd <name>`, `user list` | Users. The first user is made here, or in the web interface's first run with the setup code. |
| `runnel setup-code` | Prints the setup code, while no user exists (D35). The server also prints it in its log when it starts without users. |
| `runnel library add --name <n> --kind movies\|shows [--profile anime] --root <path>…`, `library list`, `library remove <id>` | Libraries. |
| `runnel scan [--library <id>]` | Asks the running server to scan. |
| `runnel pair approve <code> --user <name>` | Approves a device that is showing a pairing code. |
| `runnel doctor` | Checks ffmpeg, ffprobe, the render device and what can be encoded, and prints it. |
| `runnel import jellyfin …` | The one-time import. |
| `runnel version` | |

Subcommands that change data talk to the running server over its API on localhost, with a token kept in the data directory. They never open the database behind the server's back.

## 7. The data directory

```
runnel.db, runnel.db-wal, runnel.db-shm   the database
admin.token                               for the subcommands, readable by the service user only
images/ab/<key>_<width>.jpg               artwork
sidecar/<file id>-<fingerprint>.mkv       audio-and-subtitle files, a capped cache
subs/<file id>-<fingerprint>/             text subtitle tracks and attached fonts, extracted for browsers; in the same cap
import/jellyfin-report.json               the report of the last import or dry run
backup/                                   database copies made before migrations
```

Everything except the database can be deleted and will be rebuilt. `images/` also holds people's photos, which arrive only when a client first asks for one (D33).

The server's instance id, its settings and the setup code are rows in the database (`03-data-model.md`, section 4), not files here.

## 8. Memory budget

| Component | Budget | Basis |
|---|---|---|
| Server idle | 10–15 MB | Prototype: 11–14 MB |
| Server under API load | 20–35 MB | Prototype: 22–36 MB after 8,000 requests |
| A direct stream | about 0 extra | Prototype |
| A scan | 13–15 MB peak, inside the above | Prototype |
| A probe | 54 MB per ffprobe child, for a moment | Measured |
| A transcode | The ffmpeg child: 220–500 MB in software at 1080p. Hardware not measured. | Measured |
| A held warm-up | A transcode's memory, no CPU | Measured |
| A browser playback with the video copied | Two ffmpeg children, 56 to 58 MB each at their peak | Measured in the sandbox, 20 November 2026 |
| The web interface | Its files are part of the binary. To be measured at stage 5. | Not measured |

Set `GOMEMLIMIT=64MiB` in the service. In the prototype a 16 MiB limit with `GOGC=50` held the server at 21.8 MB after load.

Goal for stage 2: under 30 MB resident with a real library loaded and a client browsing.

## 9. HTTP

- Read header timeout 5 s. Idle timeout 2 minutes.
- JSON routes: 15 s to respond.
- `/files/…/raw`, `/files/…/sidecar.mkv`, `/files/…/transcode.mkv`, `/files/…/video.mp4`, `/files/…/audio.mp4`: no write deadline. They last as long as the player keeps them.
- The API is under `/api/v1`. The web interface is served from `/`, as files compressed at build time, with names that change when their content does.
- Middleware, in order: recover, request id, log (method, path, status, time; never the query string), authenticate, gzip for JSON.
- No CORS headers unless an origin is configured.

## 10. Security

- Meant for a home network. Do not expose it to the internet without a reverse proxy that adds TLS. Away from home, a client uses whatever address the admin has made reachable (D32).
- A new server has no user. Making the first one needs the setup code from the server's log (D35), so that whoever opens the address first does not become its admin.
- Over plain HTTP a browser gives a page no secure context. The web interface is built to need none (D45).
- Wrong tries are limited: sign-ins, setup codes, pairing codes (P7).
- Tokens are 32 random bytes. Only their SHA-256 is stored. They are per device and can be revoked.
- Passwords: PBKDF2 with SHA-256, a random salt and an iteration count stored with the hash.
- URLs carry ids, never paths. A path from the database is joined to its root and checked to be inside it before any open.
- The service runs as its own user, with the media mounted read-only and membership of the group that owns the render device.

## 11. Running it

A systemd unit, as a starting point:

```ini
[Unit]
Description=Runnel
After=network-online.target
Wants=network-online.target

[Service]
User=runnel
Group=runnel
SupplementaryGroups=render video
Environment=RUNNEL_DATA=/var/lib/runnel
Environment=GOMEMLIMIT=64MiB
ExecStart=/usr/local/bin/runnel serve
Restart=on-failure
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/runnel
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

- The media paths must be readable by the `runnel` user. Nothing needs to be writable outside the data directory.
- In a container: mount the media read-only, mount the data directory, pass `/dev/dri/renderD128` through, and add the render group. Apps find the server by themselves only when the container uses the host's network. Otherwise its address is typed.
- It can run beside Jellyfin on the same files for as long as wanted. It listens on its own port and writes nothing they share.

## 12. Building and releasing

- `CGO_ENABLED=1 go build -tags sqlite_fts5 -trimpath -ldflags "-s -w -X main.version=<tag>" ./cmd/runnel`
- The prototype's stripped binary was 7.4 MB. A clean build took 66 s including one compile of SQLite's C source, and a rebuild after a one-line change 0.6 s.
- From stage 5 the web interface is built first: `npm ci && npm run build` in `web/`, whose output `internal/web` embeds. A build then needs Node as well as Go and a C compiler.
- Releases are tags. CI builds `linux/amd64` and attaches the binary.

## 13. Testing

| Layer | What | How |
|---|---|---|
| Unit | Name parsing | A table of names and expected results in `internal/parse/testdata`. Every real name that parses wrong is added to it, anonymised. |
| Unit | ffmpeg arguments | Golden tests: a request in, an argument array out. |
| Unit | Progress rules, match scoring, cursors | Table tests. |
| Database | Migrations and every query | Against a temporary database with sample rows. The queries in `03-data-model.md` are the starting set. |
| API | Every endpoint | `net/http/httptest` against a temporary database and a temporary library. |
| Integration | Scan, probe, range requests, a piped transcode's first packet | Tiny media files generated with ffmpeg's test sources in CI. Software codecs only. |
| Hardware | Encoders, timings, memory | By hand on real hardware, with the checklist in `05-playback.md`, section 12. CI has no GPU. |
| Web | The web interface | Unit tests for its logic. From stage 7, the player in a headless browser against a test server. |

CI runs `go vet`, the tests and a build on every pull request. It installs ffmpeg for the integration tests.

The rigs that produced the numbers in the research notes are not in the repository (D28). The repository's tests are written for it.
