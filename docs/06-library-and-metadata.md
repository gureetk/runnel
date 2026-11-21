# Library and metadata

How files on disk become items a person can browse: scanning, probing, parsing names, building items, matching them to a metadata provider and fetching artwork. Tables and columns refer to `03-data-model.md`. Evidence is in the project's research notes, which are not published.

**Revised, 20 November 2026,** for what the client designs added (D33, D34, `11-design-review.md`): collections, cast and crew, photos fetched when first asked for, why an item is on the review list, leaving an item unmatched, the preview of a series match, the import's report, and progress of background work. Nothing here has been run against a provider yet.

## 1. The pipeline

```
scan ──► probe ──► parse ──► build items ──► match ──► fetch metadata ──► fetch images
 │         │                     │
 │         └ file is playable     └ item is browsable, under a title taken from its name
 └ file is known
```

Two rules shape all of it:

- **A file is playable as soon as it is probed.** Matching and artwork arrive later, in the background, and never block playback.
- **A missing match is better than a wrong one.** Anything uncertain goes to a review list, stays playable, and waits for a person.

The server never writes into a library folder.

## 2. Scanning

### 2.1 What a scan does

For each root of a library:

1. **Check the root.** If it cannot be listed, or lists empty when the database holds files for it that are not missing, mark it unavailable and stop. Nothing is marked missing.
2. **Load what is known**: `id, rel_path, size, mtime_ns, missing_since` and the fingerprint for the root, into a map.
3. **Walk the tree.** For every video file compare size and modification time with the map.
   - Unknown path: a new file, unless it is a known file that moved (step 4).
   - Changed size or time: reset `probe_version`, queue a probe.
   - Marked missing, and there again unchanged: clear `missing_since`.
   - Same: nothing.
   - A folder below the root that cannot be read is skipped, and nothing in it is marked missing. The scan's result says how many could not be read.
4. **Moves.** A new path whose fingerprint equals that of a file of the same root that is marked missing, or that was not seen in this walk, is that file at a new path: the row keeps its id and its probe, and takes the new path. Only new paths with the size of such a file are read for their fingerprint, so a scan reads no file's content otherwise. A file not probed yet has no fingerprint, so a rename before its first probe is a new file and a missing one. A move across roots is a new file and a missing one too.
5. **What was not seen**, and did not move, gets `missing_since`. A file missing for more than 30 days is deleted (`03-data-model.md`, section 4).
6. **Write** all of it in one transaction, then queue probes for new and changed files.
7. **Record what the scan found** on the root: files added (new, and back from missing), changed (including moved) and removed (newly missing), how long it took, or the error that stopped it. The administration page shows it (`04-api.md`, section 8.2), and the server logs it.

Measured with a Go prototype on a synthetic tree of 14,720 video files in 3,818 folders:

| Step | Time |
|---|---|
| First index, empty database, no probing | 0.2 s |
| Rescan, nothing changed | 87–114 ms |
| Rescan after 12 additions, 5 changes, 3 deletions | 100 ms, and it found exactly those |
| Rescan with the file cache dropped, on an SSD | 299 ms |

Peak memory was 13–15 MB. On spinning disks a cold walk costs about one seek per folder, so tens of seconds after a reboot or a spin-down.

### 2.2 When scans run

