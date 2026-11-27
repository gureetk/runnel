# Data model

One SQLite file holds everything the server knows. This document is the schema, the rules that keep it consistent, and the queries the API is built on. The schema below has been loaded through the Go driver the server will use, and every query in this document has been run against it with sample rows.

**Revised, 20 November 2026,** for what the client designs asked of the server (`11-design-review.md`, section 6; D33 to D36): My list (`user_list`), collections (an item type, and `items.collection_id`), cast and crew (`people`, `credits`), settings and the setup code (keys in `meta`), the last scan's result (`roots`), progress of background work (`tasks.library_id`), and `tokens.last_used_at` in place of `last_seen_at`. For browsers (D39), `streams.level`. The schema and the queries were run again after the change.

## 1. Conventions

- **Identifiers** are SQLite integer primary keys. They are stable for the life of one database and are what the API exposes.
- **Times** are Unix milliseconds in `INTEGER` columns. Durations and positions are milliseconds. Dates without a time (`release_date`) are `YYYY-MM-DD` text.
- **Booleans** are `INTEGER` 0 or 1.
- **Paths** are stored as a root plus a path relative to it, with `/` separators. A library that moves to another mount is one `UPDATE roots`.
- **Files and items are separate.** A file is something on disk. An item is something a person browses: a movie, a series, a season, an episode, or a collection of films. A file is playable as soon as it is probed, before any item metadata exists.
- **Nothing is deleted because a disk is away.** Files get `missing_since`. Rows go only after the grace period in section 4.

## 2. Connections and pragmas

- One connection writes. It is used by one goroutine at a time and every write transaction starts with `BEGIN IMMEDIATE`.
- Up to four connections read. WAL mode lets them read while the writer writes.
- Per connection: `journal_mode=WAL`, `synchronous=NORMAL`, `foreign_keys=ON`, `busy_timeout=5000`, `cache_size=-1000` (1 MB). In the prototypes the cache size moved memory under load from 35.7 MB (4 MB caches) to 23.7 MB (1 MB caches).
- The driver must be built with FTS5. With `mattn/go-sqlite3` that is the `sqlite_fts5` build tag.
- SQLite 3.43 or newer is needed, for `contentless_delete` on the search table. `mattn/go-sqlite3` v1.14.53 bundles 3.54, and this schema was loaded through it with the `sqlite_fts5` tag: substring search and deleting from the search table both worked.

## 3. Schema

Migration `0001_init.sql`:

