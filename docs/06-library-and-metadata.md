# Library and metadata

How files on disk become items a person can browse: scanning, probing, parsing names, building items, matching them to a metadata provider and fetching artwork. Tables and columns refer to `03-data-model.md`. Evidence is in the project's research notes, which are not published.

**Revised, 20 November 2026,** for what the client designs added (D33, D34, `11-design-review.md`): collections, cast and crew, photos fetched when first asked for, why an item is on the review list, leaving an item unmatched, the preview of a series match, the import's report, and progress of background work. Nothing here has been run against a provider yet. Revised on 21 November 2026 for shapes of names that a review of stage 1 found with invented names (sections 4.2 to 4.4), checked against Jellyfin's documentation and its naming code. Revised on 27 November 2026, as stage 4 begins: a folder's own numbering, and episodes without a number (D51), in sections 4.3 and 5; `.nfo` files out of section 6.1. Revised again the same day for grouping folders and mixed libraries (D49, D52): sections 4.9 and 4.10, which sections 4.1 to 4.3 and 4.6 refer to, and what keeps its id through a change of kind, in section 5; and for builds of only what a scan looked at, in sections 2.2 and 5. Revised again the same day: ids in names, as Jellyfin reads them, in sections 4.5 and 6.1, where they now come before the import, since every build writes them; what metadata writes onto items, and how the build leaves their titles once it has, in sections 5 and 6.3; and fetching by id, as TMDB's reference gives its requests, in sections 6.2 to 6.4 and 9. Revised on 28 November 2026: artwork from TMDB, and the widths settled against the designs, in section 8; and collections, as they are built, in sections 6.3, 6.4 and 6.6; and credits, as they are built, in sections 6.3 and 6.4; and people's photos, fetched on first request, in section 8, with which image sizes are asked for in section 6.4; and search and score, as they are built, in sections 6.2, 6.4 and 9.

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

- **On change.** The server watches every folder under each root with inotify. Events are collected for 5 seconds, then the affected folders are scanned, and the items of their top folders built (section 5). This keeps disks asleep when nothing changes.
  - A scan of some folders walks them and what they hold, and compares only what is known under them, so a file outside them is neither missing nor moved. A file that moved between two of them keeps its id, as in a scan of the whole root. A folder that is gone holds nothing: what was known under it is missing, unless it moved.
  - Folders that a scan leaves out (2.3) are not watched, and events for the files it leaves out are dropped.
  - Reading a file reports nothing, so the server's own probes and streams set off no scan [checked in the development sandbox: Linux 6.18, ext4 with `relatime`].
- **Nightly**, a full walk as a safety net, at 04:00 in the server's local time, and at start. The pruning of old delete markers runs just before it (`03-data-model.md`, section 5).
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

- A folder `Name (year) [ids]` holding the film, at the top of the root or below grouping folders (4.9), or a lone file `Name (year).ext` in the library root.
- The item's key is the folder, as its path below the root with the grouping folders above it, or the file when there is no folder.
- Title and year come from the folder name, or from a lone file's name. Brackets are read for ids (4.5) and otherwise dropped. The year is the last `(NNNN)` between 1880 and 2100; whatever follows it, such as a version label, is not part of the title.
- A film's name has a class, as an episode's does (4.3): **explicit** with its year, **guess** without one, and **none** when no title is left once brackets are dropped. A file in the none class makes no item.

### 4.2 Series and seasons

- A folder `Name (year) [ids]` per series. Its name gives the title and the year.
- The series folder is the top folder of the root that the file is under, or the folder below the grouping folders that the file is in (4.9). Its path below the root is the series' key.
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
6. Otherwise there is no number: class **unnumbered**. The file is an episode without a number (D51, below), and is listed in the report.

A file makes no item, class **none**, only where it has no series: at the top of a shows library, at the top of a mixed library with an `SxxEyy` token in its name (4.10), or in a series folder whose name leaves no title.

An episode number has 1 to 4 digits.

**A folder's own numbering.** The steps above read one name at a time. A folder's videos are then read together: where every video in a folder begins its name, once bracketed groups are gone, with a number before a dash between spaces or underscores (`017 - The Kite`, `017_-_The Kite`), perhaps after `E` or `EP` and with a version suffix as in step 3, and no two of those numbers are the same, that number is the episode's, class **anchored**, for each name the steps above would only guess. Alone, `017 - Paper Comets - The Kite.mkv` is a guess at 17, and `003 - Help Find Their 2 Lost Bears.mkv` a wrong guess at 2. A name that gives a token, the series' title and a number, or a number after a dash keeps what it gives. A name that begins with a decimal, `08.5 - Recap.mkv`, follows the folder's numbering, takes no part in the comparison, and keeps what its name gives (below). The series' year at the start of a name, `2005 - Paper Comets.mkv` in `Paper Comets (2005)`, is not such a number. Each folder is read by itself, and the folder's notes stay.

**Decimals are not read as numbers.** Where the number a step takes is part of a decimal, such as `12.5`, the file is in the unnumbered class with a note, and the decimal places it right after episode 12 (D51). Read as 12, in any class, it would join episode 12 as a second file. A dot between two numbers in a name whose words are joined by dots is not a decimal point: `quiet.station.12.5` gives 12.

**Episodes without a number** (D51). A file of the unnumbered class is an episode by itself: its file is its key, so it has no versions or parts, and it follows its file through a rename as any episode does (section 5). It sits in the season its folder names. Without a season folder, a decimal stays in season 1 with the numbered episodes it falls between, and any other goes among the specials. It is titled from its name, with bracketed groups dropped and underscores read as spaces: `[Grp]Copper Tower_-_08.5_(1080p).mkv` is `Copper Tower - 08.5`, and `[Grp] Quiet Station S1 - S02.mkv` in `Season 00` is a special titled `Quiet Station S1 - S02`. Which files are extras stays section 4.6's lists, so an opening without credits in a season folder is such an episode.

Comparison is on a normalised form: lower case, every run of other characters turned into one space. So `Unit No. 8` in a folder name matches `Unit No 8` in a file name.

A Go prototype of this rule was run on 11,423 synthetic names in eight forms, including titles that contain numbers, version suffixes, quality tags outside brackets and episode titles with digits:

