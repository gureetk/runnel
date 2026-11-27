# Decisions

What has been decided, when, and on what evidence, so that nobody has to work it out again. "Research notes" means the project's log of tests. It is not published, because it describes one particular setup.

To add one: give it the next number, the date, one sentence for the decision, the reason, and where the evidence is. To change one: add a new entry that names the one it replaces. Do not edit history.

## Accepted

**D1. A file-and-index server with its own client, not a lean Jellyfin.** 20 November 2026.
The version one person can finish and maintain is a thin server driven by an mpv-based client. Live TV, DLNA, plugins, device profiles, burned-in subtitles, tone mapping and music stay out. Research notes, sections 1 and 3.

**D2. Its own API, not Jellyfin's.** 20 November 2026.
The server is for one household with its own client. Its API can then express things Jellyfin's cannot: a piped transcode, a sidecar, a warm-up, a change feed. Compatibility with third-party clients is a different and much larger project. Research notes, section 3.3.

**D3. Go.** 20 November 2026.
Go and Rust prototypes were about 4 MB apart idle and both far below the goal. Go has one third-party module against 68, and this server's work, waiting on SQLite, files and child processes, is plain blocking code in Go. Research notes, section 5.2.

**D4. SQLite, one file, WAL mode.** 20 November 2026.
No database server to run or to feed with memory. One writer connection, a few readers, 1 MB caches. The driver is `mattn/go-sqlite3` with cgo, because that is what was measured. `03-data-model.md`.

**D5. The client decides how to play.** 20 November 2026.
The item detail carries codec, profile, bit depth and tracks. The client knows its device. So there is no negotiation call before playback and no device profile on the server. `04-api.md`, section 7.1.

**D6. Transcodes are piped Matroska, not HLS segments.** 20 November 2026.
A segment is usable only when complete, so its length divided by the encode speed is dead time: 2.3 s for 3-second segments against 0.6 s piped, same encoder. Research notes, section 4.2.

**D7. ffmpeg and ffprobe run as child processes.** 20 November 2026.
Spawn, probe, encoder start and first packet together took 80–100 ms. Linking the libraries would put their crashes and their memory inside the server. Research notes, section 2.

**D8. Audio switching during a transcode: restart first, sidecar second.** 20 November 2026.
Switching inside a piped stream went silent, and separate HLS renditions ended playback with fMP4 segments. Restarting the stream at the current position took 240–266 ms and needs nothing new. A seekable audio-and-subtitles file beside a video-only transcode took 16–113 ms and is the later refinement. Research notes, section 4.4.

**D9. Transcodes start on focus, held by a full pipe.** 20 November 2026.
A one-second head start cut the start from about 0.6 s to about 0.15 s. An unclaimed warm-up stops using CPU when its buffer fills. It keeps its memory, so one is held at a time, for 30 seconds. Research notes, section 4.8.

**D10. Re-encode video only. Nothing is burned in.** 20 November 2026.
Audio, subtitles and fonts are copied. mpv decodes and renders all of them. Research notes, sections 4.5 and 5.5.

**D11. The server never writes into a library folder.** 20 November 2026.
Jellyfin, or anything else, can run beside it on the same files, and switching back costs nothing. Research notes, section 5.1.

**D12. Files and items are separate, and nothing is deleted because a disk is away.** 20 November 2026.
A file is playable before it is matched. Watch state hangs on items. Fingerprints let state follow a moved file. Missing files are marked, not removed. `03-data-model.md`, section 4.

**D13. Changes are a sequence number stamped on rows, with tombstones for deletes.** 20 November 2026.
Replaces the change-log table of the first sketch. Less code, and it cannot grow without bound. `03-data-model.md`, section 5.

**D14. Names are parsed by Jellyfin's documented conventions, plus release-style episode names.** 20 November 2026.
The folders give the series and the season, so a file name only has to yield an episode number. Each result carries a confidence class, and the lowest goes to review. A prototype parsed 11,417 of 11,423 synthetic names, with every error in that class. `06-library-and-metadata.md`, section 4.

**D15. Metadata comes from TMDB, after a one-time import from Jellyfin.** 20 November 2026.
A library that Jellyfin has already matched is imported, which beats matching again. Anime uses Anime-Lists and anime-offline-database to reach TMDB. TheTVDB, AniDB and AniList are not called in version 1. Research notes, section 7.