```sql
CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
) WITHOUT ROWID;
INSERT INTO meta (key, value) VALUES
  ('schema_version', '1'),
  ('seq', '0'),              -- the change sequence, see section 5
  ('tombstone_floor', '0');  -- changes at or below this may have lost their delete markers
-- Other keys are written when they first have a value: instance_id, setup_code, server_name,
-- tmdb_key, tmdb_key_ok, tmdb_key_checked_at. See section 4, Settings and sign-in.

CREATE TABLE libraries (
  id          INTEGER PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  kind        TEXT NOT NULL CHECK (kind IN ('movies','shows')),
  profile     TEXT NOT NULL DEFAULT 'default' CHECK (profile IN ('default','anime')),
  created_at  INTEGER NOT NULL,
  updated_seq INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE roots (
  id           INTEGER PRIMARY KEY,
  library_id   INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
  path         TEXT NOT NULL UNIQUE,       -- absolute, no trailing slash
  available    INTEGER NOT NULL DEFAULT 1, -- 0 while the mount is away
  last_scan_at INTEGER,                    -- the last scan of any kind: asked for, nightly, or after a change on disk
  last_scan_ms INTEGER,
  last_scan_added   INTEGER,               -- files
  last_scan_changed INTEGER,
  last_scan_removed INTEGER,
  last_scan_error   TEXT
);

CREATE TABLE files (
  id                INTEGER PRIMARY KEY,
  root_id           INTEGER NOT NULL REFERENCES roots(id) ON DELETE CASCADE,
  rel_path          TEXT NOT NULL,
  kind              TEXT NOT NULL DEFAULT 'video' CHECK (kind IN ('video','extra')),
  size              INTEGER NOT NULL,
  mtime_ns          INTEGER NOT NULL,
  fingerprint       TEXT,                        -- size plus a hash of the first and last 64 KiB
  container         TEXT,
  duration_ms       INTEGER,
  bit_rate          INTEGER,
  attachments_bytes INTEGER NOT NULL DEFAULT 0,  -- sizes the warm-up buffer
  probe_version     INTEGER NOT NULL DEFAULT 0,  -- 0 = not probed yet
  probe_error       TEXT,
  probed_at         INTEGER,
  first_seen_at     INTEGER NOT NULL,
  missing_since     INTEGER,
  UNIQUE (root_id, rel_path)
);
CREATE INDEX files_fingerprint ON files(fingerprint);

CREATE TABLE streams (
  file_id     INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  idx         INTEGER NOT NULL,                  -- ffprobe's stream index
  kind        TEXT NOT NULL CHECK (kind IN ('video','audio','subtitle','attachment')),
  codec       TEXT,
  profile     TEXT,
  lang        TEXT,                              -- as the file tags it
  title       TEXT,
  is_default  INTEGER NOT NULL DEFAULT 0,
  is_forced   INTEGER NOT NULL DEFAULT 0,
  is_hearing_impaired INTEGER NOT NULL DEFAULT 0,
  width       INTEGER,                           -- video
  height      INTEGER,
  pix_fmt     TEXT,
  bit_depth   INTEGER,
  level       INTEGER,                           -- as ffprobe gives it; with profile and depth it makes the name a browser asks by
  fps_num     INTEGER,
  fps_den     INTEGER,
  hdr         TEXT,                              -- 'hdr10','hdr10plus','hlg','dv' or NULL
  channels    INTEGER,                           -- audio
  channel_layout TEXT,
  sample_rate INTEGER,
  bit_rate    INTEGER,
  filename    TEXT,                              -- attachment
  mime        TEXT,
  size        INTEGER,
  PRIMARY KEY (file_id, idx)
) WITHOUT ROWID;

CREATE TABLE chapters (
  file_id  INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  idx      INTEGER NOT NULL,
  start_ms INTEGER NOT NULL,
  end_ms   INTEGER,
  title    TEXT,
  PRIMARY KEY (file_id, idx)
) WITHOUT ROWID;

-- Subtitle and audio files that sit next to a video.
CREATE TABLE external_tracks (
  id         INTEGER PRIMARY KEY,
  file_id    INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  rel_path   TEXT NOT NULL,
  kind       TEXT NOT NULL CHECK (kind IN ('subtitle','audio')),
  codec      TEXT,
  lang       TEXT,
  title      TEXT,
  is_default INTEGER NOT NULL DEFAULT 0,
  is_forced  INTEGER NOT NULL DEFAULT 0,
  is_hearing_impaired INTEGER NOT NULL DEFAULT 0,
  size       INTEGER NOT NULL,
  mtime_ns   INTEGER NOT NULL
);
CREATE INDEX external_tracks_file ON external_tracks(file_id);

CREATE TABLE items (
  id               INTEGER PRIMARY KEY,
  library_id       INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
  type             TEXT NOT NULL CHECK (type IN ('movie','series','season','episode','collection')),
  parent_id        INTEGER REFERENCES items(id) ON DELETE CASCADE,  -- season -> series, episode -> season
  series_id        INTEGER REFERENCES items(id) ON DELETE CASCADE,  -- seasons and episodes
  season_number    INTEGER,
  episode_number   INTEGER,
  episode_number_end INTEGER,                    -- a file that holds several episodes
  absolute_number  INTEGER,
  numbering        TEXT CHECK (numbering IN ('seasons','absolute')), -- series only
  root_id          INTEGER REFERENCES roots(id) ON DELETE CASCADE,   -- movies and series
  collection_id    INTEGER REFERENCES items(id) ON DELETE SET NULL,  -- movies: the collection shown for it
  path_key         TEXT,                         -- the folder (or lone file) the item was built from
  title            TEXT NOT NULL,
  sort_title       TEXT NOT NULL,
  original_title   TEXT,
  overview         TEXT,
  release_date     TEXT,
  year             INTEGER,
  runtime_ms       INTEGER,
  rating           REAL,
  content_rating   TEXT,
  match_state      TEXT NOT NULL DEFAULT 'unmatched'
                   CHECK (match_state IN ('unmatched','matched','manual','review','ignored')),
  match_confidence REAL,
  match_note       TEXT,                         -- JSON from the matcher: why the item is on the review list
  added_at         INTEGER NOT NULL,
  refreshed_at     INTEGER,
  updated_seq      INTEGER NOT NULL
);
CREATE UNIQUE INDEX items_path   ON items(root_id, path_key) WHERE path_key IS NOT NULL;
CREATE INDEX items_list   ON items(library_id, type, sort_title, id);
CREATE INDEX items_added  ON items(library_id, type, added_at DESC);
CREATE INDEX items_parent ON items(parent_id, season_number, episode_number);
CREATE INDEX items_series ON items(series_id, type, season_number, episode_number);
CREATE INDEX items_seq    ON items(updated_seq);
CREATE INDEX items_collection ON items(collection_id) WHERE collection_id IS NOT NULL;

CREATE TABLE item_genres (
  item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  genre   TEXT NOT NULL,
  PRIMARY KEY (item_id, genre)
) WITHOUT ROWID;
CREATE INDEX item_genres_genre ON item_genres(genre, item_id);

-- Which files play an item: versions of a film, parts of a split film, the file of an episode.
CREATE TABLE item_files (
  item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  label   TEXT,                        -- version label, such as '1080p'
  part    INTEGER NOT NULL DEFAULT 0,  -- 0 = the whole item, 1..n = parts in order
  PRIMARY KEY (item_id, file_id)
) WITHOUT ROWID;
CREATE INDEX item_files_file ON item_files(file_id);

CREATE TABLE external_ids (
  item_id  INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  provider TEXT NOT NULL,              -- 'tmdb','imdb','tvdb','anidb','anilist','mal','tmdb_collection'
  value    TEXT NOT NULL,
  PRIMARY KEY (item_id, provider)
) WITHOUT ROWID;
CREATE INDEX external_ids_lookup ON external_ids(provider, value);

-- Cast and crew. A person is shared by every item that credits them.
CREATE TABLE people (
  id               INTEGER PRIMARY KEY,
  provider         TEXT NOT NULL,      -- 'tmdb'
  provider_id      TEXT NOT NULL,
  name             TEXT NOT NULL,
  department       TEXT,               -- what they are known for, such as 'Acting'
  image_key        TEXT,               -- 32 hex characters, as in images.key; NULL = the provider has no photo
  image_ref        TEXT,               -- the provider's path for the photo
  image_color      TEXT,               -- '#rrggbb', known once the photo has been fetched
  image_fetched_at INTEGER,            -- NULL = not fetched yet: photos are fetched when first asked for
  image_tried_at   INTEGER,            -- the last try that failed
  UNIQUE (provider, provider_id)
);
CREATE UNIQUE INDEX people_image ON people(image_key) WHERE image_key IS NOT NULL;

CREATE TABLE credits (
  item_id    INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,   -- a film or a series
  ord        INTEGER NOT NULL,         -- the order shown: cast in billing order, then directing, then writing
  person_id  INTEGER NOT NULL REFERENCES people(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL CHECK (kind IN ('cast','crew')),
  role       TEXT,                     -- the character, or the job
  department TEXT,                     -- crew: 'Directing','Writing'
  episodes   INTEGER,                  -- series: how many episodes the person is in
  PRIMARY KEY (item_id, ord)
) WITHOUT ROWID;
CREATE INDEX credits_person ON credits(person_id, item_id);

CREATE TABLE images (
  id         INTEGER PRIMARY KEY,
  item_id    INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL CHECK (kind IN ('poster','backdrop','thumb','logo','banner')),
  key        TEXT NOT NULL,            -- 32 hex characters; names the files on disk and the URL
  widths     TEXT NOT NULL,            -- JSON array of the widths stored, such as [342,500]
  aspect     REAL,                     -- width divided by height
  color      TEXT,                     -- '#rrggbb', shown until the image arrives
  source     TEXT NOT NULL,            -- 'local','tmdb',...
  source_ref TEXT,
  rank       INTEGER NOT NULL DEFAULT 0,  -- 0 is the one shown
  UNIQUE (item_id, kind, key)
);
CREATE INDEX images_item ON images(item_id, kind, rank);

-- Manual matches. Keyed by folder, so they survive a rescan that rebuilds the item.
-- provider 'none' with an empty value: left unmatched on purpose.
CREATE TABLE overrides (
  root_id        INTEGER NOT NULL REFERENCES roots(id) ON DELETE CASCADE,
  path_key       TEXT NOT NULL,
  provider       TEXT NOT NULL,
  value          TEXT NOT NULL,
  season_offset  INTEGER NOT NULL DEFAULT 0,
  episode_offset INTEGER NOT NULL DEFAULT 0,
  numbering      TEXT,
  created_at     INTEGER NOT NULL,
  PRIMARY KEY (root_id, path_key)
) WITHOUT ROWID;

-- Raw provider responses, so that re-parsing never re-fetches.
CREATE TABLE provider_cache (
  provider   TEXT NOT NULL,
  key        TEXT NOT NULL,            -- normalised request path and query
  fetched_at INTEGER NOT NULL,
  status     INTEGER NOT NULL,
  body       BLOB,                     -- gzip
  PRIMARY KEY (provider, key)
) WITHOUT ROWID;

CREATE TABLE users (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE COLLATE NOCASE,
  pw_hash    BLOB,                     -- NULL = this user signs in by pairing only
  pw_salt    BLOB,
  pw_iter    INTEGER,
  is_admin   INTEGER NOT NULL DEFAULT 0,
  audio_lang TEXT,                     -- defaults for series without their own choice
  sub_lang   TEXT,
  sub_mode   TEXT NOT NULL DEFAULT 'auto' CHECK (sub_mode IN ('off','forced','auto','on')),
  created_at INTEGER NOT NULL
);

CREATE TABLE tokens (
  id           INTEGER PRIMARY KEY,
  hash         BLOB NOT NULL UNIQUE,   -- SHA-256 of the token; the token itself is never stored
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  device_id    TEXT NOT NULL,
  device_name  TEXT,
  client       TEXT,                   -- such as 'runnel-androidtv/1.0.0'
  created_at   INTEGER NOT NULL,
  last_used_at INTEGER,                -- written at most every ten minutes
  revoked_at   INTEGER
);
CREATE INDEX tokens_user ON tokens(user_id);

CREATE TABLE pairings (
  code             TEXT PRIMARY KEY,   -- shown on the device that wants in
  secret_hash      BLOB NOT NULL,      -- the secret stays with that device
  device_id        TEXT NOT NULL,
  device_name      TEXT,
  client           TEXT,
  created_at       INTEGER NOT NULL,
  expires_at       INTEGER NOT NULL,
  approved_user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
  claimed_at       INTEGER
) WITHOUT ROWID;

CREATE TABLE user_state (
  user_id        INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  item_id        INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  position_ms    INTEGER NOT NULL DEFAULT 0,
  played         INTEGER NOT NULL DEFAULT 0,
  play_count     INTEGER NOT NULL DEFAULT 0,
  last_played_at INTEGER,
  file_id        INTEGER REFERENCES files(id) ON DELETE SET NULL,  -- the version the position belongs to
  updated_seq    INTEGER NOT NULL,
  PRIMARY KEY (user_id, item_id)
) WITHOUT ROWID;
CREATE INDEX user_state_recent ON user_state(user_id, last_played_at DESC);
CREATE INDEX user_state_seq    ON user_state(user_id, updated_seq);

-- My list: films and series a person has put aside to watch.
CREATE TABLE user_list (
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  item_id     INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  listed_at   INTEGER,                 -- NULL = taken off the list; the row stays so that /changes can say so
  updated_seq INTEGER NOT NULL,
  PRIMARY KEY (user_id, item_id)
) WITHOUT ROWID;
CREATE INDEX user_list_recent ON user_list(user_id, listed_at DESC) WHERE listed_at IS NOT NULL;
CREATE INDEX user_list_seq    ON user_list(user_id, updated_seq);

CREATE TABLE series_prefs (
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  series_id   INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  audio_lang  TEXT,
  sub_lang    TEXT,
  sub_mode    TEXT,                    -- NULL in all three = reset: the series follows the user's defaults
  updated_seq INTEGER NOT NULL,
  PRIMARY KEY (user_id, series_id)
) WITHOUT ROWID;

-- Deleted items, so a client that was away learns what to drop. One transaction can delete
-- several items, all at its one sequence value, so the key is the pair.
CREATE TABLE tombstones (
  seq     INTEGER NOT NULL,
  item_id INTEGER NOT NULL,
  at      INTEGER NOT NULL,
  PRIMARY KEY (seq, item_id)
) WITHOUT ROWID;

-- Background work that must survive a restart.
CREATE TABLE tasks (
  id         INTEGER PRIMARY KEY,
  kind       TEXT NOT NULL,            -- 'probe','match','fetch','image','sidecar','extract','import'
  key        TEXT NOT NULL,            -- what it is about, such as 'file:123'
  library_id INTEGER REFERENCES libraries(id) ON DELETE CASCADE,  -- whose work it is, for progress
  payload    TEXT,                     -- JSON
  state      TEXT NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','running','done','failed')),
  attempts   INTEGER NOT NULL DEFAULT 0,
  run_after  INTEGER NOT NULL DEFAULT 0,
  error      TEXT,
  created_at INTEGER NOT NULL,
  UNIQUE (kind, key)
);
CREATE INDEX tasks_queue ON tasks(state, run_after, id);
CREATE INDEX tasks_progress ON tasks(kind, library_id, state);

-- One row per playback start: the evidence for the speed goals.
CREATE TABLE play_log (
  id                   INTEGER PRIMARY KEY,
  at                   INTEGER NOT NULL,
  user_id              INTEGER,
  item_id              INTEGER,
  file_id              INTEGER,
  mode                 TEXT NOT NULL,   -- 'direct','transcode','sidecar'
  warmed               INTEGER NOT NULL DEFAULT 0,
  server_first_byte_ms INTEGER,         -- transcodes: request to first byte sent
  client_loaded_ms     INTEGER,         -- Play pressed to the player's file-loaded event
  client_playing_ms    INTEGER,         -- Play pressed to the first frame
  detail               TEXT             -- JSON, a snapshot: device, client, title, decoder, encoder, speed, dropped frames
);

-- Full-text search over titles. Trigram tokens make it a substring search.
CREATE VIRTUAL TABLE items_fts USING fts5(
  title, original_title, aliases,
  content='', contentless_delete=1, tokenize='trigram'
);
```