- **On change.** The server watches every folder under each root with inotify. Events are collected for 5 seconds, then the affected folders are scanned. This keeps disks asleep when nothing changes.
- **Nightly**, a full walk as a safety net, and at start.
- **On request**: `POST /admin/scan`, and when a library is created.
- A file that is still being copied is left alone until its size has stopped changing for 10 seconds.
- If a root cannot be watched (a network mount, or the system's watch limit), the server logs it once and scans that root every 15 minutes.

### 2.3 What counts

- **Video files**: `.mkv .mp4 .m4v .mov .avi .ts .m2ts .webm .wmv .mpg .mpeg`.
- **Ignored**: names that start with a dot, `@eaDir`, `lost+found`, anything ending in `.part`, `.partial`, `.!qB` or `.tmp`, and files named `sample`. Names are compared without case.
- **Links**: a symbolic link to a file counts as that file. A link to a folder is not followed.
- **Extras**: a file in an extras folder or with an extras suffix (section 4.6) is stored with `kind = 'extra'` and kept out of lists. Extras are not probed in version 1.
- **External tracks**: subtitle files (`.srt .ass .ssa .vtt .sub .sup`) and audio files (`.mka .ac3 .flac .opus`) whose name starts with a video's base name are attached to that video (section 4.7).

## 3. Probing

One ffprobe child per file:

```
ffprobe -v error -show_format -show_streams -show_chapters -of json <path>
```

It took about 0.05 s per file once the file was in the cache, and the child used 54 MB for its short life. At that rate a library of 14,700 files takes about 12 minutes on one core, once. One or two probes run at a time, and they pause while a playback starts.

What is kept:

| From ffprobe | Stored as |
|---|---|
| `format.format_name`, `duration`, `bit_rate` | `files.container`, `duration_ms`, `bit_rate`. The container is ffprobe's first name for the format (`avi`, `mpegts`, `asf`, `mpeg`), except for two families that ffprobe names together: `matroska,webm` is `webm` for a `.webm` file and `matroska` otherwise, and `mov,mp4,…` is `mov` for a `.mov` file and `mp4` otherwise. |
| each stream's `index`, `codec_type`, `codec_name`, `profile` | `streams.idx`, `kind`, `codec`, `profile`. A video stream marked `attached_pic` (cover art) and streams of other types (`data`) are not kept. |
| `tags.language`, `tags.title` | `lang`, `title`, read whatever the case of the tag's name |
| `disposition.default`, `forced`, `hearing_impaired` | `is_default`, `is_forced`, `is_hearing_impaired` |
| video `width`, `height`, `pix_fmt`, `avg_frame_rate` | `width`, `height`, `pix_fmt`, `fps_num`, `fps_den`. A rate of `0/0` is unknown. |
| video bit depth, read from the pixel format | `bit_depth`: the depth of the format's first component, as the `BIT_DEPTHS` column of `ffprobe -pix_fmts` gives it, read once at the first probe |
| video `level` | `level`, unless ffprobe prints `-99`, which means it does not know. With the profile and the bit depth it gives the name a browser asks about the stream by (`05-playback.md`, section 13.3). |
| video `color_transfer` and side data | `hdr`: `smpte2084` is HDR10, `arib-std-b67` is HLG, a Dolby Vision configuration record is `dv` and wins over the transfer. HDR10+ is carried frame by frame and cannot be seen this way, so such a file reads as HDR10. |
| audio `channels`, `channel_layout`, `sample_rate`, `bit_rate` | the same |
| attachment `tags.filename`, `tags.mimetype`, `extradata_size` | `filename`, `mime`, `size`, and their sum in `files.attachments_bytes` |
| chapters | `chapters` |

- The raw JSON is not kept. `probe_version` in the code is raised when this mapping changes, and every file is probed again. It is 1.
- A file ffprobe cannot read keeps `probe_error`, ffprobe's reason without the file's path, and is not offered for playback. It is retried when its size or time changes.
- A probe that takes longer than a minute is stopped, and so is one whose server stops. Neither is an error: the file is probed again on a later pass.
- A result is stored only while the file's size and time in the database are still those that were probed. A file that changed during its probe is probed again.
- The fingerprint is computed in the same task: the size, and a hash of the first and last 64 KiB.

What ffprobe prints was checked with ffprobe 6.1 in the development sandbox, and with the version CI installs, on files made from ffmpeg's test sources. The Dolby Vision record is the one case checked only against a hand-made sample, because the test sources cannot make one.

## 4. Parsing names

The parser expects Jellyfin's documented folder layout, with episode file names in several common styles. It implements those conventions and no others.

### 4.1 Films

- A folder `Name (year) [ids]` holding the film, or a lone file `Name (year).ext` in the library root.
- The item's key is the folder, or the file when there is no folder.
- Title and year come from the folder name, or from a lone file's name. Brackets are read for ids (4.5) and otherwise dropped. The year is the last `(NNNN)` between 1880 and 2100; whatever follows it, such as a version label, is not part of the title.
- A film's name has a class, as an episode's does (4.3): **explicit** with its year, **guess** without one, and **none** when no title is left once brackets are dropped. A file in the none class makes no item.

### 4.2 Series and seasons

- A folder `Name (year) [ids]` per series. Its name gives the title and the year.
- The series folder is the top folder of the root that the file is under.
- `Season NN` folders inside it. `Season 00` and `Specials` are season 0.
- Episode files directly in the series folder belong to season 1, and so do files in a folder of it that is not a season folder.
- A file at the top of a shows library has no series folder: it is in the none class and makes no item.

### 4.3 Episodes

The folders already give the series and the season. The file name only has to yield an episode number. The forms it reads:

- `Series - S01E02`
- `[Group] Series - S01E02`
- `[Group] Series - 123`, a running count with no season tag
- any of those without the dash

The rule, in order:

1. Remove bracketed groups: `[Group]`, `[1080p]`, `(…)`.
2. If an `SxxEyy` token is present, take it, with `SxxEyy-Ezz` or `SxxEyyEzz` for a file that holds several episodes. Case does not matter, and a space, dot, dash or underscore may stand between the season and the episode. Class **explicit**. The token's season is taken; where it differs from its season folder's, the report says so.
3. Otherwise, if the name starts with the series folder's title, cut the title off, skip the folder's year if it follows, and take the first number that stands alone. A version suffix such as `05v2` is allowed. Class **anchored**.
4. Otherwise accept a number that follows ` - `. Class **dash**.
5. Otherwise take the last number that stands alone. Class **guess**. The file goes to the review list.
6. Otherwise there is no number: class **none**. The file makes no item, and is listed in the report.

An episode number has 1 to 4 digits.

Comparison is on a normalised form: lower case, every run of other characters turned into one space. So `Unit No. 8` in a folder name matches `Unit No 8` in a file name.

A Go prototype of this rule was run on 11,423 synthetic names in eight forms, including titles that contain numbers, version suffixes, quality tags outside brackets and episode titles with digits:

| Form | Names | Right |
|---|---|---|
| The six forms above, with and without group tags and dashes | 10,761 | 10,761 |
| File title differs from the folder's, with a dash | 592 | 592 |
| File title differs from the folder's, no dash | 70 | 64 |

All six errors were in the **guess** class, the one that goes to review. The names and the parser came from the same author, so this shows the rule is sound, not that it is complete. The test table should grow from real names.

Not handled in version 1: `1x02`, dates as episode numbers, numbers inside brackets, decimals such as `12.5`.

### 4.4 Versions and parts

- **Versions.** Two files that resolve to the same film or episode are versions of it. What follows the common name, after ` - `, is the label: `Film (2001) - 1080p.mkv`.
- **Parts.** `cd`, `dvd`, `part`, `pt`, `disc` or `disk`, then a number or a letter a to d, split a film into parts that play in order.

### 4.5 Ids in names

`[imdbid-tt1234567]`, `[tmdbid-12345]`, `[tvdbid-12345]` on a folder or a file settle the match without a search. Several may appear together.

### 4.6 Extras

Kept out of lists in version 1.

Both lists are Jellyfin's, as its documentation for films gives them (checked on 20 November 2026). Names are compared without case.

- Folders: `behind the scenes`, `deleted scenes`, `interviews`, `scenes`, `samples`, `shorts`, `featurettes`, `clips`, `other`, `extras`, `trailers`, `theme-music`, `backdrops`. Such a folder counts inside a film's or a series' folder, at any depth, and not as a top folder of the root, where it would be the folder of a title such as "Other".
- Suffixes at the end of a file's base name: `-trailer`, `-sample`, `-scene`, `-clip`, `-interview`, `-behindthescenes`, `-deleted`, `-deletedscene`, `-featurette`, `-short`, `-other`, `-extra`. For `trailer` and `sample` a `.`, a `_` or a space also works in place of the `-`.
- A file whose base name is `trailer` is an extra. One named `sample` is ignored (section 2.3).

### 4.7 External subtitles and audio

`Film (2001).eng.forced.srt`: after the video's base name, dot-separated fields, each one a language code, a title, or a flag. Flags are `default`, `forced` or `foreign`, and `sdh`, `cc` or `hi`.

### 4.8 Local images

`poster`, `folder`, `backdrop`, `fanart`, `logo`, `banner` in an item's folder, and `<episode file name>-thumb` next to an episode. Used before anything is fetched.

## 5. Building items

Run after every scan. It must be idempotent: a second run changes nothing and bumps no sequence, so a scan that changed nothing changes no item, and a newer parser is applied by the next scan without a file having changed.

- **Films.** One movie item per key. Its files are attached as versions or parts.
- **Series.** One series item per series folder. Season items are created for the season numbers that occur. Episode items are created per `(series, season, episode)`.
- **Titles before metadata.** A film or series takes its title and year from the folder name. An episode is titled `Episode N` until metadata arrives.
- **A renamed folder keeps its item.** When none of an item's files parse to its own key any more, and they now parse to a key that no item has, the item takes the new key and keeps its id: a folder renamed to fix a year keeps its watch state. This needs the files to have kept their ids, which takes a fingerprint (section 2.1).
- **`sort_title`** is the title in lower case without a leading article (`the`, `a`, `an`).
- **`added_at`** is the earliest `first_seen_at` of the item's files. For a series it is that of its newest episode, so a new episode brings the series to the top of Recently added.
- **Absolute numbering.** When a series has files in the anchored, dash or guess class whose numbers run past what one season holds (a number above 99, or above the provider's count for that season once known), the series is set to `numbering = 'absolute'`, the numbers are stored in `absolute_number`, and season and episode numbers wait for the mapping in section 6.5. A person can set `numbering` by hand in the match dialog.
- **Removal.** An item whose files have all been missing for 30 days is deleted, with a tombstone. Until then it is shown as unavailable. More generally, an item that no file makes any more is deleted when items are built, with a tombstone; a series takes its episodes with it, each with its own.
- **Collections** are items too, but they are built from metadata, not from files (section 6.6).
- **Sequence.** Every item written takes the transaction's sequence number. A new episode also bumps its season and its series.

## 6. Matching and metadata

### 6.1 Order

For a film or a series, stop at the first step that gives an answer:

1. **A manual match** in `overrides` for this folder. A row that names the provider `none` means the folder was left unmatched on purpose: stop here, and the item is `ignored`.
2. **The one-time import** from Jellyfin (section 7), which seeds provider ids by path.
3. **Ids in the name** (4.5), or in an `.nfo` file in the folder if there is one.
4. **Search and score** (6.2).

Episodes are never searched for. They are looked up by number inside their series' match.

### 6.2 Search and score

- Search the provider by the parsed title, with the year when there is one.
- Score each candidate from 0 to 1: similarity of the normalised titles, counting the provider's alternative titles; plus the year matching exactly or within one; plus the candidate being the right kind.
- **Accept** when the best score is at least 0.90 and at least 0.10 ahead of the second. The item becomes `matched`.
- **Otherwise** the item becomes `review`, with the candidates kept for the dialog.
- **Why** is written to `match_note` for the review list: no results, a best score that is too low, candidates too close together, or a running count that could not be mapped, with the number of candidates and the best score.
- No result at all leaves it `unmatched`.

The thresholds are starting values. Tune them on a real library, against what Jellyfin matched there.

### 6.3 What is fetched

| For | Request | Kept |
|---|---|---|
| A film | One details request with external ids, images and certifications appended | Title, original title, overview, date, runtime, rating, certification, genres, ids, image paths |
| A series | One details request with the same appended, plus alternative titles | The same, plus alternative titles for the search index |
| A season | One request per season | Every episode's title, overview, air date, runtime and still |
| Credits | Appended to the film's or the series' details request, at no extra request | For each person kept: the provider's id, name, what they are known for, the path of their photo, and the character or the job. For a series, the number of episodes. |
| A collection | One request, when a library first holds two of its films (6.6) | Title, overview, poster and backdrop paths |

- Every response is stored in `provider_cache`. Changing how a response is read never fetches it again.
- **Refresh**: a series that is still airing, weekly. Everything else, every 30 days, and on request. Mind the provider's terms on how long data may be kept.
- At most two provider requests run at once.
- **Credits kept**: the first 20 of the cast in billing order, then the directors, then the writers, with a series' creators first among the writers (P7). An item's credits are replaced as a whole on every fetch.
- **No photo is downloaded here.** A person's photo is fetched when a client first asks for it (section 8).

### 6.4 Providers

| Provider | Use | Notes |
|---|---|---|
| TMDB | Primary, for films, series, episodes and artwork | Needs a free API key from whoever runs the server, set in the web interface or as `RUNNEL_TMDB_KEY` (D34). A new key is checked with one request before it is kept. Non-commercial use with attribution. The client's About screen must carry TMDB's notice. |
| Anime-Lists | Mapping between AniDB entries and TMDB or TVDB seasons, with episode offsets | An XML file on GitHub. Fetched at most once a day. |
| anime-offline-database | Titles and synonyms, for matching romanised release names without a network call | A JSON file, updated weekly, under the ODbL. |
| TheTVDB, AniDB, AniList, TVmaze | Not called in version 1 | Licensing or rate limits; see the research notes, section 7.1. AniDB and AniList ids are still stored when a mapping supplies them. |

TMDB requests, from memory and to be confirmed against its documentation when coding: `/search/movie`, `/search/tv`, `/movie/{id}`, `/tv/{id}`, `/tv/{id}/season/{n}`, `/find/{external id}`, with `append_to_response` for ids, images and credits, `/collection/{id}`, and `/configuration` for the image base address and sizes. A series' credits with episode counts are a separate list there (`aggregate_credits`).

### 6.5 Libraries with the anime profile

The databases disagree about what a series is. AniDB and AniList treat each season, film and OVA as its own entry, numbered from 1. TMDB and TVDB have one show with seasons, and do not always agree with each other. Release names add a third convention, one running count.

- **The folder is the unit.** One series folder is one match.
- **Titles are matched offline first**, against anime-offline-database's synonyms, then through Anime-Lists to a TMDB series. Only if that fails is TMDB searched by name.
- **Episode data comes from TMDB.** An explicit `SxxEyy` is taken as it stands, plus any offset from `overrides`.
- **A running count is mapped by counting.** Episode N of an absolute-numbered series is the Nth episode of the TMDB series in season order, specials left out. Where Anime-Lists gives an offset for the entry, it is applied first.
- **What cannot be mapped stays playable**, titled by its number, and listed for review.

Chapter names can mark openings and endings for a skip button. Many files name their chapters only `Chapter 01`, `Chapter 02` and so on, so this will not work everywhere.

### 6.6 Collections

A collection is a set of films the provider groups together. It is shown only when a library holds two or more of its films (D33).

- A film's details name the collection it belongs to, if any. That goes on the film as an `external_ids` row with provider `tmdb_collection`, whether or not a collection is shown.
- After a film is matched, and after a film is removed, the server counts the library's films for that collection. At two, it makes the collection item, fetches the collection's details and artwork, and points the films at it. Below two, it deletes the item and clears the pointer on the film that is left.
- A collection is never built from a folder, and a library's listing does not include collections. They are reached from their films.
- The rules that keep the rows consistent are in `03-data-model.md`, section 4.

### 6.7 Leaving unmatched, and the preview

- **Leaving an item unmatched** writes an `overrides` row with provider `none`. Matching skips that folder from then on, the item keeps the title from its name, and it leaves the review list. Forgetting the row puts it back.
- **The preview of a series match** (`PUT /admin/items/{id}/match?dry_run=1`) answers how the folder's files would map before anything is saved. It fetches the provider's seasons into the cache, which is allowed in a dry run because the cache is not part of the library. It applies the numbering and the offsets to the files in memory, and returns how many map and a few samples: the first file, the first file of each later season, the last file.

## 7. Importing from Jellyfin, once

A library that Jellyfin has already matched can be imported. That is quicker and more accurate than matching again.

1. `POST /admin/import/jellyfin` with the Jellyfin address, an API key, a map from Jellyfin users to local users, and path prefixes to rewrite when Jellyfin runs in a container:

   ```json
   {
     "url": "http://jellyfin.lan:8096",
     "api_key": "…",
     "users": {"ada": "ada"},
     "paths": [{"from": "/media", "to": "/srv/media"}],
     "dry_run": true
   }
   ```

2. The server lists Jellyfin's films, series and episodes with their paths, provider ids and each user's play state.
3. Each is joined to a local item by path.
4. For films and series: provider ids are written to `external_ids`, and the item becomes `matched`.
5. For films and episodes, per user: played, play count, last played and position are written to `user_state`.
6. A report says what matched, what did not, and what Jellyfin had that the server does not.

- `dry_run` does everything except the writes. Run that first.
- The import is safe to repeat. It never overwrites a manual match, and it never lowers a play state that is newer locally.
- Jellyfin's item listing with paths and user data is known from versions before 12. Check it against 12 before relying on it.
- Before the dry run, `POST /admin/import/jellyfin/users` lists Jellyfin's users, for the form that maps them to people here.
- One import runs at a time. Its report is kept in the data directory, replaced by the next run. `GET /admin/import/jellyfin` returns the summary and the first 100 rows that did not join, and every row with `full=1` (`04-api.md`, section 8.5).

## 8. Artwork

| Kind | For | Width kept |
|---|---|---|
| `poster` | films, series, seasons | 342, and 500 for the detail screen |
| `backdrop` | films, series | 1280 |
| `thumb` | episodes | 300 |
| `logo` | films, series | 500 |
| a person's photo | cast and crew | One width for cards and one for the person page, chosen from the provider's sizes when stage 4 is specified |

- **Local images first** (4.8). They are scaled to the widths above by an ffmpeg child, once.
- **Provider images** are downloaded at the provider's own sizes nearest to those widths. The server never decodes a large image in its own process.
- **Key.** 32 hex characters derived from the image's source: the provider and its image path, or a local file's fingerprint. A changed image has a new key, so responses can be cached forever.
- **On disk**: `images/<first two characters>/<key>_<width>.jpg` under the data directory.
- **Placeholder colour**: the average colour of the smallest stored width, computed once.
- **Order of work**: posters and backdrops for films and series first. Episode thumbs are fetched when their season is first opened, and in the background at the lowest priority.
- A library of 3,000 films and 240 series comes to roughly 3,500 posters and backdrops, a few hundred megabytes at most.
- **People's photos arrive on first request** (D33). A film can credit dozens of people, so matching a library downloads none of them. The key is derived from the photo's path when the credits are stored, so the address exists at once. `GET /img/{key}/{w}` for a key with no file on disk finds the person, downloads the provider's nearest size, stores it, works out the colour and answers. A photo the provider cannot give is `404`, and is not tried again for a day.
- **Episode stills, to check when stage 4 is specified**: how wide the provider's stills are. The TV's episode rows are 400 px wide. If the largest fixed size is narrower, the original is fetched and scaled once by an ffmpeg child (`11-design-review.md`, U5).

## 9. Background work

One queue in the `tasks` table, so work survives a restart.

| Kind | Runs | At once |
|---|---|---|
| `probe` | after a scan, per new or changed file | 2 |
| `match` | after items are built, per unmatched film or series | 2, sharing the provider limit |
| `fetch` | after a match, and on refresh | 2, sharing the provider limit |
| `image` | after a fetch | 2 |
| `sidecar` | when a transcode starts | 1 |
| `import` | on request | 1 |
| `extract` | when a browser first asks for a file's subtitles or fonts, or a `prepare` says so | 1 |

- A failed task is retried with a growing delay, five times, then left as failed for `GET /admin/tasks`.
- Everything here pauses while a playback session is starting.
- A provider that answers 429 pauses its queue for the time it asks.
- A task carries the library it works for. Finished tasks stay until their kind and library have nothing queued or running, and are then deleted together. `GET /admin/tasks` counts progress from that: done out of all (`03-data-model.md`, section 4).
- An admin can cancel what is queued and retry what failed. Cancelled work is found again by the next scan or refresh.
- A scan is not a task. The scanner reports its own state: walking, then probing with done out of total, which are its library's `probe` tasks.