**D16. The server never decodes a large image in its own process.** 20 November 2026.
Provider artwork is downloaded at the provider's sizes. Local artwork is scaled once by an ffmpeg child. That keeps a common source of memory spikes out. `06-library-and-metadata.md`, section 8.

**D17. Configuration is flags and environment variables. Everything else lives in the database.** 20 November 2026.
No configuration file to parse, and no third-party parser. Libraries and users are managed by subcommands and the admin API. `02-architecture.md`, section 6.

**D18. Per-device bearer tokens, pairing by code, PBKDF2 passwords.** 20 November 2026.
All from the standard library. A TV pairs by showing a code that another signed-in device approves. `04-api.md`, section 3.

**D19. Standard library first.** 20 November 2026.
A third-party module needs an entry here. Two are accepted: the SQLite driver, and `golang.org/x/sys` for inotify. `02-architecture.md`, section 3.

**D20. The project is called Runnel.** 20 November 2026.
A runnel is a small stream, which suits a server that streams. It was chosen from a short list over Riffle, Turnstone and Kinora. The binary is `runnel`, the environment variables start with `RUNNEL_`, the data directory is `/var/lib/runnel`, the service user is `runnel`, the response headers are `X-Runnel-…` and tokens start with `rn_`. This replaces the placeholder `mediad` and closes O1. The name has not yet been checked for collisions on GitHub, in package registries or among domains: `01-roadmap.md`, section 2.

**D21. The logo is a soft play button that is an island, with a river cut through it.** 20 November 2026.
A play button reads as a media player at once, and the island and river say Runnel. The island has contour rings, not water-lining. There are two colourways: Lagoon for light grounds and Paper for dark ones. Three short current marks sit in the river on icons of 48 px and larger; at 32 px and below the simple icon without them is used. Files, colours and rules: `09-brand.md`.

**D22. Transcoding is stage 3, straight after the library API, whatever the TV test says.** 20 November 2026.
Some clients cannot decode a library's codecs whatever one TV can do: browsers, older phones, and any player without the right decoder. So the transcoding path is needed in every outcome, and it comes before any client is built. This closes O3. Gate G1 is no longer a gate; the test still decides what the TV app does by default. `01-roadmap.md`.

**D23. Runnel has its own official clients.** 20 November 2026.
The official clients are a web interface, an Android TV app, an Android phone and tablet app, an iOS app, a Linux desktop app, and later Windows and macOS apps. This replaces an earlier plan to adapt an existing client of another kind. `10-clients.md`.

**D24. The web interface plays video in the browser.** 20 November 2026.
A browser is the one client every device already has, so the web interface is how Runnel reaches a device before its app exists. That needs a way to deliver a file a browser can play, and subtitles the page can draw (O11). This replaces "a web player" in the list of things not being built. `10-clients.md`, section 2.

**D25. The order of the clients.** 20 November 2026.
The web interface for setup, administration and browsing; then the Android TV app; then the web player; then Android phones and tablets; then iOS; then Linux desktop; then Windows and macOS. Setup and administration are needed by everything. The TV app is where most watching happens. The web player then covers every other device at once, so the apps after it improve the experience and are not the only way in. `01-roadmap.md`.

**D26. Client design is done in its own chats, from a brief.** 20 November 2026.
The brief is `10-clients.md`. Design work writes under `design/` and does not edit documents 00 to 09. What a design needs from the server is listed in the design's own document and brought back to the engineering documents afterwards. That keeps two lines of work from overwriting each other.

**D27. The repository is `gureetk/runnel`, and `main` only changes by pull request.** 20 November 2026.
Work happens on a branch, goes through a pull request, and is merged with Squash and merge, so every commit on `main` is one reviewed, verified commit. Nothing is pushed to `main` directly. No commit, pull request or issue carries a link to a Claude chat, session or project.

**D28. Nothing about anyone's own setup goes into the repository.** 20 November 2026.
The repository is public. It carries no description of a particular household's hardware, library, network or devices, and no measurement taken on them. Documents are published there as separate public editions with those parts removed (`00-start-here.md`, section 7). The research notes and the scripts written during the research stay out of the repository altogether. This replaces `tools/lab` in `02-architecture.md`, sections 4 and 13: the repository's tests are written for it, not carried over.