Migration `0002_images_key.sql`, because `GET /img` finds an image by its key, and without an index that read every row of `images`. With 40,000 rows, a request for the key in the last row took a median of 5.5 ms, against 0.5 ms for the first; with the index, 0.5 ms for either (measured in a development sandbox, 22 November 2026). The migration rebuilds no table:

```sql
CREATE INDEX images_key ON images(key);
```

Migration `0003_kinds.sql`, for the two kinds of library of D49, `mixed` and `videos`, and the item type of a home video, `video`. Both lists are CHECK constraints, which SQLite cannot change in place, so the migration makes `libraries` and `items` again by the procedure of section 7. Every column, row and id is copied as it was, and the indexes of `items` are made again:

```sql
CREATE TABLE new_libraries (
  id          INTEGER PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  kind        TEXT NOT NULL CHECK (kind IN ('movies','shows','mixed','videos')),
  profile     TEXT NOT NULL DEFAULT 'default' CHECK (profile IN ('default','anime')),
  created_at  INTEGER NOT NULL,
  updated_seq INTEGER NOT NULL DEFAULT 0
);
INSERT INTO new_libraries (id, name, kind, profile, created_at, updated_seq)
  SELECT id, name, kind, profile, created_at, updated_seq FROM libraries;
DROP TABLE libraries;
ALTER TABLE new_libraries RENAME TO libraries;

CREATE TABLE new_items (
  id               INTEGER PRIMARY KEY,
  library_id       INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
  type             TEXT NOT NULL CHECK (type IN ('movie','series','season','episode','collection','video')),
  parent_id        INTEGER REFERENCES items(id) ON DELETE CASCADE,  -- season -> series, episode -> season
  series_id        INTEGER REFERENCES items(id) ON DELETE CASCADE,  -- seasons and episodes
  season_number    INTEGER,
  episode_number   INTEGER,
  episode_number_end INTEGER,                    -- a file that holds several episodes
  absolute_number  INTEGER,
  numbering        TEXT CHECK (numbering IN ('seasons','absolute')), -- series only
  root_id          INTEGER REFERENCES roots(id) ON DELETE CASCADE,   -- movies and series
  collection_id    INTEGER REFERENCES items(id) ON DELETE SET NULL,  -- movies: the collection shown for it
  path_key         TEXT,                         -- the folder (or lone file) the item was built from
  title            TEXT NOT NULL,
  sort_title       TEXT NOT NULL,
  original_title   TEXT,
  overview         TEXT,
  release_date     TEXT,
  year             INTEGER,
  runtime_ms       INTEGER,
  rating           REAL,
  content_rating   TEXT,
  match_state      TEXT NOT NULL DEFAULT 'unmatched'
                   CHECK (match_state IN ('unmatched','matched','manual','review','ignored')),
  match_confidence REAL,
  match_note       TEXT,                         -- JSON from the matcher: why the item is on the review list
  added_at         INTEGER NOT NULL,
  refreshed_at     INTEGER,
  updated_seq      INTEGER NOT NULL
);
INSERT INTO new_items (id, library_id, type, parent_id, series_id, season_number, episode_number, episode_number_end, absolute_number, numbering, root_id, collection_id, path_key, title, sort_title, original_title, overview, release_date, year, runtime_ms, rating, content_rating, match_state, match_confidence, match_note, added_at, refreshed_at, updated_seq)
  SELECT id, library_id, type, parent_id, series_id, season_number, episode_number, episode_number_end, absolute_number, numbering, root_id, collection_id, path_key, title, sort_title, original_title, overview, release_date, year, runtime_ms, rating, content_rating, match_state, match_confidence, match_note, added_at, refreshed_at, updated_seq FROM items;
DROP TABLE items;
ALTER TABLE new_items RENAME TO items;
CREATE UNIQUE INDEX items_path   ON items(root_id, path_key) WHERE path_key IS NOT NULL;
CREATE INDEX items_list   ON items(library_id, type, sort_title, id);
CREATE INDEX items_added  ON items(library_id, type, added_at DESC);
CREATE INDEX items_parent ON items(parent_id, season_number, episode_number);
CREATE INDEX items_series ON items(series_id, type, season_number, episode_number);
CREATE INDEX items_seq    ON items(updated_seq);
CREATE INDEX items_collection ON items(collection_id) WHERE collection_id IS NOT NULL;
```

