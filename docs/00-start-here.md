# Runnel: start here

**The project is called Runnel.** The binary is `runnel`, its environment variables start with `RUNNEL_`, and the service unit and data directory use the same name (`08-decisions.md`, D20).

A lean, fast media server for a home library, built together with its own official clients. One Go binary, one SQLite file, ffmpeg only as a child process. The clients are a web interface, a TV app, phone and tablet apps, and desktop apps (`10-clients.md`).

Read this file first. Then read the document for the part being worked on.

## 1. Where things stand (20 November 2026)

- **Planning is finished. Stage 1 is being built:** a server that serves files (`01-roadmap.md`, section 3).
- **Decided:** the server's design (D1 to D19), the name and the logo (D20, D21), the order of the stages (D22, D25), official clients and their designs (D23 to D26, D29 to D37), the licence (D38), how a browser is fed (D39, D40), and what each client is built with (D41 to D44).
- **Proposed, to confirm in use:** encoder arguments, limits, thresholds, discovery on the local network (P1 to P8).
- **Open:** O5, O6, O8, O12 and O13 in `08-decisions.md`. None of them blocks the server.
- **Next:** stage 2, a library a client can use, once stage 1 has met its exit criteria on real hardware.

The design documents (`design/`) and the design review (`11-design-review.md`) that other documents refer to are added in the repository's next pull request, together with the brand files.

## 2. Goals, as numbers

| | Target |
|---|---|
| Server memory, idle, real library loaded | under 30 MB |
| Play pressed to first frame, file the device can decode | 0.5 s |
| Play pressed to first frame, file that must be transcoded | 1.5 s cold, 0.5 s after a warm-up |
| Audio switch | 0.1 s on direct play, 1.5 s during a transcode, 0.15 s with the sidecar |
| Rescan of an unchanged library | under 2 s |
| New file playable | within 15 s, without a manual scan |

Every target is checked on real hardware, from the server's `play_log` and its stats, before a stage is called done.

**What it will not make faster:** frames encoded per second (same ffmpeg, same GPU), what a device can decode, and how fast metadata providers answer.

## 3. Real hardware

The design is sized for small home servers: a low-power processor, little memory, disks that may sleep, and clients as weak as a TV stick.

Where a document says "real hardware", it means the machines a stage is being proven on. Descriptions of any particular setup, and measurements taken on it, are kept outside this repository (D28). The numbers in these documents come from a development sandbox with software codecs, and say so. They are not numbers for anyone's server.

## 4. The documents

| File | What it is | Read it when |
|---|---|---|
| `00-start-here.md` | This page: status, goals, rules, glossary. | Always, first. |
| `01-roadmap.md` | The stages, their order and their exit criteria. | Planning what to do next. |
| `02-architecture.md` | Process model, package layout, dependencies, configuration, deployment, testing. | Writing any server code. |
| `03-data-model.md` | The SQLite schema, its rules, and the queries behind the API. | Touching the database. |
| `04-api.md` | Every endpoint, with examples. | Writing a handler or a client call. |
| `05-playback.md` | Direct play, the piped transcode, warm-up, audio switching, browsers, ffmpeg commands, player settings. | Anything between Play and the first frame. |
| `06-library-and-metadata.md` | Scanning, probing, name parsing, items, matching, artwork, the Jellyfin import. | Anything between a file on disk and an item on screen. |
| `08-decisions.md` | What was decided, when, and why. What is still open. | Before reopening a question. |
| `10-clients.md` | The official clients, their order, and the brief for designing them. | Client work, and all design work. |

Numbers 07, 09 and 11 are not here: 07 is a private reference, and 09 (the brand) and 11 (the design review) arrive with the design documents.

Which one wins when they disagree: `08-decisions.md`, then the other documents. If code and a document disagree, one of them is wrong, and the same pull request fixes it.

## 5. The design in ten lines

1. The server states facts about files. The client decides how to play them.
2. A file the device can decode is sent as it is, by byte range, copied by the kernel.
3. A file it cannot decode is re-encoded by an ffmpeg child and piped out. Video only is re-encoded.
4. That transcode can start when an item gets focus, before Play is pressed.
5. Audio and subtitle switching is the player's job. During a transcode it is a restart, or a switch inside a small seekable sidecar file.
6. Files are scanned by walking and comparing, probed once, and playable before any metadata exists.
7. Names are parsed with the folders as anchors. Uncertain results go to a review list.
8. Metadata can be imported from Jellyfin once, and otherwise comes from TMDB.
9. Clients keep a local copy of the library and ask what changed since a sequence number.
10. Nothing in the background competes with the first seconds of a playback.

