# Runnel

A lean, fast media server for a home library, built together with its own clients.

- **One Go binary, one SQLite file.** ffmpeg runs only as a child process, and only when it is needed.
- **The server states facts about files. The client decides how to play them.** A file a device can decode is sent as it is. One it cannot decode is re-encoded and piped, without waiting for segments.
- **Clients keep a local copy of the library**, so screens draw at once and ask the server only for what changed.
- **Its own clients**: a web interface, an Android TV app, phone and tablet apps, and desktop apps.

## Status

Stages 1 and 2 of the roadmap are built: a server that indexes a library, probes its files and serves them by byte range, with everything a client needs for browsing, watch state and direct play, and a one-time import of the watch history in Jellyfin. Their checks pass in a development sandbox and are still to be run on real hardware. **There is no client yet.** Builds for Linux on amd64 are attached to the [releases](https://github.com/gureetk/runnel/releases); a pre-release is a build for testing.

The design is in [`docs/`](docs/00-start-here.md). The [roadmap](docs/01-roadmap.md) lists the stages in which it gets built, and what each stage must prove before it is done.

## Goals

| | Target |
|---|---|
| Server memory, idle, with a real library loaded | under 30 MB |
| Play pressed to first frame, for a file the device can decode | 0.5 s |
| Play pressed to first frame, for a file that must be re-encoded | 1.5 s cold, 0.5 s after a warm-up |
| Audio switch | 0.1 s on direct play, 1.5 s during a re-encode |
| Rescan of an unchanged library | under 2 s |
| A new file playable | within 15 s, without a manual scan |

These are targets, not measurements. Each is checked on real hardware before its stage is called done.

## What it is not

No live TV, DLNA, plugins, music, or compatibility with another server's API. Keeping these out is what keeps the project small. The roadmap has the list.

## Documents

| | |
|---|---|
| [Start here](docs/00-start-here.md) | Status, goals, working rules, glossary |
| [Roadmap](docs/01-roadmap.md) | The stages and their exit criteria |
| [Architecture](docs/02-architecture.md) | Process model, layout, dependencies, configuration |
| [Data model](docs/03-data-model.md) | The SQLite schema and the queries behind the API |
| [API](docs/04-api.md) | Every endpoint, with examples |
| [Playback](docs/05-playback.md) | Direct play, the piped transcode, browsers |
| [Library and metadata](docs/06-library-and-metadata.md) | Scanning, name parsing, matching, artwork |
| [Decisions](docs/08-decisions.md) | What was decided, when and why, and what is open |
| [Clients](docs/10-clients.md) | The official clients and their design brief |

## Contributing

Work happens on a branch and reaches `main` by pull request. The rules are in [`CLAUDE.md`](CLAUDE.md) and in section 7 of [Start here](docs/00-start-here.md).

## Licence

GPL-3.0-or-later. See [`LICENSE`](LICENSE).
