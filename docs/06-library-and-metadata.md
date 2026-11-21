# Library and metadata

How files on disk become items a person can browse: scanning, probing, parsing names, building items, matching them to a metadata provider and fetching artwork. Tables and columns refer to `03-data-model.md`. Evidence is in the project's research notes, which are not published.

**Revised, 20 November 2026,** for what the client designs added (D33, D34, `11-design-review.md`): collections, cast and crew, photos fetched when first asked for, why an item is on the review list, leaving an item unmatched, the preview of a series match, the import's report, and progress of background work. Nothing here has been run against a provider yet. Revised on 21 November 2026 for shapes of names that a review of stage 1 found with invented names (sections 4.2 to 4.4), checked against Jellyfin's documentation and its naming code.

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
6. **Write** all of it in one transaction, then queue a probe for each of the root's files whose probe is missing or older than the code's: the new and changed ones, and any still waiting. The server does the same for every root at start.
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
  - A scan of some folders walks them and what they hold, and compares only what is known under them, so a file outside them is neither missing nor moved. A file that moved between two of them keeps its id, as in a scan of the whole root. A folder that is gone holds nothing: what was known under it is missing, unless it moved.
  - Folders that a scan leaves out (2.3) are not watched, and events for the files it leaves out are dropped.
  - Reading a file reports nothing, so the server's own probes and streams set off no scan [checked in the development sandbox: Linux 6.18, ext4 with `relatime`].
- **Nightly**, a full walk as a safety net, at 04:00 in the server's local time, and at start.
- **On request**: `POST /admin/scan`, and when a library is created.
- **A file still being written** is left alone: one modified less than 10 seconds before the scan. Its folder is scanned again once the file is 10 seconds old. Every write moves a file's time, so one look at it does what two looks at its size, 10 seconds apart, would do. A new file is then playable about 10 seconds after it was written, inside the 15 seconds of stage 2's exit criterion: 10.6 s in the development sandbox, with the test of that criterion. A time more than 2 seconds in the future does not count as being written.
- **A root that cannot be watched** is scanned every 15 minutes, and the server logs why, once. That is a root that is away; one on a network file system, where changes made by another machine are not reported (NFS, SMB, 9p, Ceph, GlusterFS, and FUSE file systems that reach another machine, such as sshfs and rclone); and one past the system's limit of watches, `fs.inotify.max_user_watches`. Once a scan finds it there and watchable, it is watched again. A local FUSE file system, such as a union of disks, is watched. Untested: whether such a union reports changes made on its disks directly. The nightly walk finds them in any case.

### 2.3 What counts

- **Video files**: `.mkv .mp4 .m4v .mov .avi .ts .m2ts .webm .wmv .mpg .mpeg`.
- **Ignored**: names that start with a dot, `@eaDir`, `lost+found`, anything ending in `.part`, `.partial`, `.!qB` or `.tmp`, and files named `sample`. Names are compared without case.
- **Links**: a symbolic link to a file counts as that file. A link to a folder is not followed.
- **Extras**: a file in an extras folder or with an extras suffix (section 4.6) is stored with `kind = 'extra'` and kept out of lists. Extras are not probed in version 1.
- **External tracks**: subtitle files (`.srt .ass .ssa .vtt .sub .sup`) and audio files (`.mka .ac3 .flac .opus .dts .mp3 .aac`) in a video's folder, whose name starts with the video's base name, are attached to that video (section 4.7). Jellyfin's documentation shows `.dts`, `.mp3` and `.aac` among its examples [checked: its page on external subtitles and audio tracks, 21 November 2026].

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
- A probe that takes longer than a minute is stopped, and tried again later as a failed task is (section 9). One whose server stops is queued again at the next start, and that run does not count.
- When ffprobe cannot be run at all, probing waits a minute and tries again, and the server logs it once. Those runs do not count either.
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
- Season folders inside it: `Season` and a number of 1 to 4 digits, `Season 01`, `Season 1` or `Season 2019`, as Jellyfin's documentation names them. Case does not matter, and a space, dot, dash or underscore may stand between the word and the number. `Season 00` and `Specials` are season 0.
- Jellyfin's documentation says not to shorten `Season` to `S01` [checked: its pages on shows, 21 November 2026]. Its naming code is more lenient: version 12.4 also reads `S01`, a season folder with more after its number, and the word for season in other languages [checked: its `SeasonPathParser`]. Runnel reads the documented form only.
- Episode files directly in the series folder belong to season 1, and so do files in a folder of it that is not a season folder. Such a folder gives the file a note in the parse report, which names it: otherwise a second season in `S02` would land on the first one's numbers with nobody seeing it.
- A file at the top of a shows library has no series folder: it is in the none class and makes no item.