## 4. Rules that keep it consistent

**Files**

- A file's identity is `(root_id, rel_path)`. Its `fingerprint` is the size plus a hash of the first and last 64 KiB: two small reads.
- A scan that finds a new path whose fingerprint equals that of a file of the same root with `missing_since` set, or one that this scan did not see where it was, treats it as a move: it updates `rel_path` and clears `missing_since`. The file keeps its id, its probe, its items and its watch state.
- A changed `size` or `mtime_ns` resets `probe_version` to 0 and queues a probe.
- `probe_version` is a number in the code. Raising it re-probes every file once, after a change to how probe output is mapped.
- A root that cannot be listed, or lists empty when it held files before, is marked `available = 0` and skipped. None of its files is marked missing. Nothing in a folder below a root that cannot be read is marked missing either.
- A root going away or coming back bumps its library's `updated_seq`, and nothing else. That one record is how a client knows that everything under the folder is unavailable (`04-api.md`, sections 2.1 and 5).
- A file that is gone from a healthy root gets `missing_since`. It stays, shown as unavailable, for 30 days. After that the file row goes, and any item left with no file goes with it.

**Images**

- A local image has `source = 'local'` and `rank` 0. Its `source_ref` holds the root, the path below it, the size and the time of the file it was made from, so that a scan can tell an unchanged file without reading it.
- Writing an item's image of a kind replaces its other local image of that kind, and bumps the item's `updated_seq`. So does removing one. The files of a key that no image has any more are deleted: at once when an image is replaced or removed, and when its item went, by a sweep of `images/` at start and each night. The sweep leaves files younger than an hour, since an image task writes its files before the row that names them, and keys that a person's photo has.

