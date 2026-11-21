# Roadmap

The stages in which the server and its clients get built, what each one must prove before it is done, and why they are in this order. Durations are rough guesses for evenings and weekends, and exist only for the server stages. The client stages cannot be estimated before they are designed and before it is decided what they are built with.

Nothing here is to be started until it is asked for.

**Revised, 20 November 2026,** after the design review (`11-design-review.md`, D29 to D37): the designs exist, their asks are in `04-api.md` and `03-data-model.md`, and the stages below say where each new piece lands. Revised again the same day after the research that closed the planning: the licence (D38), how a browser is fed (D39, D40), what the clients are built with (D41 to D44), and discovery (P6). Revised on 21 November 2026: installing and updating join stage 5 (D46, D47). Revised again the same day: a stage may start once the stage before it is built, and is done only when its checks have run on real hardware (D48, section 15).

## 1. Order

```
Design    done, from 10-clients.md ─────────────────────► needed before stage 5

Server    0 prepare ─► 1 files ─► 2 library ─► 3 transcoding ─► 4 metadata

Clients                             5 web interface ─► 6 TV app ─► 7 web player ─► 8 phones and tablets ─► 9 desktop apps

Later     10 polish
```

What depends on what:

- Stage 5 needs stage 2 and the web design.
- Stage 6 needs stage 3, the TV design, and artwork from stage 4 to look right.
- Stage 7 needs stage 3. How a browser is fed is designed (D39, D40).
- Stage 8 starts with a spike (D43) and, for iOS, with O13. Stage 9 starts with the choice of a shell (O12).
- The designs for every client exist and were reviewed (D29). No stage waits for design any more, except the iOS drawings before the iOS half of stage 8.

Why this order:

- **Transcoding is stage 3 in every case** (D22). Browsers, older phones and any player without the right decoder need it, whatever one TV can do.
- **The web interface comes first among the clients** (D25). Setup and administration are needed by everything, and it is the smallest client.
- **The TV app is second.** It is where most watching happens.
- **The web player is third.** Once it exists, every device with a browser can watch. The apps after it make that better. They are no longer the only way in.

Seven client surfaces are more work than the server. Each stage is meant to leave something usable behind.

## 2. Stage 0: decide and prepare

| Task | State |
|---|---|
| Research, measurements and server design documents | Done |
| Language: Go. API: own, not Jellyfin-compatible | Decided |
| Name: Runnel. Logo and brand files (`09-brand.md`) | Decided (D20, D21) |
| The repository, with its rules (D27, D28) | Created |
| Order of the stages and of the clients | Decided (D22, D25) |
| Check the name for collisions: GitHub, package registries, domains | To do |
| Licence | Decided: GPL-3.0-or-later (D38) |
| Design the clients, from `10-clients.md` | Done, reviewed and accepted (D29, `11-design-review.md`). iOS has its manners in words and no drawings of its own (`design/mobile.md`, section 12). |
| Fold what the designs ask of the server into `04-api.md` and `03-data-model.md` | Done, 20 November 2026 |
| Decide what the native clients and the web interface are built with | Decided (D41 to D44). Each choice has a spike at the start of its stage. The desktop shell is open (O12). |
| Design browser delivery and add it to `04-api.md` and `05-playback.md` | Done (D39, D40). Tested in Chrome only. |
| Research discovery on the local network | Done on paper (P6). The responder is prototyped at the start of stage 6. |
| Revise the remaining documents for the official clients and the design review | Done |
| First pull request: public editions of the server documents, a README, the licence, a `CLAUDE.md`, and a CI workflow that builds an empty module | Done with the pull request that added this file |
| Second pull request: the design documents, the design review and the brand files | To do, before stage 5 |
| Tests on real hardware: software decoding on a TV (`00-start-here.md`, section 6), and the memory of one hardware transcode (`05-playback.md`, section 12, item 1) | To do. The results are kept outside the repository (D28). |

**Exit:** the licence is chosen, the designs for the web interface and the TV app exist, and the public editions of the documents are merged.

State on 20 November 2026: everything but the second pull request and the tests on real hardware. Neither blocks stage 1.

## 3. Stage 1: a server that serves files

The smallest thing that is useful and that proves the memory and scan goals on real hardware. About 2 to 3 weekends.

Build:

- The module, layout and CI from `02-architecture.md`.
- Configuration, logging, the data directory.
- Database open, pragmas, migration `0001`, the write lock, the sequence.
- `runnel user add`, password sign-in, tokens, the authentication middleware.
- `runnel library add`, roots.
- The scanner without watching: walk, compare, fingerprint, moves, missing, root availability.
- The prober and its mapping. Until the queue of stage 2, the files table is the queue: a file waits while its `probe_version` is behind, and the scanner probes two at a time after each scan and at start.
- The parser for films and episodes, and item building with titles taken from names: films, series and episodes, with every file of an item attached as part 0 without a label. Seasons, versions, parts, files that hold several episodes and absolute numbering are the full item model of stage 2.
- `GET /server`, `POST /auth/login`, `GET /libraries`, `GET /items`, `GET /items/{id}`, `GET /files/{id}/raw`. As far as stage 1 has the data: cards carry the id, type, library and folder, title, year, an episode's series and numbers, `added_at` and `seq`, and the detail adds the files with their streams and chapters. `GET /items` takes `library` and `type`, or `series`, sorted by title or in episode order, with `limit` and `cursor`. Any other parameter is refused with `400`, so that a client knows. The rest of `04-api.md`, sections 2.1, 2.2 and 4.3, arrives with stage 2.
- A report of how every file parsed, by class: `runnel report`, which reads `GET /admin/parse-report` (`04-api.md`, section 8.2). It also lists the files ffprobe could not read.
- The systemd unit: `deploy/runnel.service`.

**Exit, on real hardware with a real library:**

1. A first index and probe of the whole library completes without a failed file that Jellyfin can play.
2. A rescan with nothing changed takes under 2 s with a warm cache.
3. The server's resident memory is under 30 MB after the scan, idle.
4. A desktop plays a file with `mpv "http://<server>:8610/api/v1/files/<id>/raw?t=<token>"`, and seeking works.
5. A renamed file keeps its id. Unmounting a root marks nothing missing.
6. The parse report has no file in the `none` class, and every file in the `guess` class has been looked at.

State on 21 November 2026: built (D48). Everything on the list is merged, and the six criteria pass in a development sandbox on generated files. None has been run on real hardware yet. That check is an open issue of the stage's milestone, and the milestone stays open until it is met.

## 4. Stage 2: a library a client can use

Everything a client needs for browsing and direct play, and the existing watch history brought over. About 3 to 4 weekends.

Do first, because stage 1 is built and not done (D48): harden the parser, which has met no real name yet. Read the shapes that a review of stage 1 found with invented names: underscores between words, a season's number after the title, decimals, the forms of a file that holds several episodes, folders that are not season folders, `E05` and `EP05` (`06-library-and-metadata.md`, section 4). Give the parse report a note on every file of an item that several files make, where their names do not tell versions or parts apart, so that a wrong number in a trusted class shows. Freeze migration `0001`, since a database made with it may exist (`03-data-model.md`, section 7). Add a test that builds the binary and runs its subcommands.

Build:

- The full item model: seasons, versions, parts, specials, files that hold several episodes, the absolute-numbering flag.
- Local artwork, scaled by ffmpeg, and `GET /img`.
- `GET /home`, `GET /search`, `GET /genres` for one library or all, the rest of `GET /items`, letters and pages by position.
- Cards as `04-api.md`, section 2.1, has them: with `available`, and with their library folder, so that a folder that is away is one record.
- Watch state: `POST /progress` with its rules, played and unplayed for one item or many, putting state back for Undo, series preferences and their reset.
- My list.
- The change sequence, tombstones, `GET /changes`, ETags.
- Pairing by code, with the code lookup, the list of devices waiting and `runnel pair approve`.
- The first run: the setup code, `runnel setup-code` and `POST /setup` (D35). The server's name as a setting (D34).
- A person's own account: devices, password, track defaults.
- Watching for changes with inotify, the nightly walk, and the walk at start.
- The background queue, with the playback gate. Probes move into it.
- `POST /files/{id}/prepare` for direct play, which holds the gate, and `GET /files/{id}`.
- Subtitle and audio files that sit next to a video (`06-library-and-metadata.md`, section 4.7), and `GET /files/{id}/external/{external_id}`.
- The Jellyfin import, dry run first.
- The admin endpoints for libraries, the folder browser, scans with their results, background work with progress, people and devices.
- `play_log`, `GET /admin/playlog` and `GET /admin/stats`.

Where one call carries data that a later stage brings, stage 2 builds the part it has data for and leaves the rest absent, or refuses it plainly:

- `POST /files/{id}/prepare` for a transcode is answered `409 unsupported` until stage 3: the server encodes nothing, and `GET /server` lists nothing to transcode, so a client does not ask.
- An item's detail (`GET /items/{id}`) has no metadata, `credits` or `collection` until stage 4. Its `match.state` is `unmatched` and its `external_ids` are empty. A series' `numbering` comes from its files: setting it by hand is in stage 4's match dialog, and so is mapping a running count to the provider's seasons, so until then a running count's episodes carry provisional season and episode numbers (`06-library-and-metadata.md`, section 5).

**Exit:**