**D29. The client designs are accepted, with the fixes from their review.** 20 November 2026.
Direction D stands: Findroid's structure in Runnel's colours (DS5), with DS1 to DS7, TV1 and TV2 as the design documents record them. The designs were checked against Findroid's source, the brief and the server design. Eight findings came out. The fixes were written into `design/tv.md`, `design/mobile.md` and `design/web.md`, and drawn on the canvas, the same day. That is a one-time exception to D26. `11-design-review.md`.

**D30. A series is one page.** 20 November 2026.
Season chips, then the episodes, on the series page. Pressing an episode plays it. An episode has no page of its own: its options and File details open from it. Findroid's three levels (show, season, episode) were considered. One page is fewer presses on a remote, and it keeps "play is one press" (`10-clients.md`, section 3). `11-design-review.md`, section 3.

**D31. Runnel's colours on every device.** 20 November 2026.
No colours taken from a phone's wallpaper in version 1. One look everywhere is DS2, and contrast was checked for the two themes only. It can be added later as a setting on Android without redrawing a screen.

**D32. Away from home, a client uses whatever address reaches the server. Runnel provides no relay.** 20 November 2026.
Discovery is for the local network. Anywhere else a client is given an address: a host and port, or a full `https://` address. Making the server reachable from outside, with a VPN or with a reverse proxy that adds TLS, is the job of whoever runs it, as TLS already is (`04-api.md`, section 1). Tokens travel in requests, so plain HTTP is for a trusted network. Version 1 has no downloads for offline viewing and no transcode capped by bitrate: `max_h` is the one lever. Both can be reopened once the phone app exists. This closes O7. `11-design-review.md`, F3.

**D33. Version 1 includes My list, collections, and cast and crew.** 20 November 2026.
This accepts DS4. My list arrives with watch state. Collections and credits arrive with metadata (stage 4). A collection is shown when the library holds two or more of its films. Names and roles are stored when an item is matched. A person's photo is fetched the first time a client asks for it, so that matching a library does not download a photo of everyone credited in it. This takes cast and crew, collections and the watchlist out of the list of things not in version 1 (`10-clients.md`, section 4).

**D34. Two settings can be changed while the server runs. This amends D17.** 20 November 2026.
The server's name and the metadata key can be set by an admin through the API, and are kept in the database. Everything the server needs in order to start stays a flag or an environment variable, and D17 holds for it. Where the environment sets one of the two, the environment wins and the web interface shows it as fixed. The first run and the About page need both (`design/web.md`).

**D35. The first person is made with a setup code.** 20 November 2026.
While no person exists, the server would make an admin of whoever opens its address first. So it prints a setup code in its log when it starts without people, `runnel setup-code` prints it again, and the first run asks for it. The code stops working once the first person exists. `11-design-review.md`, F6.

**D36. The local copy carries the text a screen draws at once.** 20 November 2026.
Synced cards gain the overview, genres, rating and age rating, so lists and the top of a detail page need no request. File details, chapters and credits stay on request, and a client keeps them once fetched. Whether a folder is away is reported once for the folder, not on every card under it, so a disk going away changes one record. `04-api.md`, sections 2.1 and 5. `11-design-review.md`, F2.

**D37. A format a device decodes only in software has a switch. On a TV it starts off.** 20 November 2026.
A client's player can decode in software what the device's chip cannot. On phones, tablets and computers that is on by default. On a TV it is off by default, so the server re-encodes that format: a TV's processor may not keep up, and a re-encode is known to play smoothly. The person can turn it on to try it. The decoding test in `00-start-here.md`, section 6, can change the default for a device that passes it. `design/tv.md`, TV3.

**D38. The licence is GPL-3.0-or-later.** 20 November 2026.
For the server, the web interface and every app. Source files start with `// SPDX-License-Identifier: GPL-3.0-or-later`, or the same line in the file's own comment style. AGPL-3.0-or-later was the alternative: it adds a duty towards people who use a modified copy over a network, which a server for one home does not need. This closes O2. The iOS app raises a question of its own, now O13.

**D39. A browser is fed two piped streams, through Media Source Extensions.** 20 November 2026.
One stream carries the video and one carries a single audio track. Each is fragmented MP4, made by an ffmpeg child as it is requested, with the file's own timestamps. The page appends them to two source buffers. The page asks the browser what it can decode and asks the server to copy or to re-encode, so the client still decides (D5). Switching audio replaces the audio stream alone: in a test the new track was buffered 88 ms later with the video copied, and 171 ms later during a re-encode, and the picture did not stop. A seek outside the buffer is a new pair of requests. A plain `src` was rejected: a piped stream cannot be sought that way, and Safari needs byte ranges. Segments were rejected for the reason in D6. Tested in Chrome 153 only, in a sandbox with software codecs: Firefox and Safari are untested. `05-playback.md`, section 13. `04-api.md`, section 7.9. This closes O11.