**External tracks**

- A row of `external_tracks` is a subtitle or audio file beside a video, named for it (`06-library-and-metadata.md`, section 4.7). Its `id` is the `external_id` that clients fetch it by, so a row that stays keeps it.
- A scan of a folder replaces the rows of the videos in it with what it found there, writing only what differs. A row that came, went or changed bumps the `updated_seq` of its video's items.

**Items**

- Movies and series are keyed by `(root_id, path_key)`: the folder they were built from, or the lone file for a film that has no folder.
- Seasons belong to a series by `parent_id`. Episodes belong to a season by `parent_id` and carry `series_id` as well, so episode queries need no join through seasons.
- Season 0 holds specials. Sorting by `(season_number, episode_number)` with season 0 last is the display order.
- `numbering = 'absolute'` on a series means its files carry one running count. `absolute_number` is that count, and the episode's key. `season_number` and `episode_number` then hold the provider's numbers once they are mapped, and until then the season the file is in, or 1, and the running number (`06-library-and-metadata.md`, section 5).
- An episode's key is `(series, season_number, episode_number)`, or `(series, absolute_number)` where that is set. Building keeps an episode's id while its key is wanted, and otherwise lets it follow its files (`06-library-and-metadata.md`, section 5).
- A file that holds episodes 1 and 2 is one episode item with `episode_number = 1` and `episode_number_end = 2`.
- Two files that resolve to the same item are versions: two rows in `item_files` with different labels, the main version's NULL. The parts of one version are rows with the same label and `part` 1 to n.
- Rebuilding items from files must be idempotent. Running it twice changes nothing and bumps no `updated_seq`.
- An item that no file makes any more is deleted when items are built, with a tombstone at a sequence value of the transaction. One transaction can delete several items, so `tombstones` is keyed by the sequence value and the item together. Taking a folder out of a library deletes the items built from it in the same way.
- An item's id is never given again once the item is gone, and neither is the id of a library, a library folder, a user or a token. SQLite gives a new row the highest id plus one, so the id of a row that had the highest would come back. A client that missed the removal, work queued for the old row, or a call made from a page loaded before would take the new one for it. `meta.last_item_id`, `last_library_id`, `last_root_id`, `last_user_id` and `last_token_id` hold the highest ids given.
- A collection is an item of type `collection`. It has a library and no folder: its `root_id` and `path_key` are NULL.

**Collections**

- A film whose metadata names a collection gets an `external_ids` row with provider `tmdb_collection`. The row is there whether or not a collection is shown.
- A library shows a collection when it holds two or more films with the same `tmdb_collection` value (D33). The collection item is made in that library then, with the same `external_ids` row, and those films point at it with `collection_id`.
- When fewer than two of its films are left, the collection item is deleted, with a tombstone, and `collection_id` is cleared on the film that remains.
- Setting or clearing `collection_id` bumps the film's `updated_seq`.
- A collection's films are ordered by `release_date`, then `sort_title`.

**People and credits**

- A `people` row is one person at the provider.
- An item's `credits` rows are replaced as a whole when its metadata is fetched: at most 20 of the cast in billing order, then directors, then writers, with a series' creators first among the writers (P7). `ord` is the order shown.
- Replacing an item's credits bumps its `updated_seq`.
- A photo is not downloaded when an item is matched. `image_key` is derived from the provider's path at that time, so the photo's URL exists at once. The photo is fetched when a client first asks for it, and `image_fetched_at` and `image_color` are set then. After a try that failed, `image_tried_at` keeps the server from asking again for a day (`04-api.md`, section 6).
- A person with no credits left is deleted by the nightly cleanup.

**Manual matches**

- A manual match writes `overrides`, keyed by folder. Matching reads `overrides` before anything else. A rescan that drops and rebuilds an item therefore finds the same match again.
- A row with provider `none` means the item was left unmatched on purpose. Matching skips that folder, and the item's `match_state` is `ignored`.

**Watch state**

- State hangs on items, never on files, so replacing a file with a better copy keeps the position. `file_id` is the file the position was saved from: its version, and for an item in parts the part that was playing.
- `played = 1` sets `position_ms = 0`, and a position that is kept sets `played = 0`. The play count stays either way. The thresholds that turn progress into `played` are in `04-api.md`, section 7.6.
- Only films and episodes have rows. A series' and a season's `unplayed` counts are the user's and are counted when asked for, so a row changes nothing on any item, and no item's `updated_seq` moves for it.
- Every row written is stamped like any other (section 5). Marking a series played writes a row for each of its episodes, so it can take several sequence values.

**My list**

- One row per user and item, for films and series only.
- Taking an item off the list keeps the row, sets `listed_at` to NULL and stamps a new `updated_seq`, so that `/changes` can say it went. Such a row is deleted once its `updated_seq` is at or below `meta.tombstone_floor`.
- Resetting a series' track choice works the same way: the `series_prefs` row stays, with its three choices NULL.

**Settings and sign-in**