| Form | Names | Right |
|---|---|---|
| The first three forms above, with and without group tags and dashes | 10,761 | 10,761 |
| File title differs from the folder's, with a dash | 592 | 592 |
| File title differs from the folder's, no dash | 70 | 64 |

All six errors were in the **guess** class, the one that goes to review. The names and the parser came from the same author, so this shows the rule is sound, not that it is complete. The prototype ran the rule before the additions of 21 November 2026: underscores, the forms of a file that holds several episodes, `E` and `EP`, the second number of step 3 and decimals.

The test table, `internal/parse/testdata/names.tsv`, holds every form here and every shape found to parse wrongly. It grows from real names, each added with its title replaced by an invented one of the same shape. A folder's own numbering reads several names together, and has a test of its own beside the table.

Not handled in version 1: `1x02`, dates as episode numbers and numbers inside brackets, which give no number, and decimals: such files are episodes without a number (above).

### 4.4 Versions and parts

- **Versions.** Two files that resolve to the same film or episode are versions of it. What follows the common name is the label: `Film (2001) - 1080p.mkv`, `Series S01E01 - 720p.mkv`. In Jellyfin's documentation the label follows a film's folder name after a dash, a dot or an underscore, or stands in square brackets, and an episode's versions are named the same way after the episode [checked: its pages on films and shows, 21 November 2026].
- **Parts.** At the end of a name, after a space, a dot, a dash or an underscore, or straight after a closing bracket: `cd`, `dvd`, `part`, `pt`, `disc` or `disk`, perhaps in brackets, then a number or a letter a to d. Parts split a film or an episode into files that play in order: `Film (2001) - cd1.mkv`, `Series S02E03 Part 1.mkv`. That is Jellyfin's rule for files that split a video, in its documentation and its naming code (version 12.4). A part counts only where another file of the same item has the same name before its part: alone, `Part 2` is as likely the end of a title.
- **Labels.** A film's version whose file name begins with the film's folder name takes as its label what follows that name, without the characters other than letters and digits at its ends: `1080p` for `Film (2001) - 1080p.mkv`. Other versions, such as an episode's, take what follows the beginning that the versions' names share. The version whose name has nothing more is the main one and has no label. Where two versions end up with one label, such as one name with two extensions, their extension is added to it, and then the name of the folder they are in. The parts of one version share its label.
- **Where.** Versions and parts are the files of one film or of one episode. A lone file at the top of a films library is a film by itself, since its key is the file (4.1): two of them are two films.
- **The note.** The names of the files that make one item should tell them apart, each by its label or by its part. Every file of an item gets a note in the parse report when two of the item's files have names that differ in nothing, such as one name in two folders or with two extensions, or differ only by a number, such as `Series 2 - 05` and `Series 2 - 06`. Those files are more likely episodes that parsed to one number than versions of one. What is compared is what is left of each name once the part and the beginning and the end that all the names share are taken off.

### 4.5 Ids in names

`[imdbid-tt1234567]`, `[tmdbid-12345]`, `[tvdbid-12345]` on a folder or a file settle the match without a search. Several may appear together.

- **The form**, as Jellyfin reads it: in square brackets, the provider's name, a dash or an equals sign, and the value up to the closing bracket, with spaces around the value dropped and case ignored, so `[TMDBID=12345]` counts too. Curly or round brackets do not [checked: its page on films, and `PathExtensions.GetAttributeValue` in its source on a mirror of its main branch, 27 November 2026]. A TMDB or TheTVDB id is a number, an IMDb id `tt` and a number; a value of another form is left out, and where a name gives a provider twice, the first counts. Jellyfin also finds an IMDb id anywhere in a name without brackets; Runnel does not.
- **Where.** A film's ids are those of its folder's name, and then of its files' names, in file order, for any provider the folder does not give; a lone file's are its own name's. A series' are its folder's. Season folders and episode files may carry ids too, in Jellyfin's documentation, but they name a season or an episode, and are not read.
- **What they do.** Each build writes them into `external_ids` and makes the item `matched` (section 6.1), unless it was matched by hand or left unmatched on purpose. Ids that only add to the item's are added. Ids that differ from the item's name another title: they replace every id the item has, and what was fetched for the old title is fetched again for the new one. A name that loses its ids leaves the item's as they are: they are its match.

### 4.6 Extras

Kept out of lists in version 1.

Both lists are Jellyfin's, as its documentation for films gives them (checked on 20 November 2026). Names are compared without case.

- Folders: `behind the scenes`, `deleted scenes`, `interviews`, `scenes`, `samples`, `shorts`, `featurettes`, `clips`, `other`, `extras`, `trailers`, `theme-music`, `backdrops`. Such a folder counts inside a film's or a series' folder, at any depth, and not as a top folder of the root, where it would be the folder of a title such as "Other". The scanner tells extras before items are built, by the path alone, so a folder of these names inside a grouping folder (4.9) holds extras too: `films/Trailers/…` holds trailers, and makes no item, and so does `anime/Shorts/…`, though the shorts there may be titles. A folder of extras alone does not count when grouping folders are read.
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

Image files in an item's folder, as Jellyfin's documentation names them [checked: its page on metadata images, 21 November 2026]. Used before anything is fetched.

| Kind | Names, in the order preferred | Taken by |
|---|---|---|
| `poster` | `poster`, `cover`, `folder`, `default`, `movie`, `show` | films, series, seasons |
| `backdrop` | `backdrop`, `fanart`, `background`, `art` | films, series |
| `logo` | `logo`, `clearlogo` | films, series |
| `thumb` | `<episode file's base name>-thumb` | episodes |

- The formats are `.jpg`, `.jpeg`, `.png` and `.webp`.
- A film's images are in its folder: a name alone, `poster.jpg`, or after the base name of one of its files and a dash, `Paper Comets (2019)-poster.jpg`. A lone file at the top of a films library has only the second kind, beside it.
- A series' images are in its folder, a season's poster in its season folder, by a name alone. An episode's thumb is beside its file.
- Not kept: images in an extras folder; `banner`, and the landscape `thumb` and `landscape` of films and series, for which no screen has a place; backdrops numbered for cycling, `backdrop-1.jpg`, and the `extrafanart` folder; a season's backdrop and logo.

### 4.9 Grouping folders