### 4.3 Episodes

The folders already give the series and the season. The file name only has to yield an episode number. The forms it reads:

- `Series - S01E02`
- `[Group] Series - S01E02`
- `[Group] Series - 123`, a running count with no season tag
- `Series - S01E02-E03`, a file that holds several episodes
- `Series - E05` and `Series - EP05`, which Jellyfin's naming code reads as episode 5 [checked: `NamingOptions` in version 12.4]
- any of those without the dash, and with underscores between the words

The rule, in order:

1. Remove bracketed groups: `[Group]`, `[1080p]`, `(…)`.
2. If an `SxxEyy` token is present, take it. It starts the name or follows a character that is not a letter or a digit, so that an underscore separates it as a space does: `Series_S01E02`. Case does not matter, and up to three of space, dot, dash or underscore may stand between the season and the episode. Class **explicit**. The token's season is taken; where it differs from its season folder's, the report says so.
   - A file that holds several episodes ends its token with the last one. Jellyfin's documentation shows `S01E02-E03` [checked: its pages on shows, 21 November 2026]. Its naming code also reads `S01E02E03`, `S01E02 - E03` and `S01E02-03` [checked: `NamingOptions` in version 12.4]. Runnel reads those, and `S01E02-S01E03`, the token repeated with its own season, which Jellyfin's code does not read.
   - The ends may repeat: `S01E02E03E04` holds three. As in Jellyfin's code, a number that runs on into a digit, a `p` or an `i` is not an end, so `S01E14-1080p` is episode 14 alone. An end must come after the start, and `-S02E03` after `S01E02` names another season and is not an end.
3. Otherwise, if the name starts with the series folder's title, cut the title off, skip the folder's year if it follows, and take the first number that stands alone. A version suffix such as `05v2` is allowed, and so is `E` or `EP` before the number. Class **anchored**.
   - Where a number follows ` - ` after the title and differs from that first number, the first one is not trusted. If it is the season folder's own number, as `2` in `[Group] Series 2 - 05` inside `Season 02`, the number after the dash is the episode, still **anchored**. Otherwise the number after the dash is taken, class **guess**, and the report says that the two differ.
4. Otherwise accept a number that follows ` - ` (or `_-_`, the dash between underscores), perhaps after `E` or `EP`. Class **dash**.
5. Otherwise take the last number that stands alone. Class **guess**. The file goes to the review list.
6. Otherwise there is no number: class **none**. The file makes no item, and is listed in the report.

An episode number has 1 to 4 digits.

**Decimals are not read.** Where the number a step takes is part of a decimal, such as `12.5`, the file is in the none class with a note. Read as 12, in any class, it would join episode 12 as a second file. A dot between two numbers in a name whose words are joined by dots is not a decimal point: `quiet.station.12.5` gives 12.

Comparison is on a normalised form: lower case, every run of other characters turned into one space. So `Unit No. 8` in a folder name matches `Unit No 8` in a file name.

A Go prototype of this rule was run on 11,423 synthetic names in eight forms, including titles that contain numbers, version suffixes, quality tags outside brackets and episode titles with digits:

| Form | Names | Right |
|---|---|---|
| The first three forms above, with and without group tags and dashes | 10,761 | 10,761 |
| File title differs from the folder's, with a dash | 592 | 592 |
| File title differs from the folder's, no dash | 70 | 64 |

All six errors were in the **guess** class, the one that goes to review. The names and the parser came from the same author, so this shows the rule is sound, not that it is complete. The prototype ran the rule before the additions of 21 November 2026: underscores, the forms of a file that holds several episodes, `E` and `EP`, the second number of step 3 and decimals.

The test table, `internal/parse/testdata/names.tsv`, holds every form here and every shape found to parse wrongly. It grows from real names, each added with its title replaced by an invented one of the same shape.

Not handled in version 1: `1x02`, dates as episode numbers, numbers inside brackets, and decimals, which are in the none class (above).

### 4.4 Versions and parts