- What the server needs in order to start is a flag or an environment variable, and is not in the database (D17).
- `meta` holds, besides its counters: `instance_id` (made at the first start), `setup_code` (only while no user exists), `server_name`, `tmdb_key`, `tmdb_key_ok` and `tmdb_key_checked_at` (D34, D35).
- A value that the environment sets is used as it is and is never written to `meta`.
- `tmdb_key` is stored in plain text. Whoever can read the database file can read it. The API never returns it.
- `setup_code` is made at a start without users and kept by later ones, and is deleted in the transaction that makes the first user, whichever call makes them.
- The local admin token of the subcommands is not in the database. The server keeps its hash in memory (`02-architecture.md`, section 6).
- `tokens.last_used_at` is written at most once in ten minutes for a token (P7). Otherwise every request would be a write.
- A pairing is deleted an hour after it expires or is claimed. Until then a lookup can say that the code has expired. The deleting is done when a pairing starts, and a row older than the hour answers as if it were gone already.

**Background work and the start-time log**

- A task carries the library it works for, where it has one. Removing a library removes its queued work.
- The Jellyfin import's task, of no library, keeps its request in `payload`, the Jellyfin key with it, until its run ends. Its done row then goes like any other.
- A `done` row stays until no row of its kind and library is queued or running. Then all of them are deleted together. Progress is counted from that: the `done` rows out of all rows.
- A `failed` row stays until it is retried or cancelled. A failed probe of a file that has changed since goes when the scan sees the change, and the file is probed afresh.
- A row left `running` by a server that stopped is queued again at the next start, and that run does not count in `attempts`.
- A probe queued again while it runs, for a file that changed meanwhile, stays queued when the run ends, and runs again.
- `play_log` keeps its newest 1,000 rows (P7). Inserting a row deletes what is older.
- A `play_log` row comes from the first progress report of a playback that carries timings or stats (`04-api.md`, section 7.6). Its `mode` is the report's, so it can also be `remux` (section 7.9 there), which the comment in migration 0001 leaves out. When the session had a transcode, `server_first_byte_ms`, the encoder in `detail` and `warmed` come from the session's last stream (`05-playback.md`, section 5). `detail` keeps the user's name, the device's name and client, the item's title and the client's stats as they were, so that the row still reads the same after the device signs out or the item goes. Removing a user sets `user_id` to NULL in their rows.

**Search index**

- `items_fts` rows use the item id as `rowid`. Writing an item deletes its row and inserts the new one in the same transaction.
- `aliases` holds alternative titles, one per line.
- Trigram search needs three characters. Shorter queries fall back to a prefix match on `sort_title`.

## 5. The change sequence

Clients keep a local copy of the library and ask "what changed since I last looked". One counter answers that.

- `meta.seq` is a counter. Every transaction that changes something a client can see takes the next value and stamps it on the rows it touched: `items.updated_seq`, `user_state.updated_seq`, `user_list.updated_seq`, `series_prefs.updated_seq`, `libraries.updated_seq`.
- Deleting an item writes a row to `tombstones` with the transaction's value.
- **One value is on at most 500 rows.** A transaction that stamps more, such as the first build of a large library, takes the next value for every 500 rows it stamps, counting tombstones and the rows of every table together. A page of `/changes` never splits a value, so this is what keeps a page bounded: at most its limit, or 500 rows when the limit is lower (`04-api.md`, section 5). The 500 is `db.StampBatch`.
- Within a transaction, a parent is written before its children and children are deleted before their parent. A client that applies a page before the rest of a large transaction has arrived then never holds an episode without its season.
- A client that last saw sequence S asks for items, state and tombstones above S.
- Tombstones older than 90 days are pruned, at start and each night before the walk at 04:00, and `meta.tombstone_floor` moves up to the highest pruned value. A client whose S is below the floor is told to start again from a full copy. The same pruning deletes the rows of `user_list` taken off and of `series_prefs` reset at or below the floor (section 4).
- A transaction may take a value and stamp no row, when what changed shows only through other rows: a user's track defaults, which a detail's `prefs` applies. That moves the `ETag`s (`04-api.md`, section 1).
- An image change, a genre change, a credits change or a file change bumps the `updated_seq` of the item it belongs to. That includes a file going missing or coming back. A new episode also bumps its season and series, because their counts changed.
- A root going away or coming back bumps its library, not the items under it.
- Removing a library writes no tombstones. `/changes` lists the libraries that exist, and a client drops a removed library together with its items.

Taking the next value, inside the write transaction:

```sql
UPDATE meta SET value = CAST(value AS INTEGER) + 1 WHERE key = 'seq' RETURNING CAST(value AS INTEGER) AS seq;
```

There is no separate change log table. The earlier sketch had one; stamping rows needs less code and cannot grow without bound.

## 6. Queries the API is built on

Named parameters are as the server binds them.

**A page of a library**, sorted by title, continuing after the last row of the page before:

```sql
SELECT id, type, title, sort_title, year, runtime_ms, updated_seq
FROM items
WHERE library_id = :library AND type = :type
  AND (sort_title, id) > (:after_title, :after_id)
ORDER BY sort_title, id
LIMIT :limit;
```

**The same page by position**, for a grid that jumps to a letter:

```sql
SELECT id, type, title, sort_title, year, runtime_ms, updated_seq
FROM items
WHERE library_id = :library AND type = :type
ORDER BY sort_title, id
LIMIT :limit OFFSET :offset;
```

**Letters**: for a library sorted by title, which letters have titles, how many, and where each begins. `#` is everything that sorts before `a`, `~` everything that sorts after `z`.

```sql
WITH letters AS (
  SELECT CASE WHEN substr(sort_title, 1, 1) < 'a' THEN '#'
              WHEN substr(sort_title, 1, 1) > 'z' THEN '~'
              ELSE upper(substr(sort_title, 1, 1)) END AS letter,
         COUNT(*) AS n,
         MIN(sort_title) AS first
  FROM items
  WHERE library_id = :library AND type = :type
  GROUP BY letter
)
SELECT letter, n,
       COALESCE(SUM(n) OVER (ORDER BY first ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING), 0) AS starts_at
FROM letters
ORDER BY first;
```

