-- SPDX-License-Identifier: GPL-3.0-or-later
--
-- Migration 0001: the whole schema of docs/03-data-model.md, section 3.
-- Keep the two in step: a change here is a change there, in the same pull request.

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
