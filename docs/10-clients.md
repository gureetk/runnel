# Clients and design brief

Runnel has its own official clients. This document says which, in what order, and what a designer needs to know to design them. It is the starting point for the design chats (`08-decisions.md`, D26). Read `00-start-here.md` and `09-brand.md` first.

Nothing here describes a particular household's setup, so it can be published as it is.

**State, 20 November 2026.** The designs exist under `design/`. They were reviewed (`11-design-review.md`) and accepted with fixes (D29). Section 4 says what the server gives now, after the designs' asks were folded into `04-api.md`. The questions of section 7 are answered. What each client is built with is decided (D41 to D44), and so is how a browser is fed (D39, D40).

## 1. The clients

| Client | Runs on | Input | Role | Built in |
|---|---|---|---|---|
| Web interface | Any browser. Served by the server itself. | Pointer, touch, keyboard | Setup and administration. Browsing. Later, playback in the browser. | Stage 5, and stage 7 for the player |
| TV app | Android TV and Google TV devices | A remote: D-pad, OK, Back | Where most watching happens. | Stage 6 |
| Phone and tablet app | Android, then iOS | Touch | Watching around the house, and away from it wherever the device can reach the server (D32). | Stage 8 |
| Desktop app | Linux, then Windows and macOS | Pointer and keyboard | The best playback on a computer: every format, instant track switching. | Stage 9 |

**Design order:** the design system, the web interface and the TV app first. The phone, tablet and desktop designs follow from the same system and can wait.

## 2. Two kinds of player

A designer needs to know this because it changes what a screen can promise.

| | Apps (TV, phone, tablet, desktop) | Web player |
|---|---|---|
| Player | mpv, inside the app | The browser's own video element |
| What it plays | Every file in the library, as it is on disk | What the browser can decode. The server repackages the stream, and re-encodes the video when it must. |
| Audio or subtitle switch | Instant, when the file is played as it is. About a second when the video is being re-encoded. | A fraction of a second. Only the audio stream is replaced, and the picture keeps playing. |
| Seek | Instant inside the file | About a second for a long jump |
| Subtitles | Drawn by mpv, styled as the file says | Drawn by the page, styled as the file says. Picture subtitles are not shown in version 1. |

So every player screen needs a short "switching" state that is not a spinner on black: the last frame held, the control that was used still visible.

How a browser is fed: two MP4 streams made on request, video and audio, which the page plays, with the video copied whenever the browser can decode it (D39). `05-playback.md`, section 13.

## 3. What Runnel should feel like

These come from how the server is built. A design that fights them cannot be made fast.

1. **Screens draw from a local copy.** Every app keeps the library on the device and asks the server only for what changed. Home, a library and a detail page appear at once, with no spinner for anything already known. The first sync after sign-in is the one place a progress screen belongs.
2. **Artwork arrives into a prepared space.** For every image the server supplies its aspect ratio and a placeholder colour. Nothing jumps when an image loads.
3. **Focus is a signal.** When an item has rested in focus for about half a second, the app tells the server to get ready to play it. Do not hang anything heavy on focus: no trailers that start by themselves, no moving backdrops.
4. **Play is one press.** A detail page has one main action. It resumes where the person left off, or plays the next unwatched episode, or starts from the beginning, and says which.
5. **The player gets out of the way.** The audio and subtitle choice is remembered for each series. The next episode starts on the same tracks.
6. **States are honest.** A file whose disk is away says so and cannot be played. An item without metadata yet shows the title from its file name and a plain tile. A server that cannot be reached leaves the app browsable from its local copy, and says the server is away.
7. **Technical detail stays out of the way and within reach.** Whether a file is played as it is or re-encoded, which decoder, dropped frames, start time: all of it in a stats overlay, none of it in the normal interface.
8. **Calm and light.** The brand is an island and a small stream. Two colourways, Lagoon and Paper. The cheapest TV sticks have weak graphics chips: no full-screen blur, no parallax, no layers of animation.