- **Versions.** Two files that resolve to the same film or episode are versions of it. What follows the common name is the label: `Film (2001) - 1080p.mkv`, `Series S01E01 - 720p.mkv`. In Jellyfin's documentation the label follows a film's folder name after a dash, a dot or an underscore, or stands in square brackets, and an episode's versions are named the same way after the episode [checked: its pages on films and shows, 21 November 2026].
- **Parts.** At the end of a name, after a space, a dot, a dash or an underscore, or straight after a closing bracket: `cd`, `dvd`, `part`, `pt`, `disc` or `disk`, perhaps in brackets, then a number or a letter a to d. Parts split a film or an episode into files that play in order: `Film (2001) - cd1.mkv`, `Series S02E03 Part 1.mkv`. That is Jellyfin's rule for files that split a video, in its documentation and its naming code (version 12.4). A part counts only where another file of the same item has the same name before its part: alone, `Part 2` is as likely the end of a title.
- **Labels.** A film's version whose file name begins with the film's folder name takes as its label what follows that name, without the characters other than letters and digits at its ends: `1080p` for `Film (2001) - 1080p.mkv`. Other versions, such as an episode's, take what follows the beginning that the versions' names share. The version whose name has nothing more is the main one and has no label. Where two versions end up with one label, such as one name with two extensions, their extension is added to it, and then the name of the folder they are in. The parts of one version share its label.
- **Where.** Versions and parts are the files of one film or of one episode. A lone file at the top of a films library is a film by itself, since its key is the file (4.1): two of them are two films.
- **The note.** The names of the files that make one item should tell them apart, each by its label or by its part. Every file of an item gets a note in the parse report when two of the item's files have names that differ in nothing, such as one name in two folders or with two extensions, or differ only by a number, such as `Series 2 - 05` and `Series 2 - 06`. Those files are more likely episodes that parsed to one number than versions of one. What is compared is what is left of each name once the part and the beginning and the end that all the names share are taken off.

### 4.5 Ids in names

`[imdbid-tt1234567]`, `[tmdbid-12345]`, `[tvdbid-12345]` on a folder or a file settle the match without a search. Several may appear together.

### 4.6 Extras

Kept out of lists in version 1.

Both lists are Jellyfin's, as its documentation for films gives them (checked on 20 November 2026). Names are compared without case.

- Folders: `behind the scenes`, `deleted scenes`, `interviews`, `scenes`, `samples`, `shorts`, `featurettes`, `clips`, `other`, `extras`, `trailers`, `theme-music`, `backdrops`. Such a folder counts inside a film's or a series' folder, at any depth, and not as a top folder of the root, where it would be the folder of a title such as "Other".
- Suffixes at the end of a file's base name: `-trailer`, `-sample`, `-scene`, `-clip`, `-interview`, `-behindthescenes`, `-deleted`, `-deletedscene`, `-featurette`, `-short`, `-other`, `-extra`. For `trailer` and `sample` a `.`, a `_` or a space also works in place of the `-`.
- A file whose base name is `trailer` is an extra. One named `sample` is ignored (section 2.3).

### 4.7 External subtitles and audio

`Film (2001).eng.forced.srt`: after the video's base name, dot-separated fields, each one a language code, a title, or a flag. Flags are `default`, `forced` or `foreign`, and `sdh`, `cc` or `hi`. This is Jellyfin's documented form [checked: its page on external subtitles and audio tracks, 21 November 2026].

- The name is the video's base name, then a dot or the extension. A track whose name begins with the base names of two videos in its folder, such as `Film (2001) - 720p.eng.srt` beside `Film (2001).mkv` and `Film (2001) - 720p.mkv`, is the track of the longer. Two videos with one base name, such as one name with two extensions, both have it.
- A language is a code of two letters (ISO 639-1) or three (ISO 639-2). It is kept in three, as the bibliographic code where ISO 639-2 has two, as Matroska keeps it: `en` and `eng` are `eng`, `de` and `deu` are `ger`. The first language in a name counts, and another is part of the title. The table is that of the iso-codes project, version 4.16.0.
- `hi` alone is Hindi. Beside another language it means hearing impaired, as Jellyfin's documentation says.
- What is neither a language nor a flag is the title, its fields joined with spaces: `Film.English Commentary.en.mp3` is English audio titled `English Commentary`.
- A track file is not probed. Its codec is what its extension holds, by ffprobe's name for it: `.srt` subrip, `.ass` and `.ssa` ass, `.vtt` webvtt, `.ac3`, `.flac`, `.opus`, `.dts`, `.mp3` and `.aac` their own [checked: ffprobe 6.1 in the development sandbox, on files made with ffmpeg and a hand-written SSA file]. A `.sub`, which can be MicroDVD text or VobSub pictures, a `.sup` and an `.mka` have no codec, and no audio track file has a channel count.
- A scan of a folder makes the tracks of its videos what it finds there. A track that stays keeps its id, which is its `external_id`. A track that comes, goes or changes is a change to its video's items (`03-data-model.md`, section 5). The tracks of a folder that could not be read stay as they were.

### 4.8 Local images