A browser is the one client that cannot take a file as it is. It is fed two MP4 streams made on request, and draws subtitles itself (`05-playback.md`, section 13).

## 6. The decoding test

**Can a device decode a library's video in software?** A player inside an app can decode in software what the device's chip cannot. Whether that is smooth decides what a client does by default on that device: play the original file, or ask for a transcode.

- The test: play one file of the format for ten minutes with hardware decoding off, and read the dropped-frame count.
- Until a device has passed it, a TV asks for a transcode of a format it decodes only in software, and has a switch to try otherwise. Phones, tablets and computers start with the switch on (D37).

## 7. Working rules

For anyone working on this, person or Claude session. `CLAUDE.md` at the top of the repository carries the same rules in short.

**Evidence**

- Say where a number comes from. "Measured" names the kind of machine. A number from a 2-vCPU sandbox with software codecs is not a number for a real server.
- Mark what is untested as untested. Encoder arguments, memory figures and timings on real hardware are claims until someone has run them there.
- Do not state from memory how ffmpeg, mpv, a browser, Android, iOS or a provider behaves when it can be checked. Check it, or mark it as from memory.
- When a test shows a document is wrong, change the document, and say what was wrong.

**Scope**

- Nothing is built before its stage.
- The list of things not being built is in `01-roadmap.md`. Adding one needs a decision first.
- A third-party dependency in the server needs a decision first. So does a runtime dependency of the web interface.
- After a change to the server, look at its memory. The budget is in `02-architecture.md`, section 8.

**The repository** (D27)

- Work on a branch. Open a pull request. Merge with Squash and merge. Never push to `main`.
- No link to a Claude chat, session or project in a commit, a pull request title or description, an issue or a comment. No `Claude-Session:` trailer. Read a new pull request's description back and remove any such link.
- Write about the work, not about who asked for it. Commits, pull requests, issues and documents state the problem, the decision or the change.
- Source files start with `// SPDX-License-Identifier: GPL-3.0-or-later` (D38), in the file's own comment style.
- A commit made with Claude's help may end with a `Co-Authored-By: Claude …` trailer. That is the only mention of Claude it carries.

**Never in the repository** (D28)

- Anything about a particular setup: hardware models, what is in a library, how its files are named, addresses, device names, or any measurement taken there.
- The research notes, and the scripts and test rigs written during the research. The repository's tests are written for it.
- Real file names, release names, tokens, API keys. Test data is synthetic.

**These documents** are public editions. They speak of requirements, design and targets. Where one needs a measurement, it uses one from a development sandbox and says so.

## 8. Glossary

| Term | Meaning |
|---|---|
| Direct play | The player gets the original file and decodes it. |
| Piped transcode | ffmpeg re-encodes the video and its output is relayed to the player as one continuous stream. |
| Browser delivery | Two MP4 streams made on request, video and audio, which a page plays through Media Source Extensions, with subtitles and fonts fetched separately and drawn by the page (D39, D40). |
| Remux | Copying a file's video or audio into another container without re-encoding it. |
| Sidecar | A small file holding everything in the original except its video. Lets the player switch tracks locally during a transcode. |
| Warm-up | Starting a transcode, or reading a file's header, when an item gets focus. `prepare` in the API. |
| Ticket | What `prepare` returns. The transcode request that carries it gets the warmed-up stream. |
| File | Something on disk. Has streams and chapters. |
| Item | Something browsed: a movie, a series, a season, an episode, or a collection of films. Has files, metadata and watch state. |
| Card | The short form of an item. Lists are made of cards, and so is a client's local copy. `04-api.md`, section 2.1. |
| My list | A person's own list of films and series to watch. |
| Setup code | Printed in the server's log while no person exists. Making the first person needs it. |
| Root | A folder that a library scans. |
| Sequence | A counter that rises with every visible change. Clients sync by it. |
| Tombstone | A record that an item was deleted, kept so clients can drop it. |
| Fingerprint | A file's size plus a hash of its first and last 64 KiB. Follows a file through a rename. |
| Confidence class | How an episode number was found in a name: explicit, anchored, dash or guess; none when there is none. A film's name has a class too. |
| Playback gate | The rule that background work pauses while a playback starts. |
| Official client | An app or page that is part of Runnel. `10-clients.md`. |
| Real hardware | The machines a stage is proven on. Section 3. |