## 4. What the server gives a client

All of it is in `04-api.md`. In a designer's terms:

**To show**

- Libraries, each of films or of shows.
- A card for every film, series, season and episode: title, year, runtime, overview, genres, a rating, an age rating, artwork (poster, backdrop, episode thumbnail, logo), how far the person has watched, whether it is played, whether it is on their list, and whether its file is there. All of that is in the device's local copy, so it draws at once.
- For a series: number of episodes, number unwatched, number of seasons, and the episode to resume.
- A detail page, fetched when it opens and kept: cast and crew, the collection a film belongs to, the episode before and after.
- Collections: films that belong together, shown when a library holds two or more of them.
- People: a photo, what they are known for, and the films and series on the server that credit them.
- For each file: resolution, HDR or not, every audio track with its language and channels, every subtitle track with its language, chapters with their times, and other versions of the same film.
- Home rows: Continue watching, Next up, Recently added (new episodes folded into their series), My list.
- Search, by any part of a title, and a list of genres, for one library or for all.
- The server's name, and whether a library folder is away.

**To do**

- Sign in with a pairing code, or a name and password. Approve another device's code, after seeing which device is asking.
- Play, resume, seek, switch tracks, go to the next episode.
- Mark played or unplayed: an episode, a season, a whole series, a collection, or several items at once. Undo it.
- Put a film or a series on My list, and take it off.
- Set preferred audio and subtitle languages, for everything or for one series. Reset a series' choice.
- See one's own signed-in devices, sign one out, change one's password.

**To administer** (web interface only)

- The first run, started with the setup code from the server's log (D35).
- The server's name and the metadata key.
- Libraries and the folders they scan, chosen in a browser of the server's folders. Scans and other background work, with their progress and results.
- People and their devices. Devices waiting with a pairing code, and approving one.
- A review list of items the server could not match with confidence, each with why, candidates to pick from, and a manual search. Leaving an item unmatched. A preview of how a series' files will map.
- A one-time import from Jellyfin, with a dry-run report before anything is written.
- What the server can encode. Playbacks in progress. A log of how long playbacks took to start.

**Not there in version 1.** Trailers, playlists, favourites, personal ratings, recommendations, parental controls, downloads for offline viewing, casting to another device, music, live TV. (Cast and crew, collections and a watchlist were on this list. They are in version 1 now: D33.) Do not design for these as if they exist. If a design needs one, it goes in the list of asks (section 8), and it is decided before it is drawn in detail.

## 5. Screens

**Every client that browses**

1. **Sign in.** Find a server on the network or type its address. Show a pairing code and a QR code, and wait for approval. Name and password as the other way.
2. **Home.** Continue watching, Next up, Recently added, and a way into each library.
3. **Library.** Thousands of posters. Sort by title, date added, release date or rating. Filter by genre and by unwatched. A way to jump through the alphabet.
4. **Search.**
5. **Film.** The main action, overview, facts about the file, other versions.
6. **Series.** The main action, seasons, episodes with their progress, mark as played.
7. **Player.** Transport and seek with chapter marks. Audio and subtitle pickers. A prompt for the next episode near the end. The switching state. The stats overlay. What an error looks like.
8. **Settings.** This device and who is signed in. Preferred languages and subtitle behaviour. What this device may play as it is. About, with the notice TMDB requires and the licence.
9. **The states of section 3, item 6**, and an empty library.

**Web interface, administration**

1. **First run.** Make the first user, add a library, enter the metadata key, check what the server can encode. Someone who has never seen Runnel should finish it without reading documentation.
2. **Overview.** What the server is doing now: scans, background work, who is playing what, memory.
3. **Libraries.**
4. **People and devices**, with approval of pairing codes.
5. **Review and match.**
6. **Import from Jellyfin.**
7. **Playback**: what can be encoded, limits, the start-time log.
8. **About.**

**Only on some surfaces**