**Continue watching**: started, not finished, most recent first.

```sql
SELECT i.id, i.type, i.title, i.series_id, i.season_number, i.episode_number,
       s.position_ms, s.last_played_at
FROM user_state s JOIN items i ON i.id = s.item_id
WHERE s.user_id = :user AND s.played = 0 AND s.position_ms > 0
ORDER BY s.last_played_at DESC
LIMIT :limit;
```

**Next up**: for each series, the first unplayed episode after the one played most recently. Specials are skipped.

```sql
WITH last_played AS (
  SELECT e.series_id, e.season_number AS s, e.episode_number AS n, us.last_played_at AS t,
         ROW_NUMBER() OVER (PARTITION BY e.series_id
                            ORDER BY us.last_played_at DESC, e.season_number DESC, e.episode_number DESC) AS r
  FROM user_state us JOIN items e ON e.id = us.item_id
  WHERE us.user_id = :user AND us.played = 1 AND e.type = 'episode'
)
SELECT nx.id, nx.series_id, nx.season_number, nx.episode_number, nx.title, lp.t AS series_last_played_at
FROM last_played lp
JOIN items nx ON nx.id = (
  SELECT c.id FROM items c
  WHERE c.series_id = lp.series_id AND c.type = 'episode' AND c.season_number > 0
    AND (c.season_number, c.episode_number) > (lp.s, lp.n)
    AND NOT EXISTS (SELECT 1 FROM user_state u
                    WHERE u.user_id = :user AND u.item_id = c.id AND (u.played = 1 OR u.position_ms > 0))
  ORDER BY c.season_number, c.episode_number
  LIMIT 1)
WHERE lp.r = 1
ORDER BY lp.t DESC
LIMIT :limit;
```

An episode that is already started is left out here because Continue watching shows it. Episodes marked played together share one `last_played_at`, so the latest of them in order counts as the one played most recently. A series' `resume` (`04-api.md`, section 2.2) asks the same question for one series.

**Recently added**: films as they are, episodes folded into their series. `:since` is the start of the day 30 days back (P7), so that Home's `ETag` can carry it.

```sql
SELECT id, type, title, added_at, 1 AS new_count
FROM items WHERE type = 'movie' AND added_at > :since
UNION ALL
SELECT s.id, s.type, s.title, MAX(e.added_at) AS added_at, COUNT(*) AS new_count
FROM items e JOIN items s ON s.id = e.series_id
WHERE e.type = 'episode' AND e.added_at > :since
GROUP BY s.id
ORDER BY added_at DESC
LIMIT :limit;
```

**Episodes of a season with the user's state:**

```sql
SELECT e.id, e.episode_number, e.episode_number_end, e.absolute_number, e.title, e.runtime_ms,
       COALESCE(s.position_ms, 0) AS position_ms, COALESCE(s.played, 0) AS played
FROM items e LEFT JOIN user_state s ON s.item_id = e.id AND s.user_id = :user
WHERE e.parent_id = :season
ORDER BY e.episode_number;
```

**Unplayed count of a series:**

```sql
SELECT COUNT(*) AS unplayed
FROM items e
WHERE e.series_id = :series AND e.type = 'episode' AND e.season_number > 0
  AND NOT EXISTS (SELECT 1 FROM user_state s WHERE s.user_id = :user AND s.item_id = e.id AND s.played = 1);
```

**The files of an item with their streams**, as the detail response needs them:

```sql
SELECT f.id, f.container, f.size, f.duration_ms, f.bit_rate, f.attachments_bytes, f.missing_since,
       l.label, l.part,
       st.idx, st.kind, st.codec, st.profile, st.lang, st.title, st.is_default, st.is_forced,
       st.width, st.height, st.bit_depth, st.fps_num, st.fps_den, st.hdr, st.channels
FROM item_files l
JOIN files f ON f.id = l.file_id
LEFT JOIN streams st ON st.file_id = f.id AND st.kind <> 'attachment'
WHERE l.item_id = :item
ORDER BY l.part, f.id, st.idx;
```

**Whether an item's own files are there**, for `available` on a card. For a series the same test runs over its episodes.

```sql
SELECT EXISTS (
  SELECT 1 FROM item_files l JOIN files f ON f.id = l.file_id
  WHERE l.item_id = :item AND f.missing_since IS NULL
) AS available;
```

**My list**, newest first:

```sql
SELECT i.id, i.type, i.title, i.year, l.listed_at
FROM user_list l JOIN items i ON i.id = l.item_id
WHERE l.user_id = :user AND l.listed_at IS NOT NULL
ORDER BY l.listed_at DESC
LIMIT :limit;
```

**The collections a library should show**: those with two or more of their films in it.

```sql
SELECT x.value AS provider_collection, COUNT(*) AS films
FROM external_ids x JOIN items i ON i.id = x.item_id
WHERE x.provider = 'tmdb_collection' AND i.type = 'movie' AND i.library_id = :library
GROUP BY x.value
HAVING COUNT(*) >= 2;
```

**The films of a collection**, in order, with the user's state:

```sql
SELECT i.id, i.title, i.release_date, i.runtime_ms,
       COALESCE(s.position_ms, 0) AS position_ms, COALESCE(s.played, 0) AS played
FROM items i LEFT JOIN user_state s ON s.item_id = i.id AND s.user_id = :user
WHERE i.collection_id = :collection
ORDER BY i.release_date, i.sort_title;
```

**The credits of an item**, in the order shown:

```sql
SELECT c.ord, c.kind, c.role, c.department, c.episodes,
       p.id AS person_id, p.name, p.image_key, p.image_color
FROM credits c JOIN people p ON p.id = c.person_id
WHERE c.item_id = :item
ORDER BY c.ord;
```

**A person's titles**, newest first. Someone credited twice on one item appears once, under the credit that is shown first.

```sql
SELECT i.id, i.type, i.title, i.year, c.kind, c.role, c.episodes, MIN(c.ord) AS ord
FROM credits c JOIN items i ON i.id = c.item_id
WHERE c.person_id = :person
GROUP BY i.id
ORDER BY i.release_date DESC, i.id;
```