1. Every endpoint of `04-api.md` is implemented and has a test, except those that belong to later stages. Stage 3: transcoding, sessions and `GET /admin/sessions` (a session is an ffmpeg child's, `05-playback.md`, section 5), `prepare` for transcodes, the encoder check. Stage 4: the metadata key, matching and review, collections, credits and people. Stage 5: `GET /admin/update` (`04-api.md`, section 8.8). Stage 6, with the TV app: discovery. Stage 7: playing in a browser (`04-api.md`, section 7.9). Stage 10: the sidecar.
2. The import's dry run joins at least 99% of Jellyfin's films and episodes to local items by path. What is left is explained.
3. After the import, played flags and positions equal Jellyfin's for a sample of 50 items per user.
4. A copy built from `GET /changes?since=0` equals a copy built later from zero, after a day of scans and playback in between.
5. Resident memory stays under 30 MB while a client browses. `GET /home` answers in under 20 ms on real hardware.
6. A new file dropped into a library is playable through the API within 15 seconds, without a manual scan.

## 5. Stage 3: transcoding

The piped transcode and its warm-up, on the server, with mpv on a desktop standing in for a client. About 1 to 2 weekends. Browser delivery (`04-api.md`, section 7.9) shares this stage's argument builder. It is built at the start of stage 7, or here if there is room.

Do first, by hand: the checklist in `05-playback.md`, section 12. It settles the encoder arguments on real hardware before any code depends on them.

Build: the argument builder with its fallback ladder, sessions, relaying, killing on disconnect, slots, the memory limit, finding out what the server can encode and checking again on request (`GET /admin/encoders`), `GET /files/{id}/transcode.mkv`, `POST /files/{id}/prepare` with tickets, and the memory of each ffmpeg child in `GET /admin/sessions`.

**Exit, on real hardware:**

1. Request to first byte sent: within 0.8 s cold.
2. With mpv on a desktop: load to first frame within 1.5 s cold, within 0.5 s after a warm-up.
3. Restarting the stream at the current position with another audio track: within 1.5 s.
4. No ffmpeg process is left one second after a player disconnects, in 20 tries.
5. A transcode with every hazard file from the test set (a subtitle track with long gaps, tens of megabytes of fonts, image-based subtitles) starts within the same limits or is listed as a known exception.
6. The server's own memory stays under 30 MB during a transcode. The child's memory is measured and written into `05-playback.md`.

## 6. Stage 4: metadata

About 2 to 4 weekends, then a long tail of odd cases. In two parts, because the Jellyfin import already supplies provider ids for most of the library:

1. **Fetch by known id**: the metadata key as a setting with its check (D34), the TMDB client and the provider cache, fetching films, series and seasons, artwork downloads, refresh. With them: collections, and cast and crew with people, whose photos are fetched only when a client asks (D33). This is what makes a client look right, and it is the easy half.
2. **Match what has no id**: search and score, the review list with why each item is on it, manual match and unmatch, leaving an item unmatched, the preview of a series match, then the anime profile: the offline title index, the Anime-Lists mapping, and mapping a running count to seasons.

**Exit:**

1. Every item that came with an id has its title, overview and artwork.
2. With the Jellyfin import ignored, at least 90% of a real library's films and series match by themselves.
3. In an audit of 100 automatic matches, none is wrong. If one is, the threshold goes up until none is.
4. The series that is numbered in one running count shows the right title for every episode.
5. Everything unmatched is on the review list, is still playable, and can be fixed from the list.
6. No provider request is made twice for the same thing: a second full refresh of an unchanged library makes none.
7. A collection appears when a library holds two of its films, and goes when one is left.
8. A film's and a series' credits arrive with its details. After the whole library is matched, no person's photo has been downloaded. The first request for one fetches it.

Check first, when this stage is specified: how wide the provider's episode stills are. The TV's episode rows are 400 px wide (`11-design-review.md`, U5).

## 7. Stage 5: the web interface

Setup, administration and browsing, in any browser, served by the server itself. No player yet.

The screens are those of `design/web.md`. It is built with Svelte, TypeScript and Vite, and embedded in the binary (D41). It must work on a plain-HTTP address (D45). Do first: a spike that measures the first load against the brief's aim.

Installing and updating come with it:

- **The install script** (D46): `deploy/install.sh`, attached to each release. It installs, updates and removes the server as a systemd service.
- **The update check** (D47): once a day the server asks GitHub whether a newer release exists, and the web interface tells an admin, with `GET /admin/update` (`04-api.md`, section 8.8). It is on by default, and `RUNNEL_UPDATE_CHECK=off` stops it. The server never replaces its own binary: the update is the install script.

**Exit:**

1. A fresh install can be set up from the browser alone, by someone who holds the setup code and nothing else: the first person, a library chosen in the folder browser, the metadata key, a check of what the server can encode.
2. Everything the subcommands do can be done here: libraries, scans, users, devices, approving a pairing code, the review list and manual matches, the Jellyfin import with its dry-run report.
3. The library can be browsed and searched, items marked played with Undo, and films and series put on My list.
4. It works at phone width and with a keyboard alone.
5. The server's resident memory is not measurably higher for serving it.
6. A release installed with `deploy/install.sh` is updated to the next one by the same command, keeping its data, after the web interface has shown an admin that the newer release exists.

## 8. Stage 6: the Android TV app

The screens are those of `design/tv.md`. Kotlin, Compose for TV and libmpv (D42). Do first: write the discovery responder and test it against Android's and Apple's browsers and against Avahi (P6), and decide whether the apps live in this repository.

**Exit, on a real TV:**

1. Sign in by pairing code. Browse, search, play, resume and move to the next episode, with the remote alone.
2. For files the TV decodes: Play to first frame within 0.5 s, median of 20 starts from `play_log`. Audio switch within 0.1 s.
3. For files it does not: Play to first frame within 1.5 s cold and 0.5 s after a warm-up. Audio switch within 1.5 s.
4. A position saved on the TV shows up on a second device after its next sync.
5. With the server stopped, Home still draws from the local copy and says the server is away.
6. A format the TV decodes only in software is re-encoded by default, and is played as it is once its switch is on (D37).
7. The app finds the server on the local network without an address being typed, and takes a typed address where it cannot.

## 9. Stage 7: the web player

Playback inside the web interface: browser delivery on the server, a player page, subtitles drawn by the page.

Do first: the browser tests of `05-playback.md`, section 12, item 10, in Firefox and Safari, and the choice of the package that wraps libass (D40).

**Exit:**

1. An episode plays in current Firefox, Chromium and Safari, on a desktop and on a phone, with styled subtitles.
2. On the home network: Play to first frame within 0.5 s with the video copied and 1.5 s re-encoded, a seek within 1.5 s, an audio switch within 0.5 s.
3. Progress is reported as from any other client.
4. All of it on a plain-HTTP address (D45).

## 10. Stage 8: phones and tablets

Android first, then iOS. Kotlin Multiplatform with Compose Multiplatform, on the core the TV app uses (D43). Do first: the spike of D43, mpv drawing inside a Compose screen on iOS. Settle O13 before anything goes to the App Store.

The screens are those of `design/mobile.md`. iOS uses the same screens with its own manners, described in words (section 12 there): it needs drawings before it is built. Away from home the apps use whatever address reaches the server (D32).

## 11. Stage 9: desktop apps

Linux first, then Windows and macOS. Until then a desktop has the web player, and mpv for anything a browser cannot play well.

The screens are those of `design/desktop.md`. The desktop app is the web interface in a native window, with libmpv for playback (D44). Do first: choose the shell (O12).

## 12. Stage 10: polish

In whatever order is wanted. Each is its own small project.

- The sidecar file and local track switching during a transcode. Target: audio switch within 0.15 s.
- Preparing the next episode while the credits run.
- A skip button from chapter names, where the files have them.
- A font cache on the clients.
- Watching away from home, beyond what D32 gives: a transcode mode with a bitrate cap and re-encoded audio, and downloads for offline viewing.
- A memory cap on ffmpeg children through cgroups, counting GPU memory.

## 13. Running beside Jellyfin

Runnel can run beside Jellyfin on the same files for as long as wanted, and a switch can wait until the stages have held in daily use. The server writes nothing Jellyfin reads, so there is nothing to undo. Jellyfin is switched off only for what stages that are done provide, not stages that are only built (D48).

## 14. Not being built

Keeping these out is what keeps the project small: live TV, DLNA, plugins, device profiles on the server, burned-in subtitles, tone mapping, music, compatibility with Jellyfin's API. Each one costs more than it looks. Bringing one in needs an entry in `08-decisions.md` first.

## 15. How work is tracked

- One GitHub milestone per stage. One issue per task in the lists above. A pull request names the issue it closes.
- Every change reaches `main` by pull request, merged with Squash and merge. The repository's rules are in `00-start-here.md`, section 7.
- A change in behaviour updates the document that describes it, in the same pull request.
- A decision that changes a document's premise is written into `08-decisions.md` first.
- A stage is *built* when its list is merged and its exit criteria pass in a development sandbox. It is *done* when they are met on real hardware (D48). The next stage may start once a stage is built.
- The checks a built stage still owes on real hardware are an open issue of its milestone, and the milestone stays open until they are met. What they find is fixed first, in whatever stage is then in progress.
- A release cut while a stage is built and not done is a pre-release.
- The results from real hardware are kept outside the repository (D28).