`poster`, `folder`, `backdrop`, `fanart`, `logo`, `banner` in an item's folder, and `<episode file name>-thumb` next to an episode. Used before anything is fetched.

## 5. Building items

Run after every scan. It must be idempotent: a second run changes nothing and bumps no sequence, so a scan that changed nothing changes no item, and a newer parser is applied by the next scan without a file having changed.

- **Films.** One movie item per key. Its files are attached as versions or parts.
- **Series.** One series item per series folder. Season items are created for the season numbers that occur, and a season goes when its last episode does. Episode items are created per episode key: `(series, season, episode)`, or `(series, absolute_number)` in a series numbered in one running count (below).
- **Titles before metadata.** A film or series takes its title and year from the folder name. An episode is titled `Episode N` until metadata arrives, and `Episodes N–M` when its file holds several. A season is titled `Season N`, and season 0 `Specials`.
- **A renamed folder keeps its item.** When none of an item's files parse to its own key any more, and they now parse to a key that no item has, the item takes the new key and keeps its id: a folder renamed to fix a year keeps its watch state. This needs the files to have kept their ids, which takes a fingerprint (section 2.1).
- **An episode follows its files.** An episode keeps its id while its key is wanted. When its key is not wanted any more, it goes with its files to the key they make now, if no episode has that key: a file renamed to correct its number, or to another series' folder, keeps its episode and the watch state on it, and so does a series that changes its numbering. When an episode's files now make several episodes, it goes with the one that holds its oldest file, the one with the lowest id. A series follows its episodes' files in the same way, and a film its files. Where the new key already has an episode, the files join it as versions, and the episode they left goes when no file makes it. Like a renamed folder, this needs the files to have kept their ids.
- **Provisional numbers.** Until a running count is mapped to the provider's seasons (6.5), its episodes are shown in season 1, or in their season folder's season, numbered by their running number. The mapping changes their season and episode numbers and not their key, so they keep their ids. A file with an `SxxEyy` token, or in the specials' folder, keeps its season and number in a running count, and does not decide whether the series has one.
- **`sort_title`** is the title in lower case without a leading article (`the`, `a`, `an`).
- **`added_at`** is the earliest `first_seen_at` of the item's files. For a series it is that of its newest episode, so a new episode brings the series to the top of Recently added.
- **Absolute numbering.** When a series has files in the anchored, dash or guess class, outside the specials, whose numbers run past what one season holds (a number above 99, or above the provider's count for that season once known), the series is set to `numbering = 'absolute'`, the numbers are stored in `absolute_number`, and season and episode numbers wait for the mapping in section 6.5. A person can set `numbering` by hand in the match dialog.
- **Removal.** An item whose files have all been missing for 30 days is deleted, with a tombstone. Until then it is shown as unavailable. More generally, an item that no file makes any more is deleted when items are built, with a tombstone; a series takes its episodes with it, each with its own.
- **Collections** are items too, but they are built from metadata, not from files (section 6.6).
- **Sequence.** Every item written takes a sequence value of its transaction, which takes one value for every 500 rows (`03-data-model.md`, section 5). An episode that comes, goes or moves also bumps its season and its series, because their counts changed.

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
| `probe` | after a scan, per new or changed file, and at start for any file still to probe | 2 |
| `match` | after items are built, per unmatched film or series | 2, sharing the provider limit |
| `fetch` | after a match, and on refresh | 2, sharing the provider limit |
| `image` | after a fetch | 2 |
| `sidecar` | when a transcode starts | 1 |
| `import` | on request | 1 |
| `extract` | when a browser first asks for a file's subtitles or fonts, or a `prepare` says so | 1 |

- A failed task is retried five times, after 1, 2, 4, 8 and 16 minutes, then left as failed for `GET /admin/tasks`. A probe of a file that ffprobe cannot read has not failed: it is done, and the file keeps the reason (section 3).
- A run that the server's stop cut short is queued again at the next start, and is not counted. Neither is a run that could not work for a reason that is not the task's own, such as an ffprobe that cannot be run: its whole kind waits, and then tries again.
- Everything here pauses while a playback session is starting (the playback gate, `02-architecture.md`, section 5).
- A provider that answers 429 pauses its queue for the time it asks.
- A task carries the library it works for. Finished tasks stay until their kind and library have nothing queued or running, and are then deleted together. `GET /admin/tasks` counts progress from that: done out of all (`03-data-model.md`, section 4).
- An admin can cancel what is queued and retry what failed. Cancelled work is found again by the next scan or refresh.
- A scan is not a task. The scanner reports its own state: walking, then probing with done out of total, which are its library's `probe` tasks.