- **TV:** the banner on the launcher, and whether Continue watching appears on the TV's own home screen.
- **Phone and tablet:** portrait layouts, gestures in the player, picture-in-picture, controls on the lock screen, two panes on a tablet.
- **Desktop:** a window that resizes, keyboard shortcuts, hover, full screen.

## 6. Constraints

**TV**

- A remote only. Every screen is reachable and usable with D-pad, OK and Back. Focus is always visible, and where it goes next is never a surprise.
- Read from three metres. Generous type. Keep clear of the screen's edges.
- Avoid typing. Pair by code, search with as few letters as possible.
- Weak hardware: little memory, a small graphics chip. Few images on screen at once, simple motion.

**Web**

- From a 360-pixel phone to a large monitor. Usable with a keyboard alone and with a screen reader.
- Light and dark, from the two colourways.
- It is served from inside the server binary, so it must stay small. A first load in the low hundreds of kilobytes is the aim.
- On a home network it is reached over plain HTTP. A browser then gives a page no camera, no clipboard access and no service worker, so nothing in the web interface may depend on them (D45). Scanning a pairing QR code is for the apps, and for a phone's own camera, which opens the `/pair` link.

**Phone, tablet, desktop**

- Whether these follow each platform's own look or one Runnel look everywhere is a design decision (section 7). It interacts with what they are built with: one shared toolkit favours one look. Both are decided since (DS2, D43, D44).

**Everywhere**

- Contrast and text size that meet accessibility guidelines.
- Text kept in resources, never drawn into images, so it can be translated. English first.
- The mark, colours and wordmark as `09-brand.md` gives them. The design system extends the brand. It does not redraw it.

## 7. Questions the design has to settle

Each is decided with the maintainer, and the answer is recorded at the top of the design's own document.

All six were answered on 20 November 2026: DS1 to DS7 in `design/00-design-system.md`, TV1 to TV3 in `design/tv.md`, and the further decisions at the top of `web.md` and `mobile.md`.

1. **Who is watching.** One person per device, or a quick switch between people on a shared screen? The server can hold several signed-in people for one device. A picker is the client's to design.
2. **One look, or each platform's own.**
3. **How much administration the apps carry.** The proposal is none: administration lives in the web interface, and apps link to it.
4. **The TV's own home screen.** Whether Runnel puts Continue watching there.
5. **Which of the "not there" features version 1 needs after all.** Favourites or a watchlist are the likely ones.
6. **Subtitle appearance.** Which controls a person gets over size, position and style, given that styled subtitles carry their own.

## 8. What a design chat produces, and where

- `design/00-design-system.md`: colours that extend Lagoon and Paper, the type scale, spacing, shapes, focus and pressed states, motion, the components, the icons. For TV, touch and web.
- `design/web.md`, `tv.md`, `mobile.md`, `desktop.md`: the screens, what each is for, its layout, its components, its states, how one leads to another, and the names of the design files that show them.
- In each of those, a section **"Asks of the server"**: any data or action the design needs that section 4 does not list.
- Decisions made along the way, dated, at the top of the document they belong to.

Rules for design chats:

- Write under `design/` only. Leave documents 00 to 10 and `brand/` as they are.
- Put nothing in a design document about a particular household's devices or library. These documents are meant to be publishable as they are.
- Use made-up titles and artwork in mock-ups. No real posters, no real titles, no real names.

## 9. After the design

The designs come back to the engineering documents:

1. The asks of the server are weighed, and what is accepted goes into `04-api.md` and `03-data-model.md`. Done on 20 November 2026 (`11-design-review.md`, D29 to D37).
2. What the native clients are built with is decided, with research. So is the web interface's framework, and how a browser is fed. Done on 20 November 2026: D39 to D45. Two points are open: the desktop shell (O12) and the iOS app under the GPL (O13).
3. `01-roadmap.md` gets exact exit criteria for stages 5 to 9. Done for stages 5 to 7. Stages 8 and 9 have their first steps.
4. Each client gets its own engineering document.