**Genres across every library**, with the libraries that hold each:

```sql
SELECT g.genre, COUNT(*) AS items, group_concat(DISTINCT i.library_id) AS libraries
FROM item_genres g JOIN items i ON i.id = g.item_id
WHERE i.type IN ('movie','series')
GROUP BY g.genre
ORDER BY g.genre;
```

**Series with an unplayed episode**, for a shows library's `unplayed`:

```sql
SELECT COUNT(DISTINCT e.series_id) AS unplayed
FROM items e
WHERE e.library_id = :library AND e.type = 'episode' AND e.season_number > 0
  AND NOT EXISTS (SELECT 1 FROM user_state s WHERE s.user_id = :user AND s.item_id = e.id AND s.played = 1);
```

**Search.** The query text is wrapped in double quotes by the server, so user input is matched as a phrase and never parsed as FTS syntax.

```sql
SELECT i.id, i.type, i.title, i.year
FROM items_fts f JOIN items i ON i.id = f.rowid
WHERE items_fts MATCH :q AND i.type IN ('movie','series')
ORDER BY rank
LIMIT :limit;
```

**What changed since a sequence**: one read for each kind of row. `series_prefs` and `libraries` are read the same way.

```sql
SELECT id, type, title, updated_seq FROM items WHERE updated_seq > :since ORDER BY updated_seq LIMIT :limit;
```

```sql
SELECT item_id, position_ms, played, play_count, last_played_at, updated_seq
FROM user_state WHERE user_id = :user AND updated_seq > :since ORDER BY updated_seq LIMIT :limit;
```

```sql
SELECT item_id, listed_at, updated_seq
FROM user_list WHERE user_id = :user AND updated_seq > :since ORDER BY updated_seq LIMIT :limit;
```

```sql
SELECT seq, item_id FROM tombstones WHERE seq > :since ORDER BY seq LIMIT :limit;
```

**The scanner's comparison set** for one root:

```sql
SELECT id, rel_path, size, mtime_ns, missing_since FROM files WHERE root_id = :root;
```

**Progress of background work**, one row for each kind and library:

```sql
SELECT kind, library_id,
       SUM(state = 'done') AS done, COUNT(*) AS total,
       SUM(state = 'failed') AS failed, SUM(state = 'running') AS running,
       MIN(created_at) AS since
FROM tasks
GROUP BY kind, library_id;
```

**Trimming the start-time log** to its newest 1,000 rows:

```sql
DELETE FROM play_log WHERE id <= (SELECT MAX(id) FROM play_log) - 1000;
```

## 7. Migrations

- Numbered SQL files in the binary (`embed`). `meta.schema_version` records the last one applied.
- At start the server applies what is missing, each file in its own transaction, and refuses to start on a database newer than itself.
- A merged migration is never edited, not even a comment in it. A database made with it may exist anywhere, and the server never applies a migration twice, so an edit would go unnoticed where it has run. A change to the schema is a new migration. A test holds every merged migration to the SHA-256 it was merged with, and a new migration adds its own in the pull request that adds it.
- A migration never rewrites watch state in a way that cannot be reversed from a backup. Before a migration that rebuilds a table, the server copies the database file into `backup/` in the data directory (`02-architecture.md`, section 7):
  - **When.** At start, before the first pending migration whose header says `-- Rebuilds:`, and only for a database that held a schema already. A new database is not copied. One copy covers every migration of that start.
  - **How.** `VACUUM INTO`, which writes a consistent snapshot of the database into a new file [checked: SQLite's page on VACUUM, 27 November 2026]. It is written as `….partial`, readable by the server's own user only, synced, and renamed once complete, so a copy cut short by a crash or a power loss is never taken for a backup. Partial files left by an earlier start are deleted.
  - **Its name**: `backup/runnel-schema<N>-<YYYYMMDD-HHMMSS>.db`, `N` the schema version it holds and the time in UTC. It is an ordinary database: copied back over `runnel.db`, with the `-wal` and `-shm` files removed, it is the server as it was before, and an older release of the server opens it.
  - **How many.** The newest three. Older ones are deleted after a new copy is made.
  - **Without room.** The copy and the rebuilt tables need about twice the database's size free in the data directory. Without it, or when the copy cannot be written, the server refuses to start, says how much room it needs and where, and changes nothing.
- **A migration that rebuilds a table that others point at** follows SQLite's procedure for it [checked: SQLite's page on ALTER TABLE, section "Making Other Kinds Of Table Schema Changes", 27 November 2026]. Foreign keys are turned off first, on the writing connection and outside any transaction, since the pragma does nothing inside one. Then, in one transaction: the new table is made under a new name, filled from the old, the old one dropped, the new one renamed to the old name, and its indexes made again; `PRAGMA foreign_key_check` must find nothing, or the transaction is rolled back and the server does not start. Then foreign keys are turned on again. With them on, dropping `items` would delete every row that points at it, watch state among them. Since SQLite 3.26.0 a renamed table's foreign key references are always rewritten, so a table that others point at is never renamed out of the way first: that is the order the procedure warns against.
- Anything derived from the media or from providers (`files`, `streams`, `chapters`, `items`, `images`, `people`, `credits`, `provider_cache`) can be rebuilt by a rescan. Only `users`, `tokens`, `user_state`, `user_list`, `series_prefs`, `overrides` and the settings in `meta` are irreplaceable. A backup is those, or the whole file.

## 8. Sizes to expect

- A test database with 14,720 file rows and nothing else was 3.1 MB.
- A prototype with 10,741 items, 10,491 files and 52,455 stream rows was 9.6 MB.
- People and credits, on synthetic rows: 30,000 people and 78,360 credits (3,300 films and 240 series with 22 to 24 credits each) added 9.2 MB to the file. Reading one item's credits took 0.04 ms. How many different people a real library credits is not known, so the 30,000 is an assumption.
- The image cache and the sidecar cache are files on disk, not rows. See `02-architecture.md`.