**D40. In a browser, styled subtitles are drawn by libass compiled to WebAssembly.** 20 November 2026.
The page fetches the subtitle track and the file's fonts from the server and draws on a canvas over the video. Nothing is burned in (D10), and the rendering engine is the one the apps use. It is loaded only when a styled track is shown: about 2.8 MB before compression. In a test the `libass-wasm` package drew a line with an attached font on a plain-HTTP page, 59 ms after the time changed. The server converts other text subtitles to the same format. Picture subtitles (PGS, VobSub) are not shown in a browser in version 1. Which package wraps libass is chosen at stage 7: `libass-wasm` and `jassub` both do. `05-playback.md`, section 13.

**D41. The web interface is static files: Svelte and TypeScript, built with Vite, embedded in the binary.** 20 November 2026.
It talks to the same API as every other client, and the server renders no pages. Svelte compiles components away, which keeps the first load small, and its compiler warns about accessibility mistakes. Node is needed to build the web interface, not to run the server. A runtime dependency of the web interface needs an entry here, as a Go module does (D19). So far there are two: Svelte, and the subtitle renderer of D40. No measurement is behind this choice. A spike at the start of stage 5 measures the first load against the brief's aim of the low hundreds of kilobytes. What would change it: a first load well above that, or the desktop shell (D44) being unable to host it. This closes O10.

**D42. The Android TV app is Kotlin, with Compose for TV and libmpv.** 20 November 2026.
The TV app runs on Android TV and Google TV only, so it uses the platform's own toolkit. Its player is mpv inside the app, bound through JNI, as existing mpv-based Android apps do. That Compose for TV is stable is from memory, to be checked when stage 6 starts.