A library may be rooted a level above its titles, with a folder for each category: `films/Amber Atlas (1969)/…` beside `series/Glass Harbor (2020)/Season 03/…`. Such a folder is a grouping folder, in a films, a shows or a mixed library (D49, as D52 amends it). It makes no item, and the folders in it are read as if they were at the top of the root, and so are those of a grouping folder in it.

- A **grouping folder** is a folder whose name has no year, that holds no video and no season folder of its own, and each of whose folders has a year in its name, holds a season folder, or is a grouping folder itself. A series without a year counts by its season folder: `series/Quiet Station/Season 01/…`.
- A folder counts only when it holds a video somewhere below it, as the server knows the root's files. An empty folder, or one of extras only (4.6), does not.
- A film or a series in it takes its title and year from its own folder, and is keyed by that folder's path below the root: `films/Amber Atlas (1969)`, `anime/films/Tide Runner (2005)`.
- Any other folder at the top is a title's, as before D49. `Odd Lot/Ferry Signal/…` beside `Odd Lot/Salt Road (2010)/…` is one film, or one series, called Odd Lot, since `Ferry Signal` has no year and holds no season folder. So is a folder that holds a video of its own.
- Grouping folders are read from all the files of a root each time items are built, so a folder can become one, or stop being one, as its folders and files change (section 5).

### 4.10 Mixed libraries

A mixed library (`mixed`) holds films and series side by side. It settles what each folder is before anything is matched, from its names, by rules the parse report shows (D49). Each folder at the top of the root, or below its grouping folders (4.9), is a series' when it holds:

- a season folder (4.2);
- a file with an `SxxEyy` token (4.3, step 2); or
- two files that the anchored class gives different numbers (4.3, step 3, and a folder's own numbering), and that the film rules do not read as parts of one video (4.4): `Salt Road (2010) - Part 1.mkv` and `Salt Road (2010) - Part 2.mkv` are a film's parts, though the numbers after the title differ.

Every other folder is a film's (4.1): one video, as `7 Lanterns (2007)/[Grp] 7 Lanterns (BD 1080p).mkv`, whose title begins with a number, or several that are versions or parts.

- **The report's note.** Where a film's files, read as a series' episodes, give different numbers in any class, as `Ferry Signal (2016)/[Grp] Night Ferry - 01.mkv` and `[Grp] Night Ferry - 02.mkv` do, each of them gets a note in the parse report: such a folder is more likely a series whose files are named otherwise, and the note says what makes a folder a series'.
- **A lone file** at the top of the root is a film, unless its name has an `SxxEyy` token: then it makes no item, as at the top of a shows library.
- **A folder can change kind** as its files change: a second episode numbered after the series' title makes a film's folder a series'. Its old item goes, and a new one is made (section 5).

Each folder's files are read as episodes, all together as section 4.3 reads a folder's videos, and then as a film's or a series', so the anchored class counts a folder's own numbering. Everything else, in a series' folder or a film's, is read as in a shows or a films library.

## 5. Building items

Run after every scan. It must be idempotent: a second run changes nothing and bumps no sequence, so a scan that changed nothing changes no item.

- **What a build reads.** A scan of the whole library, and of a whole root, builds all of it. A scan of some folders, as a change on disk asks for (2.2), builds the items of their top folders: the folders at the top of the root that hold them, with everything below. Those are what the change can touch, since a film's or a series' folder is its top folder or lies inside it, grouping folders and all (4.9). A file at the top of the root is a top folder by itself. The build also reads the top folders of the files whose rows a scan removes after 30 days missing, wherever they are in the root, and those of the items that play its files now, until there are no more: a file that moved in from another top folder is still played by an item there, which can then follow it. Everything else is neither read nor written. What a build of some folders makes is what a build of the whole library makes there; the tests hold every partial build to that.
- **A newer parser** is applied without a file having changed by the walk of every root at start and each night (2.2), which builds everything.

- **Films.** One movie item per key. Its files are attached as versions or parts.
- **Series.** One series item per series folder. Season items are created for the season numbers that occur, and a season goes when its last episode does. Episode items are created per episode key: `(series, season, episode)`, or `(series, absolute_number)` in a series numbered in one running count (below).
- **Titles before metadata.** A film or series takes its title and year from the folder name. An episode is titled `Episode N` until metadata arrives, and `Episodes N–M` when its file holds several. An episode without a number keeps the title from its file's name (section 4.3). A season is titled `Season N`, and season 0 `Specials`.
- **Titles after metadata.** Once a provider's metadata is written onto an item (section 6.3), the build leaves its title and year as they are. An episode that its files give another number loses what the provider said of its old number, and is titled from its name until its season is read again. When metadata goes after 180 days without a refresh (D53), the next build titles the item from its name again.
- **Episodes without a number** (D51) have no `episode_number`. In their season they come after the episode their decimal follows, `episode_order` holding the decimal, or else after the numbered episodes, by title. They are never matched to a provider's episode by number, and they are on the review list. Next up and a series' resume step over them, as they step over specials, and the season's list and `prev` and `next` take them in their place (`04-api.md`, sections 2.2 and 4.2). A running count does not count them.
- **A renamed folder keeps its item.** When none of an item's files parse to its own key any more, and they now parse to a key that no item has, the item takes the new key and keeps its id: a folder renamed to fix a year keeps its watch state. This needs the files to have kept their ids, which takes a fingerprint (section 2.1).
- **An episode follows its files.** An episode keeps its id while its key is wanted. When its key is not wanted any more, it goes with its files to the key they make now, if no episode has that key: a file renamed to correct its number, or to another series' folder, keeps its episode and the watch state on it, and so does a series that changes its numbering. When an episode's files now make several episodes, it goes with the one that holds its oldest file, the one with the lowest id. A series follows its episodes' files in the same way, and a film its files. Where the new key already has an episode, the files join it as versions, and the episode they left goes when no file makes it. Like a renamed folder, this needs the files to have kept their ids.
- **Provisional numbers.** Until a running count is mapped to the provider's seasons (6.5), its episodes are shown in season 1, or in their season folder's season, numbered by their running number. The mapping changes their season and episode numbers and not their key, so they keep their ids. A file with an `SxxEyy` token, or in the specials' folder, keeps its season and number in a running count, and does not decide whether the series has one.
- **`sort_title`** is the title in lower case without a leading article (`the`, `a`, `an`).
- **`added_at`** is the earliest `first_seen_at` of the item's files. For a series it is that of its newest episode, so a new episode brings the series to the top of Recently added.
- **Absolute numbering.** When a series has files in the anchored, dash or guess class, outside the specials, whose numbers run past what one season holds (a number above 99, or above the provider's count for that season once known), the series is set to `numbering = 'absolute'`, the numbers are stored in `absolute_number`, and season and episode numbers wait for the mapping in section 6.5. A person can set `numbering` by hand in the match dialog.
- **Removal.** An item whose files have all been missing for 30 days is deleted, with a tombstone. Until then it is shown as unavailable. More generally, an item that no file makes any more is deleted when items are built, with a tombstone; a series takes its episodes with it, each with its own.
- **Collections** are items too, but they are built from metadata, not from files (section 6.6).
- **A change of kind.** A films or a shows library can become mixed (`04-api.md`, section 8.2), and no other change of kind is allowed (D49). The change writes the library's kind and nothing else; a scan follows at once, and its build reads every folder again as section 4.10 says, by the rules of this section:
  - A folder that reads as the kind it was keeps its item and its id, and so does everything it holds: a film with its versions and parts, a series with its seasons and episodes, and everyone's watch state on them.
  - A folder that reads as the other kind now is a new item: a films library's folder of episodes becomes a series, and a shows library's folder that holds no season folder, no file with an `SxxEyy` token and no two anchored numbers, such as a film's in its own folder, becomes a film. The old item goes, with a tombstone, and the watch state on it and on its episodes goes with it. A lone file at the top of a films library whose name has an `SxxEyy` token makes no item in a mixed library, and its film goes too.
  - In a mixed library a film and a series never hold one folder at once: the item that goes gives up its folder before the new one takes it.
- **A folder read through** as a grouping folder (4.9), in any kind of library, gives its items to the titles in it as files give their items to new keys above. A folder that was one series is the series in it that holds its oldest file, with that series' id, its seasons and the episodes whose keys it still wants, and their watch state. The other series in it are new, and an episode whose key that series no longer wants follows its files into the series they are in now, with its watch state. A folder that was one film, its versions in folders of their own, is the film that holds its oldest file in the same way, and the others are new.
- **Sequence.** Every item written takes a sequence value of its transaction, which takes one value for every 500 rows (`03-data-model.md`, section 5). An episode that comes, goes or moves also bumps its season and its series, because their counts changed.

## 6. Matching and metadata

### 6.1 Order

For a film or a series, stop at the first step that gives an answer:

1. **A manual match** in `overrides` for this folder. A row that names the provider `none` means the folder was left unmatched on purpose: stop here, and the item is `ignored`.
2. **Ids in the name** (4.5). Every build writes them again, so where the import gave other ids, the name's win at the next build. Jellyfin reads the same ids from the same names, so the two seldom differ. `.nfo` files are not read: the scanner does not see them, and no stage builds it.
3. **The one-time import** from Jellyfin (section 7), which seeds provider ids by path.
4. **Search and score** (6.2).

Episodes are never searched for. They are looked up by number inside their series' match.

### 6.2 Search and score

- **Which items.** A film or a series that is `unmatched` or on the review list, whose names gave no id, that the import gave none, and whose folder has no row in `overrides` (6.1). An episode is never searched for.
- **Search the provider** by the title and the year the item has from its names: `/search/movie` for a film, `/search/tv` for a series, with `include_adult=false`, `language=en-US` and the first page (6.4). With a year, a film's search asks for TMDB's `year`, which takes any release date in it, and a series' for `first_air_date_year`; when no title of that answer is within a year of the item's, the search is asked again without the year, so that a title TMDB dates otherwise is found. A film is searched for among films and a series among series: a mixed library settles which an item is before it is matched (4.10).
- **Score each candidate** from 0 to 1: 0.7 for the similarity of the normalised titles, the better of the candidate's title and its original title; 0.2 for the year, whole when it is the same, half when it is a year apart or either has none; and 0.1 for the candidate being the right kind, which a search of one kind always is. Titles are compared in lower case, with the common Latin marks folded (macrons too), `&` read as `and`, apostrophes dropped, every other mark a space, and a leading `the`, `a` or `an` left out; letters of every script are kept, so a name in the title's own script finds its original title. The similarity is one less the edit distance in characters over the longer title's length. Scores are rounded to three places. A search answer does not carry a title's alternative titles, which only its details do, so they are not counted; the anime profile matches romanised names offline first (6.5).
- **Accept** when the best score is at least 0.90 and at least 0.10 ahead of the second (P4). The item becomes `matched`, its TMDB id is written, its score is its `match_confidence`, and its details are fetched (6.3).
- **Otherwise** the item becomes `review`, with the best score as its confidence. The candidates are not stored: the search's answer is in `provider_cache`, and the dialog asks the same search again (`04-api.md`, section 8.4).
- **Why** is written to `match_note` for the review list, as JSON: `no_results`, `low_score` or `too_close`, with the number of candidates, the best and the second score, and the title, the year and the time searched for: `{"reason": "too_close", "candidates": 2, "best": 0.9, "second": 0.9, "title": "Glass Harbor", "year": null, "at": 1795200000000}`. A running count that could not be mapped is noted with the anime profile (6.5), and an id that names nothing at the provider (6.3) as `{"reason": "id_not_found", "provider": "tmdb", "value": "404"}`.
- No result at all leaves it `unmatched`, with its note.
- **When.** A search is queued whenever fetches are (6.3): after every build, after an import, at start and each night, when a key is set, and when a library is refreshed; it runs as a `match` task (section 9). It is queued for an item never searched for, or searched for under another title or year, as after its folder was renamed; at start, each night and on a library's refresh, also for one searched for more than 30 days ago. A search's answer is read from the cache while it is younger than 30 days, so a second round of an unchanged library asks for nothing. An item's refresh searches again, asking the provider (`04-api.md`, section 8.4). Without a key nothing is searched for.
- **Ids stay.** A match writes the provider's id onto the item and changes nothing else about it: its id, its files and everyone's watch state stay.

The thresholds are starting values. Tune them on a real library, against what Jellyfin matched there.

### 6.3 What is fetched

| For | Request | Kept |
|---|---|---|
| A film | `/movie/{id}` with `credits,external_ids,images,release_dates` appended | Title, original title, overview, date, runtime, rating, certification, genres, ids, image paths |
| A series | `/tv/{id}` with `aggregate_credits,alternative_titles,content_ratings,external_ids,images` appended | The same, plus alternative titles for the search index |
| A season | `/tv/{id}/season/{n}`, for each season the library holds that the series' details list | Its title, overview and air date, and every episode's title, overview, air date, runtime, rating and still |
| An IMDb or TheTVDB id without TMDB's | `/find/{id}` with `external_source` `imdb_id` or `tvdb_id` | TMDB's id of the film or the series, written beside the other |
| Credits | Appended to the film's or the series' details request, at no extra request | For each person kept: the provider's id, name, what they are known for, the path of their photo, and the character or the job. For a series, the number of episodes. |
| A collection | `/collection/{id}`, when a library first holds two of its films (6.6) | Title, overview, poster and backdrop paths |

- Every response is stored in `provider_cache`. Changing how a response is read never fetches it again. Changing a request does, for every item: the lists above are part of each cached answer's key, so they are settled once.
- **Language.** Every request asks for `language=en-US`, TMDB's own default, and the certification kept is that of the United States: a film's from its release dates, theatrical first, then limited, digital, physical, TV and premiere, the first that has one; a series' from its content ratings. Details requests also ask for `include_image_language=en,null`, since TMDB filters images by the request's language and most backdrops have none (6.4). No setting changes these in version 1.
- **The rating** is TMDB's average of its users' votes, to one decimal, and none while nobody has voted. **The runtime** is a film's or an episode's, in minutes, and a series' first `episode_run_time`.
- **Which items.** A collection (6.6), and a film or a series that is `matched` or `manual` and has a TMDB id, or an IMDb or a TheTVDB id that `/find` turns into one. An id that names nothing at TMDB, a `404`, or one that `/find` finds nothing for, puts a `matched` item on the review list with why (6.2); a manual match stays as it is.
- **When.** A fetch is queued for every such item that was never fetched, or is past its age, and for a series that holds a season or a numbered episode not fetched yet: after every build, after an import that wrote ids, at start and each night, when a key is set, and when a library is refreshed (`04-api.md`, section 8.2). Without a key nothing is queued, and setting one queues it all. An item's refresh always asks (section 8.4 there).
- **What it waits for.** A provider that asks the server to wait pauses every fetch for that long (6.4), without counting the run. A key the provider refuses pauses them for an hour, or until another key is set.
- **Onto the items.** A film's or a series' title, original title, overview, date and the year from it, runtime, rating, certification and genres, and a series' alternative titles, which only the search index holds (`03-data-model.md`, section 4, Metadata on items). A season's data goes onto the season and onto its episodes by number: a file that holds several episodes takes their titles joined with ` / `, the first one's overview, date and rating, and their runtimes added. An episode the provider does not list keeps its title. Episodes without a number (D51), and those of a series numbered in one running count until it is mapped (6.5), are not written by number.
- **Refresh**: a series that is still airing, weekly. Everything else, every 30 days, and on request. A series airs while its details say `in_production`, or name a `next_episode_to_air`; then its details and its seasons from the one its `last_episode_to_air` is in are asked again after 7 days, and its older seasons after 30. A fetch reads what the cache holds while it is younger than that, so a second full refresh of an unchanged library asks for nothing (`01-roadmap.md`, section 6, exit criterion 6), and a new episode takes its title from the season kept there.
- **How long anything fetched is kept** (D53): a response, and what was read from it onto an item, is dropped when it has not been fetched again for 180 days, as when the provider could not be reached or the key was taken away. TMDB's terms forbid keeping what it gives for longer than six months (6.4). The provider's ids stay: they are the match, which a person may have made by hand. The drop runs at start and each night, before the walk that builds every library, which titles each such item from its name again (section 5).
- At most two provider requests run at once.
- **Credits kept** (P7): the first 20 of the cast in billing order (TMDB's `order`), then the directors, then the writers, each person once in each. An item's credits are replaced as a whole on every fetch; the people they name are written then, and those no item credits any longer go each night.
  - **A film**: its directors are the crew whose job is `Director`, its writers those of the department `Writing`, with a person's jobs there joined, such as `Screenplay, Story`; both in TMDB's order.
  - **A series**: each of the cast with the role they play in the most episodes, and their episodes in all. Its directors, then its creators (`created_by`, as `Creator`), then the other writers, the directors and writers by how many episodes they worked on, most first. P7 limits only the cast, so a long series keeps every director and writer its credits name. An answer without `aggregate_credits` asks `/tv/{id}/aggregate_credits` by itself, at the same age as the details.
- **No photo is downloaded here.** A person's photo is fetched when a client first asks for it (section 8).

### 6.4 Providers

| Provider | Use | Notes |
|---|---|---|
| TMDB | Primary, for films, series, episodes and artwork | Needs a free key from whoever runs the server, set in the web interface or as `RUNNEL_TMDB_KEY` (D34). A new key is checked with one request before it is kept. Non-commercial use with attribution. Below. |
| Anime-Lists | Mapping between AniDB entries and TMDB or TVDB seasons, with episode offsets | An XML file on GitHub. Fetched at most once a day. |
| anime-offline-database | Titles and synonyms, for matching romanised release names without a network call | A JSON file, updated weekly, under the ODbL. |
| TheTVDB, AniDB, AniList, TVmaze | Not called in version 1 | Licensing or rate limits; see the research notes, section 7.1. AniDB and AniList ids are still stored when a mapping supplies them. |

**TMDB**, as its API reference at `developer.themoviedb.org` gives it [checked 27 November 2026, unless marked otherwise]:

- **The address** is `https://api.themoviedb.org/3`, and images are served from `https://image.tmdb.org/t/p/` with a size and the image's path.
- **The key.** TMDB gives an account two credentials: an API key, and an API read access token. Its reference calls the token, sent as `Authorization: Bearer <token>`, "the default method"; the API key goes in the query as `api_key`; both give the same access. The server takes either, and tells them apart by their form: a read access token is a JSON Web Token, three parts of base64 joined by dots, and is sent as the bearer header; anything else is sent as `api_key`. Neither is ever written to the log, to an error, or into `provider_cache`'s keys, whose requests are stored without it.
- **Checking a key** is one `GET /configuration` with it: `200` keeps it, and stores the configuration with it; `401` is a key TMDB refuses (its error 7, "Invalid API key"); anything else, or no answer, is a provider that cannot be reached, and nothing is stored. TMDB's error page gives the codes the server meets: 7, an invalid key, and 10, a suspended one, both `401`; 6, an invalid id, and 34, a resource that cannot be found, both `404`; 25, too many requests, `429`; 27, more than 20 appended, `400` [checked: its page on errors, 27 November 2026]. A later request that gets `401` marks the key as not working (`tmdb_key_ok`, `03-data-model.md`, section 4), and so does one that works mark it as working again.
- **The searches** [checked: a search engine's copy of TMDB's reference page for `/search/tv`, and copies of its description for `/search/movie` in client libraries' documentation, 28 November 2026; the reference pages themselves were not read]: both take `query`, `include_adult` (by default `false`), `language` (by default `en-US`) and `page` (by default 1, at most 1000, 20 titles a page). `/search/movie` takes `year`, which matches any of a film's release dates, `primary_release_year`, which matches its primary one, and `region`. `/search/tv` takes `first_air_date_year`, which TMDB's page says searches only the first air date, and `year`, which searches the first air date and every episode's. An answer holds `page`, `results`, `total_pages` and `total_results`; a film's result has `id`, `title`, `original_title`, `release_date`, `poster_path` and `popularity`, a series' `id`, `name`, `original_name`, `first_air_date`, `poster_path` and `popularity`.
- **The requests**: `/search/movie`, `/search/tv`, `/movie/{id}`, `/tv/{id}`, `/tv/{id}/season/{n}`, `/find/{external id}` (by `imdb_id`, `tvdb_id`, `wikidata_id` and social ids; not by AniDB's), `/collection/{id}` and `/configuration`. A film's details carry `belongs_to_collection` with its id, name, poster and backdrop. A collection's details take `language`, by default `en-US`, and answer its `id`, `name`, `overview`, `poster_path`, `backdrop_path` and `parts`, its films [checked: a search engine's copy of its reference page, and the copy of TMDB's OpenAPI description at `apis.io`, 28 November 2026; the reference page itself was not read]. A series' details carry `seasons`, each with its `episode_count`, and `status`, `in_production`, `next_episode_to_air` and `created_by`.
- **`append_to_response`** joins other requests to a film's, a series', a season's, an episode's or a person's details, at most 20 of them in one request. The reference pages of the film's, the series' and the season's details describe it the same way: "comma separated list of endpoints within this namespace, 20 items max". Each of them takes `language`, by default `en-US` [checked: those pages, 27 November 2026].
- **A film's credits**, appended as `credits`: `cast`, each with the person's `id`, `name`, `known_for_department` and `profile_path`, and the `character` and `order`; `crew`, each with the person's and their `job` and `department`, one entry for each job [checked: the answers with `append_to_response=credits` shown in TMDB's support forum, 28 November 2026; the reference page itself was not read]. Of a series' `created_by` (above), the server reads each one's `id`, `name` and `profile_path`, as the other people's: untested against a real answer.
- **A series' credits with episode counts** are `/tv/{id}/aggregate_credits`: each cast entry has `roles`, each with its `character` and `episode_count`, and the person's `total_episode_count`, `order`, `profile_path` and `known_for_department`; each crew entry has `jobs`, each with its `job` and `episode_count`, and the person's `department` and `total_episode_count` [checked: its reference page, 27 November 2026]. It is an endpoint within the series' namespace, so it is appended to the series' details. That is the reference's rule, not a request made with a real key: the part that reads credits asks for it by itself when an answer comes without it.
- **Limits.** The old limit of 40 requests in 10 seconds was turned off on 16 December 2019. TMDB says only that the limit is now somewhere around 40 requests a second, that it may change, and that a client must respect `429`; it documents no `Retry-After`. The server runs at most two requests at once (6.3). After a `429` it sends none until the time the answer's `Retry-After` gives, in seconds or as a date, at most an hour, or for 10 seconds without one, doubling while the answers stay `429`, up to five minutes; background work pauses meanwhile (section 9).
- **Images and their language.** The language a request asks for filters its images; `include_image_language` takes a list of languages, such as `en,null`, where `null` is an image without one, as nearly every backdrop and every episode still is [checked: its page on image languages, 27 November 2026].
- **The image configuration** (`/configuration`, its example; its reference page says nothing of how often to read it [checked 27 November 2026]): backdrops at `w300`, `w780`, `w1280` and `original`; logos at `w45`, `w92`, `w154`, `w185`, `w300`, `w500` and `original`; posters at `w92`, `w154`, `w185`, `w342`, `w500`, `w780` and `original`; people's photos at `w45`, `w185`, `h632` and `original`; episode stills at `w92`, `w185`, `w300` and `original`. The server asks for fixed sizes from these, the ones section 8 gives, and does not read the configuration it stored with the key.
- **Its terms** [read from a search engine's copy of the terms page, 27 November 2026; the page refuses automated reading; last changed on 20 October 2023 by others' reading of it]: use is free for a project that is not commercial, with attribution. Nothing obtained from TMDB may be cached for longer than six months (§1.C), which section 6.3 and D53 answer.
- **The attribution a client must show**, as TMDB's developer FAQ gives it [checked 27 November 2026]: the sentence "This product uses the TMDB API but is not endorsed or certified by TMDB." in the client's About or credits screen, with one of TMDB's approved logos, less prominent than the client's own. Every official client carries it on its About screen (`10-clients.md`).

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

- A film's details name the collection it belongs to, if any. That goes on the film as an `external_ids` row with provider `tmdb_collection`, whether or not a collection is shown. Details that no longer name one take the row away.
- **Counting.** After every build, with a key set or not, the server counts each collection's films in the library; after a film's fetch, those of the collections it named before and names now. A film counts while it is an item: one whose file is only missing still does, until its row goes (section 5). Films in another library do not.
- **At two**, it makes the collection item, titled with the name the film's details gave, points the films at it, and queues the collection's fetch: `/collection/{id}` (6.4), whose name, overview, poster and backdrop replace what it had. The images are fetched as a film's are (section 8). A collection that TMDB answers `404` for keeps the name it was made with.
- **Below two**, it deletes the item, with a tombstone, and clears the pointer on the film that is left. A collection made again later is a new item, with a new id.
- **Refresh**: every 30 days, as anything else (6.3). Dropped after 180 days without one (D53), a collection keeps its films and is titled "Collection" until it is fetched again: it has no name of its own to fall back to.
- A collection is never built from a folder, and a library's listing does not include collections. They are reached from their films, and from search.
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
- Before the dry run, `POST /admin/import/jellyfin/users` lists Jellyfin's users, for the form that maps them to people here.

**The calls**, checked against Jellyfin 12.4's published API description (the `openapi.json` of version 12.4.0 that its SDKs carry) and its source at `v12.4` [checked 22 November 2026]:

- **The key** goes in `Authorization: MediaBrowser Token="<key>"`. That is the header the description names. Jellyfin 12 turns the older ways off by default: `X-Emby-Token`, `X-MediaBrowser-Token`, `X-Emby-Authorization` and `api_key` are read only with its `EnableLegacyAuthorization` setting (`Jellyfin.Server.Implementations/Security/AuthorizationContext.cs`). An API key needs none of the header's device fields.
- **`GET /System/Info/Public`**, without the key, gives `Version` and `ServerName`.
- **`GET /Users`** gives the users, each with `Name` and `Id`. A key Jellyfin refuses is `401`.
- **`GET /Items`** with `userId`, `recursive=true`, `includeItemTypes=Movie,Series,Episode`, `excludeLocationTypes=Virtual`, `fields=Path,ProviderIds`, `enableUserData=true`, `enableImages=false`, `sortBy=SortName`, `startIndex` and `limit=500` pages every film, series and episode that has a file. Each comes with its `Id`, `Name`, `Type`, `Path` and `ProviderIds`, and with that user's `UserData`: `Played`, `PlayCount`, `LastPlayedDate` and `PlaybackPositionTicks`, in units of 100 ns. A page answers `Items`, `TotalRecordCount` and `StartIndex`. Lists are comma-delimited, as the description says. With an API key `userId` may be left out, and the items then come without user data.
- **Provider ids** are the keys of `ProviderIds`. Jellyfin itself writes `Tmdb`, `Imdb`, `Tvdb` and `TmdbCollection` (`MediaBrowser.Model/Entities/MetadataProvider.cs`). Its anime plugins write `AniDB` and `AniList` (their `ProviderNames.cs` at the default branches of `jellyfin-plugin-anidb` and `jellyfin-plugin-anilist`). They become `tmdb`, `imdb`, `tvdb`, `tmdb_collection`, `anidb` and `anilist`. Keys are compared without case, and other keys are left.
- **Answers** are JSON with PascalCase names, Jellyfin's default.
- The tests run against a stand-in that answers as the description says, and that refuses the older ways to send the key.

**How it joins and writes:**

- A path from Jellyfin is first rewritten by the `paths` entry whose `from` is the longest that holds it, matching whole folders. It then belongs to the deepest library folder here that holds it.
- A film or an episode joins the item that the file at that path plays. A series joins the series built from that folder. A row that does not join is listed with why: no file at that path, a file that is no film's or episode's (an extra), no series built from that folder, a path outside every library folder, or a different kind of item here.
- Films and series get the provider ids Jellyfin gives, and become `matched`. Ids that Jellyfin does not give stay. An item matched by hand, or left unmatched on purpose, is left as it is. An item whose ids change is stamped, since its detail changed.
- Films and episodes get a play state for each person mapped: Jellyfin's played flag, play count, last played time and position. A state here whose `last_played_at` is later than Jellyfin's is kept as it is. A played item has no position, and the play count is the greater of the two. A Jellyfin item that says nothing (unplayed, no plays, never played, no position) writes nothing, and so does one that would change nothing.
- With nobody mapped, the import still joins and writes the provider ids.
- The import is the queue's one task of kind `import` (section 9). The task keeps the request, with the key, until the run ends. The run waits at the playback gate before each page. A failure ends the run as failed, its report says why, and it is not tried again by itself. A run that the server's stop cuts short is queued again at the next start.
- One import runs at a time. Its report is kept in the data directory, replaced by the next run. `GET /admin/import/jellyfin` returns the summary and the first 100 rows that did not join, and every row with `full=1` (`04-api.md`, section 8.5).

## 8. Artwork

| Kind | For | Widths kept | Downloaded from TMDB | Why, in the designs |
|---|---|---|---|---|
| `poster` | films, series, seasons | 342, 500 | `w500` | PosterCards: 264 px in TV rows, 248 in TV search, about 333 px for a phone's three columns (111 dp at 3×), 140 px and more in the web's grids; 500 for the web's larger cards, such as a collection's 180 px at 2× |
| `backdrop` | films, series | 780, 1280 | `w1280` | The TV's film page draws it at 1280 × 720 and the hero card at 1040 × 360; 780 for a WideCard without a thumb (440 px on TV, 280 on the web at 2×, 240 dp on a phone at 3×) |
| `thumb` | episodes | 440, 780 | `original` | The TV's EpisodeItems have 400 px thumbnails and its WideCards are 440 px; 780 for the web and phones, as backdrops. TMDB's widest fixed still is 300 (6.4), so the original is downloaded and scaled once |
| `logo` | films, series | 500 | `w500` | Kept as PNG; a logo TMDB has only as SVG is not taken, since ffmpeg does not decode SVG |
| `photo` | cast and crew | 240, 420, cropped to 2:3 | `h632` | PersonCards, 3:4: 208 px on TV, 224 on the web (112 px at 2×), 252 on phones (84 dp at 3×), 240 for all three; the person page 360 × 480 on TV and 440 on the web (220 px at 2×), 420 for both, the most `h632` gives (421 × 632) |

- **Local images first** (4.8). They are scaled to the widths above by an ffmpeg child, once, none wider than the file itself: a file narrower than a width is kept at its own, and that is the width listed. A scan finds them after it has built the items, and queues an `image` task for each one that is new or changed (section 9). An image whose file went is removed, with its files. The files of an item's images go with the item, at the next start or by night (`03-data-model.md`, section 4). A scan also makes again an image whose files were deleted from the data directory.
- **Provider images** are downloaded once, at the size in the table, and scaled to the widths kept by an ffmpeg child, as local images are; a download already of a width kept is kept as it is. The server never decodes an image in its own process. A download is written to the image folder as it arrives, at most 32 MB, and removed once scaled.
- **Which.** A film's or a series' poster and backdrop are its details' `poster_path` and `backdrop_path`, which TMDB chooses for the request's language; its logo is the best-voted PNG among the appended logos, in English first, then without a language, then any. A season's poster is its season's `poster_path`, an episode's thumb its `still_path`, and a file of several episodes the first one's. A path that is absent asks for nothing.
- **One image of a kind.** A local image comes first: an item with one of a kind downloads none of the provider's for it, and a local image that arrives replaces the provider's. When a local image goes, the item is fetched again from the cache (6.3), and the provider's takes its place. A refresh whose paths are those already stored downloads nothing; a new path replaces the old image. What was downloaded goes with D53's drop after 180 days without a refresh, and the sweep deletes its files.
- **Key** of a provider's image: the first 128 bits of the SHA-256 of `tmdb:` and its path.
- **Key.** 32 hex characters derived from the image's source: the provider and its image path, or a local file's fingerprint (2.1). For a local file it is the first 128 bits of the SHA-256 of `local:` and the fingerprint. A changed image has a new key, so responses can be cached forever.
- **On disk**: `images/<first two characters>/<key>_<width>.jpg` under the data directory, and `.png` for a logo, which keeps its transparency.
- **Placeholder colour**: the average colour of the smallest stored width, computed once, by an ffmpeg child that scales it to one pixel.
- **Order of work**: posters, backdrops and logos of films, series and seasons first. Episode thumbs are queued behind every other image, and a client that lists a season's episodes (`GET /items?parent=`, its first page) moves that season's to the front.
- A library of 3,000 films and 240 series comes to roughly 3,500 posters and backdrops, a few hundred megabytes at most.
- **People's photos arrive on first request** (D33). A film can credit dozens of people, so matching a library downloads none of them. The key is derived from the photo's path when the credits are stored, as a provider's image's is, so the address exists at once. `GET /img/{key}/{w}` for a key that no item's image has finds the person whose photo it is, downloads TMDB's `h632` of it, crops it to 2:3 about its centre and scales it to 240 and 420 with an ffmpeg child, works out the colour, and answers. Every photo is kept at both widths and at 2:3, a smaller one scaled up, so a credit gives its widths and shape before the photo is fetched (`04-api.md`, section 2.4). The files are `images/<ab>/<key>_<width>.jpg`, as other images'.
  - **The bounds**, since the request needs no token (`04-api.md`, section 6): only a key that a person's row holds is fetched, from the path that the provider's credits gave, never from anything in the request. One download runs for a key at a time, and every request for it waits for that one; at most two run at once, and a request that finds both taken waits up to 15 s for one, then is told to come back. A download and its scaling have 15 s. Nothing is fetched without a metadata key, or without ffmpeg.
  - **When it fails.** A photo the provider does not give, for whatever reason, is `404`, and is not tried again for a day (P7). When the provider could not be reached, answered with an error of its own (5xx), or answered `429`, no photo is downloaded for a minute, or for the time a `429`'s `Retry-After` gives, at most an hour; a request meanwhile is answered `503` with `Retry-After`, and its photo is not counted as tried.
  - **Fetching a photo stamps nothing.** People are not part of the local copy, and a detail shows the photo's colour from the item's next change.
  - **How long a photo is kept** (D53): a photo fetched 180 days ago is dropped, its files deleted and its key kept, so the next request fetches it again. A person whom no item credits any more goes each night (6.3), and the sweep deletes their photo's files.
- **People's photos at TMDB** [checked: TMDB's support forum, 28 November 2026; its image guidelines and its API reference were not read]: an uploaded profile photo must be 2:3, rounded to two decimals, and at least 300 × 450; the `h` sizes of `/configuration`'s `profile_sizes` give the height, so `h632` is 632 high and 421 wide at 2:3. An older photo of another shape is cropped.
- **Episode stills**, checked on 27 November 2026: TMDB's fixed still sizes are 92, 185 and 300 wide (6.4), narrower than the TV's 400 px rows, so a still is the original, scaled once (`11-design-review.md`, U5).
- **Images made before** keep their widths until their file changes: a backdrop or a thumb scaled when the widths were 1280 and 300 is served at those, the nearest it has (`04-api.md`, section 6).

## 9. Background work

One queue in the `tasks` table, so work survives a restart.

| Kind | Runs | At once |
|---|---|---|
| `probe` | after a scan, per new or changed file, and at start for any file still to probe | 2 |
| `match` | whenever fetches are queued, per film or series to search for (6.2); on an item's refresh | 2, sharing the provider limit |
| `fetch` | after a build, an import or a match, for what was never fetched or is past its age; each night; when a key is set; on refresh (6.3) | 2, sharing the provider limit |
| `image` | after a scan, per local image that is new or changed; from stage 4 after a fetch | 2 |
| `sidecar` | when a transcode starts | 1 |
| `import` | on request | 1 |
| `extract` | when a browser first asks for a file's subtitles or fonts, or a `prepare` says so | 1 |

- A failed task is retried five times, after 1, 2, 4, 8 and 16 minutes, then left as failed for `GET /admin/tasks`. A probe of a file that ffprobe cannot read has not failed: it is done, and the file keeps the reason (section 3).
- A run that the server's stop cut short is queued again at the next start, and is not counted. Neither is a run that could not work for a reason that is not the task's own, such as an ffprobe that cannot be run: its whole kind waits, and then tries again.
- Everything here pauses while a playback session is starting (the playback gate, `02-architecture.md`, section 5).
- A provider that answers 429 pauses its queue for the time it asks.
- A task carries the library it works for. Finished tasks stay until their kind and library have nothing queued or running, and are then deleted together. `GET /admin/tasks` counts progress from that: done out of all (`03-data-model.md`, section 4).
- An admin can cancel what is queued or failed, and retry what failed. Cancelled work is found again by the next scan or refresh.
- The import is the exception to the retries: a run that fails ends there, and its report says why (section 7).
- A scan is not a task. The scanner reports its own state: walking, then probing with done out of total, which are its library's `probe` tasks.
