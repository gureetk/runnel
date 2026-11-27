-- SPDX-License-Identifier: GPL-3.0-or-later
--
-- Migration 0003: two more kinds of library, mixed and home videos, and the
-- item type of a home video (D49; docs/03-data-model.md, section 3). Both are
-- CHECK constraints, so libraries and items are rebuilt by SQLite's
-- procedure for a table that others point at: the server turns foreign keys
-- off around it and copies the database into backup/ first (section 7).
-- Rebuilds: libraries, items

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