**D43. The phone and tablet apps are Kotlin Multiplatform with Compose Multiplatform, on a core shared with the TV app.** 20 November 2026.
One set of screens for Android and iOS, which is what one Runnel look (DS2) asks for. Compose Multiplatform for iOS has been stable since May 2025 [checked: JetBrains' release post]. The shared core holds the API client, the local copy, sync and the player's state. The TV app uses the same core under its own screens. The player is libmpv on both systems: through JNI on Android, and on iOS through a build of mpv under the LGPL (O13). To prove at the start of stage 8, before any screen is built: mpv drawing inside a Compose screen on iOS. If that fails, the iOS screens are written in SwiftUI on the same core.

**D44. The desktop app is the web interface in a native window, with libmpv for playback.** 20 November 2026.
That is what `design/desktop.md` draws, and it reuses the web interface whole. What hosts the pages and the player in one window is open (O12). This closes O9.

**D45. The web interface works on a plain-HTTP address.** 20 November 2026.
A home server is reached at an address like `http://192.0.2.10:8610`, which a browser does not treat as secure. Measured in Chrome 153 on such an address: Media Source Extensions, IndexedDB and workers are there. The camera, the clipboard API, service workers, `crypto.subtle` and `crypto.randomUUID` are not. So nothing in the web interface may need them: no QR scanning in a browser (the apps scan, and a phone's own camera opens the `/pair` link), device ids made with `crypto.getRandomValues`, no service worker. Behind a reverse proxy with TLS they exist, and a page may use them when they are there. `05-playback.md`, section 13.

**D46. An install script installs, updates and removes the server, from stage 5.** 21 November 2026.
`deploy/install.sh` does what the unit's comments describe by hand: the service's user, the binary, the unit and its drop-ins (`02-architecture.md`, section 11). Run again, it updates to the newest release, pre-releases included, and keeps the data directory. It can also start over with a fresh data directory, build from source, and remove everything it added. Each release carries it beside the binary, so one command installs a release on any distribution the static build runs on. Updating stays this command: the server does not replace itself (D47). A first version was tested in a development sandbox, with stand-ins for systemd: installs from source and from a release, an update, a fresh data directory and a removal.

**D47. The server checks once a day for a newer release and tells an admin. It never updates itself.** 21 November 2026.
From stage 5. Once a day the server asks GitHub for the project's list of releases, and the web interface shows an admin the newer version and how to update (`04-api.md`, section 8.8). The check is on by default, and `RUNNEL_UPDATE_CHECK=off` stops it. Being an environment variable, it leaves D17 as it is, and D34's two settings stay two. The request sends nothing about the server or its library, not even its version. GitHub sees the address it comes from, as with any request. The server does not download or swap its own binary. Its unit keeps the binary unwritable to it (`ProtectSystem=strict`, `02-architecture.md`, section 11), and code that a server fetches and runs without a person would need signed releases. The update is the install script (D46).

**D48. A stage may start once the stage before it is built. It is done only when its checks have run on real hardware.** 21 November 2026.
Stage 1 is built, and its six exit criteria pass in a development sandbox on generated files. They have not been run on real hardware with a real library yet. Stage 2 starts anyway, which changes the rule of `01-roadmap.md`, section 15. A stage is *built* when its list is merged and its criteria pass in a sandbox. It is *done* when the criteria are met on real hardware. The next stage may start when a stage is built. The checks still open are tracked in an issue of the stage's milestone, which stays open until they are met, and what they find is fixed first in the stage then in progress. A release cut while a stage is built and not done is a pre-release, and switching away from Jellyfin waits for stages that are done (`01-roadmap.md`, section 13). The cost is known: a fault that only real files show is found later, with more built on it. The parser is where that is likeliest, so stage 2 opens by hardening it, and the parse report gains a note where several files make one episode.

**D49. Two more kinds of library: mixed films and shows, and home videos. Built in stage 4.** 27 November 2026.
A library is films or shows, and the schema enforces it. That leaves out a folder that holds both, and videos that are neither, such as home videos, which nothing should try to match. Jellyfin has a mixed type, and its documentation discourages it because of unreliable metadata [checked: its page on libraries, 27 November 2026]. So a mixed library here settles what each folder is before anything is matched, by rules the parse report shows. In a mixed library (`mixed`) a folder is a series when it holds a season folder, a file with an `SxxEyy` token, or two files that the anchored class gives different numbers and that the film rules do not read as parts. Every other folder is a film, and the report notes one whose files look like episodes. A lone file at the top of a root is a film, unless it has an `SxxEyy` token: then it makes no item, as in a shows library. Grouping folders are read the same way in films, shows and mixed libraries: a folder without a year that holds no video and no season folder of its own, and whose folders are each a film's, a series' or another grouping folder, makes no item, and the folders in it are read as if they were at the top of the root. Until now such a folder became one film or one series. In a home-videos library (`videos`) every video file is an item of a new type, `video`. It is titled from its file name, and dated by the container's `creation_time` where ffprobe reports one, otherwise by the file's modification time. ffprobe reports the tag on generated MP4 and Matroska files [checked in a development sandbox, ffprobe 6.1]. What phones and cameras write is untested. A video has no versions, parts or extras, is never sent to a provider and is never on the review list. Its picture is a frame taken from it. It is browsed by folder, and folders are not items. It is played, resumed and listed as a film is. A films or a shows library may be changed to mixed, and no other change of kind is allowed: what reads the same keeps its id and watch state, and what now reads as the other kind becomes a new item, and the old one's watch state goes with it. Both kinds are CHECK constraints, so their migration rebuilds `libraries` and `items`, and the database is copied into `backup/` first (`03-data-model.md`, section 7). The web design gains the two kinds and a folder view before stage 5 is specified. Whether the Jellyfin import can bring the watch state of Jellyfin's home videos is checked against its API when this part is specified.

**D50. VAAPI on Intel GPUs. QSV is not used.** 27 November 2026.
ffmpeg reaches an Intel GPU both ways. Run by hand on real hardware on the same input, QSV was the slower of the two and held more memory (`05-playback.md`, section 12), and the VAAPI form is the one known to work there. The figures are kept outside the repository (D28). So the server encodes with VAAPI, or else in software, and `RUNNEL_HWACCEL` takes `auto`, `vaapi` or `none`. This closes O5. One GPU is one data point: QSV can come back by a new decision if another GPU shows the opposite.

**D51. A video in a series' folder always makes an item.** 27 November 2026.
A file in a series' folder whose name gives no episode number made no item, so no client could list or play it, though Jellyfin lists such files: specials numbered in a release group's own scheme, a decimal episode such as `08.5`. A server that takes over from Jellyfin cannot hide a file that Jellyfin plays. So in a shows or a mixed library, a video in a series' folder that is not an extra (`06-library-and-metadata.md`, section 4.6) and whose name gives no number, or only a decimal, is an episode without a number. It sits in the season that its folder names; without a season folder, a decimal stays in season 1 with the numbered episodes and any other goes among the specials. It is titled from its file name and keyed by its file, so it follows its file as any episode does. It is listed after the season's numbered episodes, by title, and one with a decimal right after the whole number before it. It is never matched to a provider's episode by number, and it is on the review list. The parse report gives such files a class of their own, `unnumbered`, so that `none` again means a file that makes no item. Next up and a series' resume step over it, as they step over specials, while the season's list and an episode's `prev` and `next` take it in its place. Which names make a file an extra stays Jellyfin's lists: an opening without credits in a season folder is such an episode, as Jellyfin lists it. On a card its `episode` is null. The cost: an episode's number may be empty, in the API's cards and in every client's design, which owes a way to show one before the clients that list episodes are built.

## Proposed, to confirm on real hardware or in use

**P1. Encoder arguments.** HEVC 10-bit for 10-bit sources when the client decodes it, quality-based rate control, no B-frames, VAAPI first (D50). The HEVC forms and the second rung of the ladder ran by hand on real hardware. H.264 on the GPU, scaling, and the output on a TV are still to be checked. `05-playback.md`, sections 4 and 12.

**P2. Limits.** One transcode slot, one held warm-up, a 1024 MB cap on an ffmpeg child. `05-playback.md`, section 4.4.

**P3. Progress rules.** A stop below 30 seconds clears the position. At or beyond 90% the item is played. `04-api.md`, section 7.6.

**P4. Match thresholds.** Accept at 0.90 with a 0.10 lead. `06-library-and-metadata.md`, section 6.2.

**P5. Default port 8610.**

**P6. Discovery by DNS-SD.** The server answers multicast DNS for one service, `_runnel._tcp`, with its name, version and instance id in the text record. It only responds: it announces itself at start, answers queries, and says goodbye when it stops. Proposed: a small responder of the server's own on the standard library, tested against Android's and Apple's browsers and against Avahi before it ships. A module would need a new decision (D19). Not prototyped. Known limits: it does not cross subnets, and in a container it needs the host's network. On Android 17 and later, an app that targets it needs the local network permission to reach any address on the home network, whether found or typed. The system's own service picker is the way without the permission [checked: Android's developer documentation, November 2026]. On Apple systems the app declares the service it browses for, and the system asks the person once [from memory]. `04-api.md`, section 3.6.

**P7. Small limits from the design review.** A setup code is eight characters from the pairing alphabet, and five wrong ones a minute from one address is the limit. A token's `last_used_at` is written at most every ten minutes. The start-time log keeps its last 1,000 rows. One token may look up ten pairing codes a minute. At most 20 pairings wait at once, one address may start ten a minute, and an expired code is remembered for an hour. A password has at least ten characters. An item's credits keep the first 20 of the cast, the directors and the writers. A person's photo that could not be fetched is not asked for again for a day. Recently added on Home reaches back 30 days, from the start of the day. `04-api.md`, sections 2.4, 3, 4.2 and 6.

**P8. Browser delivery numbers.** Fragments of half a second when the video is copied and a fifth of a second when it is re-encoded. The page keeps about 30 seconds buffered ahead. Not tuned. `05-playback.md`, section 13.

## Open

**O6. The pure-Go SQLite driver.** Would remove cgo. Not measured.

**O8. Music.** Out of scope for now.

**O12. The desktop shell.** What hosts the web interface and libmpv in one window on Linux, Windows and macOS (D44): Qt with its web view, or a lighter shell around the system's web view. It has to draw the page over mpv's picture, and work under Wayland and on arm64 Linux. A spike at the start of stage 9.

**O13. The iOS app and the GPL.** Apple's App Store terms and the GPL have clashed before [from memory]. Three things to settle before the iOS half of stage 8. The player libraries in the iOS build must be LGPL builds: one of mpv for iOS exists, MPVKit [checked: its README]. Whether the project's own code can go to the App Store under D38 as it stands, which is simple only while every line of the app is the maintainer's. And what a contribution from someone else to the app would need: an added permission in the licence notice, or an agreement. Not legal advice. Until it is settled, the iOS app can be built from source.
