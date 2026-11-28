# API, version 1

The contract between the server and its clients. The clients are Runnel's own: the web interface, the Android TV app, the phone and tablet apps and the desktop apps (`10-clients.md`). Nothing here is private to them, so any other client can use the same calls. The server implements it stage by stage, as `01-roadmap.md` lists, and answers `404` for a call that its stage has not brought yet.

Table and column names refer to `03-data-model.md`. This document and the database say `user` where the interfaces say person.

**Revised, 20 November 2026.** What the client designs asked of the server, and the review accepted (`11-design-review.md`, section 6), is now here:

- fuller cards, and folders that are away (2.1, 4.1, 5)
- collections, cast and crew (2.3, 2.4, 4.3, 4.8)
- the server's name and the first run (3.1, 3.4), looking up a pairing code (3.3), a person's own account (3.5), finding a server (3.6, proposed)
- My list (4.7), letters without a local copy (4.3), genres across libraries (4.6)
- marking several items and Undo (7.7), resetting a series' track choice (7.8)
- most of administration (section 8)

Added the same day, after the research on browsers (D39, D40): playing in a browser (7.9), and what an app needs before it can look for a server (3.6).

## 1. Conventions

| Topic | Rule |
|---|---|
| Base | `http://<host>:8610/api/v1`. The port is a setting. TLS, if wanted, is a reverse proxy's job: a client is then given an `https://` address and appends `/api/v1` to it. Tokens travel in requests, so plain HTTP is for a trusted network (D32). |
| Format | JSON, UTF-8. Field names are `snake_case`. Unknown fields must be ignored by clients. |
| Identifiers | Integers, as stored. Stable for the life of one server database. |
| Time | Unix milliseconds. Durations and positions are milliseconds. Dates are `YYYY-MM-DD`. |
| Authentication | `Authorization: Bearer <token>`. Media and image URLs also take `?t=<token>`, for players that cannot send headers; other calls do not. The server never logs a query string. A token is one person on one device. The subcommands use the server's local admin token instead (section 8). |
| Errors | An HTTP status and a body `{"error":{"code":"…","message":"…"}}`. Codes are in section 9. |
| Wrong tries | A call that takes a secret (a password, a setup code, a pairing code) answers `429` with code `too_many` and a `Retry-After` header after too many wrong tries. The limit is given with each call. |
| Paging | `limit` and `cursor`. A response carries `next`, a cursor for the following page, or `null`. Cursors are opaque. |
| Caching | `GET /home`, `GET /items` and `GET /items/{id}` send a weak `ETag` built from the server's instance, the change sequence and the user, with `Cache-Control: private, no-cache`. `If-None-Match` gets `304` when nothing changed. Other JSON is sent with `Cache-Control: no-store`. Images are immutable and cache forever. |
| Compression | JSON is gzip-compressed when the request allows it. Media is never compressed. |
| Versioning | `/api/v1` only grows: new endpoints, new optional parameters, new response fields. Anything else is `/api/v2`. `GET /server` lists `features` so a client can ask before it uses something newer. |

The client decides how to play a file. The server states facts about files and does what it is asked. There is no playback negotiation call and no device profile on the server.

## 2. Objects

### 2.1 Item card

The short form of an item, used in every list. A client's local copy is made of cards (section 5), so a card carries the text a screen draws at once (D36).

```json
{
  "id": 4812,
  "type": "episode",
  "library_id": 2,
  "title": "The Quiet Morning",
  "overview": "A storm closes the harbor.",
  "year": 2021,
  "date": "2021-04-12",
  "runtime_ms": 1440000,
  "rating": 7.9,
  "content_rating": "TV-14",
  "series_id": 377,
  "series_title": "Harbor Lantern",
  "season": 1,
  "episode": 5,
  "episode_end": null,
  "absolute": null,
  "available": true,
  "images": {
    "thumb": {"key": "5b1c0e9a3f6d4c7e8a2b9d0f1e3c5a7b", "w": [300], "ar": 1.778, "color": "#1b2a3c"}
  },
  "state": {"position_ms": 512000, "played": false, "play_count": 0, "last_played_at": 1795200000000},
  "added_at": 1795100000000,
  "seq": 1259
}
```

A film's card, to show the fields an episode does not have:

```json
{
  "id": 5031,
  "type": "movie",
  "library_id": 1,
  "root_id": 1,
  "title": "Paper Comets",
  "overview": "Two sisters chart a comet from a rooftop.",
  "year": 2019,
  "date": "2019-03-08",
  "runtime_ms": 6240000,
  "rating": 7.2,
  "content_rating": "PG",
  "genres": ["Drama", "Family"],
  "collection_id": 5120,
  "available": true,
  "images": {
    "poster": {"key": "0e9a3f6d4c7e8a2b9d0f1e3c5a7b5b1c", "w": [342, 500], "ar": 0.667, "color": "#22313f"},
    "backdrop": {"key": "3f6d4c7e8a2b9d0f1e3c5a7b5b1c0e9a", "w": [1280], "ar": 1.778, "color": "#0e1622"}
  },
  "state": {"position_ms": 1980000, "played": false, "play_count": 0, "last_played_at": 1795200000000},
  "listed_at": 1795150000000,
  "added_at": 1795100000000,
  "seq": 1244
}
```

| Field | Notes |
|---|---|
| `type` | `movie`, `series`, `season`, `episode` or `collection` (2.3). |
| `overview`, `date`, `runtime_ms`, `rating`, `content_rating` | The same values as on the detail. Absent until metadata has arrived (`06-library-and-metadata.md`, section 6.3), and absent again when it is dropped after 180 days without a refresh (D53). `rating` is the provider's average of its users' votes, out of 10. `content_rating` is the certification of one country (6.3 there). |
| `genres` | Films and series only, by name. Absent until metadata has arrived. |
| `series_id`, `series_title`, `season`, `episode`, `episode_end`, `absolute` | Seasons and episodes only. `episode_end` is set when one file holds several episodes. `absolute` is set for series numbered in one running count. `episode` is null for an episode without a number (D51): a file whose name gives none, or only a decimal such as `08.5`, titled from its name. |
| `root_id` | Films and series only: the library folder the item was built from. Seasons and episodes are in their series' folder. |
| `available` | False when none of the item's own files is there. For a series or a season: none of its episodes' files. A folder that is away does not change it: that is said once, for the folder, in `GET /libraries` and `/changes`. A client shows an item as unavailable when either says so. |
| `collection_id` | Films only: the collection the film belongs to, when the library shows one (2.3). |
| `images` | By kind: `poster`, `backdrop`, `thumb`, `logo`. Each has a `key`, the widths stored, the aspect ratio and a placeholder colour. The URL is `/img/{key}/{w}`. A kind that is missing is absent. |
| `state` | The signed-in user's, on films and episodes, also before they are played: `position_ms` 0, `played` false, `play_count` 0, `last_played_at` null. `position_ms` is from the start of the item, across its parts (7.6). Absent on seasons and collections. On a series it is replaced by `counts`. |
| `counts` | Series and seasons: `{"episodes": 24, "unplayed": 20, "seasons": 2}`. Collections: `{"films": 2}`. A series counts its episodes and seasons without the specials. The specials' season counts its own episodes. `unplayed` is the signed-in user's: the episodes counted that are not played. |
| `listed_at` | Films and series only: when the signed-in user put the item on My list (4.7). Absent when it is not on it. |
| `seq` | The change sequence at which this card last changed. |

`state`, `listed_at` and the `unplayed` count belong to the signed-in user. Everything else on a card is the same for everyone.

### 2.2 Item detail

Everything on the card, and what a detail screen and the player need besides: the files with their streams and chapters, the track choice, the credits and the neighbours. It is the playback contract: with this response a client can start playing with one more request. A client keeps a detail once it has fetched it, so a page opened before draws in full the next time, also while the server is away (D36).

```json
{
  "id": 4812,
  "type": "episode",
  "library_id": 2,
  "title": "The Quiet Morning",
  "original_title": null,
  "overview": "A storm closes the harbor.",
  "year": 2021,
  "date": "2021-04-12",
  "runtime_ms": 1440000,
  "rating": 7.9,
  "content_rating": "TV-14",
  "series_id": 377,
  "series_title": "Harbor Lantern",
  "season": 1,
  "episode": 5,
  "episode_end": null,
  "absolute": null,
  "available": true,
  "external_ids": {"tmdb": "123456"},
  "match": {"state": "matched", "confidence": 0.97},
  "images": {
    "thumb": {"key": "5b1c0e9a3f6d4c7e8a2b9d0f1e3c5a7b", "w": [300], "ar": 1.778, "color": "#1b2a3c"},
    "backdrop": {"key": "9d0f1e3c5a7b5b1c0e9a3f6d4c7e8a2b", "w": [1280], "ar": 1.778, "color": "#0e1622"}
  },
  "state": {"position_ms": 512000, "played": false, "play_count": 0, "last_played_at": 1795200000000},
  "prefs": {"audio_lang": "eng", "sub_lang": null, "sub_mode": "off"},
  "files": [
    {
      "id": 9901,
      "label": null,
      "part": 0,
      "available": true,
      "container": "matroska",
      "size": 312276541,
      "duration_ms": 1438080,
      "bit_rate": 1736000,
      "attachments_bytes": 759720,
      "video": {"idx": 0, "codec": "av1", "profile": "Main", "width": 1920, "height": 1080,
                "bit_depth": 10, "pix_fmt": "yuv420p10le", "fps": 23.976, "hdr": null, "mp4_codecs": "av01.0.08M.10"},
      "audio": [
        {"idx": 1, "codec": "opus", "channels": 2, "layout": "stereo", "lang": "jpn", "title": null, "default": true, "forced": false, "mp4_codecs": "opus"},
        {"idx": 2, "codec": "opus", "channels": 2, "layout": "stereo", "lang": "eng", "title": null, "default": false, "forced": false, "mp4_codecs": "opus"}
      ],
      "subtitles": [
        {"idx": 3, "codec": "ass", "lang": "eng", "title": "Full", "default": true, "forced": false, "hearing_impaired": false, "external_id": null}
      ],
      "chapters": [
        {"start_ms": 0, "title": "Chapter 01"},
        {"start_ms": 90000, "title": "Chapter 02"}
      ],
      "attachments": [
        {"idx": 4, "filename": "NotoSans-Regular.ttf", "mime": "font/ttf", "size": 759720}
      ],
      "sidecar": {"ready": false, "size": null}
    }
  ],
  "prev": {"id": 4811},
  "next": {"id": 4813, "title": "Low Tide", "season": 1, "episode": 6}
}
```

| Field | Notes |
|---|---|
| `original_title` | The title in its original language, where it differs from `title`; null otherwise, and until metadata has arrived. |
| `prefs` | Films, episodes and series: the track choice to start with. For an episode or a series, the user's choice for the series if there is one, otherwise the user's defaults; for a film, the defaults. `sub_mode` is `off`, `forced`, `auto` or `on`. `auto` means subtitles are shown when the audio is not in `audio_lang`. |
| `files` | Versions and parts. `part` 0 is a whole item. Parts 1..n play in order. The main version, whose `label` is null, comes first, then versions whose label is a number and a `p` or an `i`, such as `1080p`, the highest first, then the others by label. A version's parts follow each other. |
| `numbering` | Series only: `seasons`, or `absolute` when its files carry one running count (`06-library-and-metadata.md`, section 5). |
| `seasons` | Series only: its seasons in order, the specials last, each with its id, number, title and counts. |
| `files[].available` | False while the file is missing or its disk is away. |
| `files[].attachments_bytes` | Fonts and other attachments. A client on a slow link can use it to decide whether to ask for fonts. |
| `files[].video.fps` | Frames per second as a number. |
| `mp4_codecs` | On the video and on each audio stream: the stream's name as a browser asks about it, for the `codecs` part of `video/mp4; codecs="…"`. Null when the codec cannot be carried in MP4. For a page (7.9). |
| `files[].attachments` | Fonts and other files attached to the video. A page fetches the fonts it needs for styled subtitles (7.9). |
| `files[].subtitles[].external_id`, `files[].audio[].external_id` | Set when the track is a file next to the video. It is fetched from `/files/{id}/external/{external_id}`. Such a track has `idx` null. It is not probed: its `codec` comes from its extension, and is null where that does not say, and an audio file's `channels` and `layout` are null (`06-library-and-metadata.md`, section 4.7). The file's own streams come first. |
| `files[].sidecar` | Whether the audio-and-subtitles file exists yet (section 7.4). |
| `prev`, `next` | Episodes only, in season and episode order, specials last. An episode without a number is in its place there: after the number its decimal follows, or after the season's numbered episodes, by title. `next.episode` is null for one. |
| `match.state` | `unmatched`, `matched`, `manual`, `review`, or `ignored` for an item left unmatched on purpose (8.4). |
| `credits` | Films and series only (2.4). |
| `collection` | Films only, when the card has a `collection_id`: `{"id": 5120, "title": "Paper Comets Collection"}`. |

A series detail has `counts`, `seasons` and `credits` in place of `files`, `prev` and `next`:

```json
{
  "id": 377,
  "type": "series",
  "library_id": 2,
  "root_id": 2,
  "title": "Harbor Lantern",
  "year": 2021,
  "genres": ["Drama"],
  "numbering": "seasons",
  "counts": {"episodes": 24, "unplayed": 20, "seasons": 2},
  "seasons": [
    {"id": 378, "season": 1, "title": "Season 1", "counts": {"episodes": 12, "unplayed": 8}},
    {"id": 391, "season": 2, "title": "Season 2", "counts": {"episodes": 12, "unplayed": 12}}
  ],
  "prefs": {"audio_lang": "eng", "sub_lang": null, "sub_mode": "off"},
  "resume": {"id": 4812, "season": 1, "episode": 5, "position_ms": 512000},
  "credits": {
    "cast": [{"person_id": 811, "name": "Ilse Varga", "role": "Edda", "episodes": 12}],
    "crew": [{"person_id": 902, "name": "Tomas Reinholt", "role": "Creator", "department": "Writing"}]
  }
}
```

`resume` is what a Play button on the series should start: the episode in progress, the one played last if several are; else the next unplayed one, as Next up finds it (`03-data-model.md`, section 6): the first episode after the one played most recently that is neither played nor started, specials skipped; else the first episode. Its `position_ms` is 0 unless it is in progress. The counts of the series and of each season carry the user's `unplayed`. An episode without a number is stepped over here, as by Next up, unless it is the one in progress (D51); `resume.episode` is null for one.

A series is one page in every client (D30). Its seasons are in this response, and the episodes of a season are `GET /items?parent={season id}`.

### 2.3 Collection

Films that belong together, as the metadata provider groups them. A collection is an item of type `collection` in one library. It exists only while that library holds two or more of its films (D33). It has a card like any item, and each of its films carries its id as `collection_id`.

Its detail is the card, its films in release order, and what its main button should start:

```json
{
  "id": 5120,
  "type": "collection",
  "library_id": 1,
  "title": "Paper Comets Collection",
  "overview": "Both Paper Comets films.",
  "counts": {"films": 2},
  "images": {
    "poster": {"key": "8a2b9d0f1e3c5a7b5b1c0e9a3f6d4c7e", "w": [342, 500], "ar": 0.667, "color": "#22313f"},
    "backdrop": {"key": "2b9d0f1e3c5a7b5b1c0e9a3f6d4c7e8a", "w": [1280], "ar": 1.778, "color": "#0e1622"}
  },
  "items": [
    {"id": 5031, "type": "movie", "library_id": 1, "root_id": 1, "title": "Paper Comets", "year": 2019, "date": "2019-03-08", "runtime_ms": 6240000, "collection_id": 5120, "available": true, "state": {"position_ms": 1980000, "played": false, "play_count": 0, "last_played_at": 1795200000000}, "seq": 1244},
    {"id": 5032, "type": "movie", "library_id": 1, "root_id": 1, "title": "Paper Comets: The Second Winter", "year": 2023, "date": "2023-11-17", "runtime_ms": 6840000, "collection_id": 5120, "available": true, "state": {"position_ms": 0, "played": false, "play_count": 0, "last_played_at": null}, "seq": 1244}
  ],
  "resume": {"id": 5031, "position_ms": 1980000},
  "seq": 1240
}
```

- `items` are whole cards, shortened here. Films of the collection that the library does not hold are not listed.
- `resume` is the first film in order that is not played.
- `PUT /items/{id}/played` on a collection marks every film in it (7.7).

### 2.4 Credits and people

The detail of a film or a series carries `credits`:

```json
{
  "credits": {
    "cast": [
      {"person_id": 811, "name": "Ilse Varga", "role": "Ida", "episodes": null,
       "image": {"key": "7e8a2b9d0f1e3c5a7b5b1c0e9a3f6d4c", "w": [240, 420], "ar": 0.6666666666666666, "color": null}}
    ],
    "crew": [
      {"person_id": 902, "name": "Tomas Reinholt", "role": "Director", "department": "Directing"}
    ]
  }
}
```

| Field | Notes |
|---|---|
| `cast` | In billing order, at most 20 (P7). `role` is the character. `episodes` is set on a series: how many episodes the person is in. |
| `crew` | Directors and writers, and a series' creators. `role` is the job, or a person's jobs in one department joined with commas, such as `Screenplay, Story`; a series' creators are `Creator`. |
| `image` | On the cast and the crew: the person's photo, shaped like every image (section 6). Absent when the provider has none. A photo is always 2:3, at widths 240 and 420, so both are known before it is fetched (`06-library-and-metadata.md`, section 8). `color` is null until the photo has been fetched once; fetching it stamps nothing, so a detail kept by its `ETag` shows the colour from the item's next change. |

A person: `GET /people/{id}`.

```json
{"id": 811, "name": "Ilse Varga", "department": "Acting", "titles": 5,
 "image": {"key": "7e8a2b9d0f1e3c5a7b5b1c0e9a3f6d4c", "w": [240, 420], "ar": 0.6666666666666666, "color": "#3a2f2a"}}
```

- `department` is what the person is known for. `titles` counts the films and series on this server that credit them.
- The titles themselves are `GET /items?person=811` (4.3).
- People and credits are not part of the local copy. They arrive with a detail, and the client keeps them.

## 3. Server and sign-in

### 3.1 `GET /server`

No authentication. What a client reads first, to check an address it found (3.6) or was given.

```json
{
  "product": "runnel",
  "name": "Upstairs",
  "version": "0.1.0",
  "instance_id": "b6c1f0a2-5d3e-4c8a-9a51-2e7f6d1c0b34",
  "setup_done": true,
  "api": {"version": 1, "features": ["pairing", "changes", "prepare", "sidecar", "list", "collections", "credits", "browser"]},
  "transcode": {"hw": "vaapi", "video": ["hevc", "h264"], "depth10": ["hevc"], "slots": 1}
}
```

- `product` is always `runnel`. It is how a client knows that what answered is a Runnel server.
- `name` is the server's name, which an admin sets (8.1). Until one is set it is the machine's host name. Clients show it where a server is chosen and on the server chip.
- `setup_done` is false while no person exists. The web interface then opens the first run (3.4).
- `transcode` is what the server found it can encode when it last checked (see `05-playback.md`, section 8). A client asks only for what is listed. A server that can encode nothing answers `{"hw": "none", "video": [], "depth10": [], "slots": 0}`.
- `api.features` lists only what this server implements, so a client can tell an older server from a newer one.

### 3.2 Password sign-in

`POST /auth/login`

```json
{"name": "ada", "password": "correct horse", "device": {"id": "0b7e6c1e-52a4-4a0e-8f4e-6f0d3d2a9c11", "name": "Living room TV", "client": "runnel-androidtv/1.0.0"}}
```

Returns `200`:

```json
{"token": "rn_Jp3mK0c5Xq8sTn2vYw7zBf4hLr9dGa6eUi1oCx0", "user": {"id": 1, "name": "ada", "is_admin": true}}
```

- `name`, `password` and `device.id` are needed. `device.id` has at most 200 bytes, `device.name` and `device.client` at most 100.
- A device id is made once by the client and kept. Signing in again from the same device and user replaces that device's earlier token.
- `client` is the app and its version: `runnel-web`, `runnel-androidtv`, `runnel-android`, `runnel-ios` or `runnel-desktop`, then a slash and the version. A client from somewhere else sends its own name.
- A device can hold several people's tokens, one each, and offer a picker between them (`design/00-design-system.md`, DS1). The server has no picker of its own: a token is one person on one device.
- Wrong name or password is `401` with code `wrong_login`, and so is a person who signs in by pairing only. Five failures a minute from one address is `429`, with `Retry-After`. A sixth try within the minute is refused before its password is checked.
- A password has at least ten characters. Setting a shorter one, anywhere, is `400` with code `weak_password`.

### 3.3 Pairing by code

For a TV, where typing a password with a D-pad is slow.

1. The TV calls `POST /auth/pair/start` with its `device` object, as in 3.2. No authentication.

   ```json
   {"code": "K7Q2M9", "secret": "s_4f8a1c2e9b7d60358c1e2a9f7b3d6e04", "expires_in": 600, "poll_every": 2}
   ```

2. The TV shows the code, and a QR code for `http://<host>:8610/pair?code=K7Q2M9`, at the address the TV itself reached the server by.
3. Someone already signed in enters the code on their own device, which looks it up: `GET /auth/pair/K7Q2M9`.

   ```json
   {"code": "K7Q2M9", "device": {"name": "Living room TV", "client": "runnel-androidtv/1.0.0"}, "expires_in": 412}
   ```

   Their screen shows the device's name and client, so they see what is being let in before they approve.
4. They approve it: `POST /auth/pair/approve` with `{"code": "K7Q2M9"}`, which answers `204`. That signs the TV in as the approving user. An admin may add `"user_id"` to sign it in as someone else; anyone else doing so gets `403`, and an id that no user has `400`. A code approved already is `409` with code `conflict`. On the server's own shell, `runnel pair approve K7Q2M9 --user ada` does the same, with the local admin token, which takes the lookup and needs `user_id` on the approval.
5. The TV polls `POST /auth/pair/poll` with `{"code": "K7Q2M9", "secret": "s_4f8a1c2e9b7d60358c1e2a9f7b3d6e04"}`.
   - `202` with `{"waiting": true}` while it waits.
   - `200` with the same body as a password sign-in, once. The pairing is then spent.
   - `410` with code `expired` after ten minutes or once spent.
   - `404` for a code that no device waits with, or a secret that is not the code's.

Codes are six characters from an alphabet without look-alikes (no 0, O, 1, I, L). A code is taken as typed in small or capital letters, with spaces or dashes. The secret is `s_` and 32 hexadecimal characters, and only its SHA-256 is stored. Only the device that holds the secret can collect the token.

- The lookup answers `404` when no device waits with that code, and `410` with code `expired` for a code that ran out or was spent. An expired code is remembered for an hour, so the two can be told apart.
- One token may look up or approve ten codes a minute, then `429` (P7).
- At most 20 pairings wait at once, and one address may start ten a minute (P7). Beyond that `pair/start` answers `429`, with `Retry-After`. Behind a reverse proxy the address is the proxy's (`02-architecture.md`, section 10).
- Admins can list what is waiting (8.6). That says a device is waiting, not whose it is. A code is approved only by someone who sees it on the device's own screen.

### 3.4 The first run: `POST /setup`

A new server has no person, so nobody can sign in. While that is so, `GET /server` says `"setup_done": false`, and this one call makes the first person, who is an admin. It needs the setup code (D35).

The server makes the code when it starts without people and prints it in its log. A later start without people keeps the same code. `runnel setup-code` prints it again, from `GET /admin/setup-code`, which takes the local admin token only: an admin's token is `403`, and once a person exists it is `409` with code `setup_done`. The code is eight characters from the pairing alphabet, shown in two groups of four: `K7Q2-M9XD`.

```json
{"code": "K7Q2M9XD", "name": "ada", "password": "correct horse", "device": {"id": "5e1c9a70-3b2d-4f6a-8c11-9d0e7f2a4b63", "name": "Firefox on Linux", "client": "runnel-web/0.1.0"}}
```

Returns `200` with the same body as a password sign-in. The rest of the first run uses the ordinary calls of section 8 with that token: a library, the metadata key, the encoder check.

- The code is accepted in either case, with or without the hyphen.
- A wrong code is `403` with code `wrong_code`. Five wrong codes a minute from one address is `429` (P7).
- `400` with code `weak_password` for a password under ten characters, and `400` for a name or a device as `POST /admin/users` (8.6) and sign-in (3.2) refuse them.
- `409` with code `setup_done` once a person exists. The code is deleted when the first person is made, here or by `runnel user add`.

### 3.5 A person's own account

| Call | What it does |
|---|---|
| `GET /auth/me` | The user and the device of the token: `{"user": {"id": 1, "name": "ada", "is_admin": true, "has_password": true}, "device": {…}}`, the device shaped as in the list below. |
| `POST /auth/logout` | Revokes the token it is called with. `204`. |
| `PUT /auth/me/password` | `{"current": "…", "new": "…"}`. `204`. A wrong `current` is `401` with code `wrong_login`, counted with failed sign-ins, and limited with them. Tokens stay valid: a device is signed out from the list below. Someone who has no password yet, because they sign in by pairing only, gets `403` and asks an admin to set one. |
| `GET /auth/me/devices` | The person's signed-in devices, the one used last first. |
| `DELETE /auth/me/devices/{id}` | Signs one of them out. `204`, and `404` for a device that is not the person's. |
| `GET /auth/me/prefs`, `PUT /auth/me/prefs` | Track defaults, and the series with a choice of their own (7.8). |

```json
{"devices": [
  {"id": 12, "name": "Living room TV", "client": "runnel-androidtv/1.0.0", "signed_in_at": 1794000000000, "last_used_at": 1795200000000, "current": false},
  {"id": 15, "name": "Firefox on Linux", "client": "runnel-web/0.1.0", "signed_in_at": 1795100000000, "last_used_at": 1795203000000, "current": true}
]}
```

`last_used_at` is written at most every ten minutes (P7). It is a rough time, not a log.

### 3.6 Finding a server on the local network

Proposed (P6). Not researched yet, and not final.

- The server announces itself by DNS-SD over multicast DNS, as a service of type `_runnel._tcp` on its HTTP port.
- The service's text record carries `name` (the server's name), `version` and `id` (the instance id). That is what "Choose a server" lists before any request is made.
- A client that finds one calls `GET /server` at that address and checks `product` and `instance_id` before it offers the server.
- This reaches the local network only. It does not cross subnets or leave a container's private network. So every client also takes a typed address, and away from home that is the only way (D32).
- In a container it needs the host's network. A start-up setting turns the announcement off (`02-architecture.md`, section 6).
- An app has to be allowed onto the home network before it can look, or connect to an address there at all. On Android 17 and later that is the local network permission, or the system's own service picker. On Apple systems the app declares the service and the system asks once. What the screens do when the answer is no is in `design/mobile.md`, section 1.

## 4. Library

### 4.1 `GET /libraries`

```json
{"libraries": [
  {"id": 1, "name": "Films", "kind": "movies", "items": 3120, "unplayed": 212, "roots": [{"id": 1, "available": true}], "seq": 1190},
  {"id": 2, "name": "Anime", "kind": "shows", "items": 241, "unplayed": 9, "roots": [{"id": 2, "available": true}, {"id": 3, "available": false}], "seq": 1259}
]}
```

- `kind` is `movies`, `shows` or `mixed` (`06-library-and-metadata.md`, section 4.10).
- `items` counts films and series, not episodes.
- `unplayed` is the signed-in user's: films not played, or series with an episode not played outside the specials.
- `roots` are the library's folders. `available` is false while a folder is away, for example a disk that is not mounted. Every item built from that folder (`root_id` on its card) is then unavailable, whatever its own `available` says. Paths are not shown here: they are for admins (8.2).

### 4.2 `GET /home`

The first screen in one response. Parameters: `limit` (each row, default 20, at most 200), `library` (optional).

```json
{
  "continue": [{"id": 4812, "type": "episode", "library_id": 2, "title": "The Quiet Morning", "series_id": 377, "series_title": "Harbor Lantern", "season": 1, "episode": 5, "runtime_ms": 1440000, "state": {"position_ms": 512000, "played": false, "play_count": 0, "last_played_at": 1795200000000}, "seq": 1259}],
  "next_up": [],
  "recent": [{"id": 377, "type": "series", "library_id": 2, "title": "Harbor Lantern", "year": 2021, "counts": {"episodes": 24, "unplayed": 20, "seasons": 2}, "new_episodes": 2, "added_at": 1795100000000, "seq": 1258}],
  "list": [{"id": 5031, "type": "movie", "library_id": 1, "title": "Paper Comets", "year": 2019, "listed_at": 1795150000000, "seq": 1244}]
}
```

- `continue`: started and not finished, most recent first.
- `next_up`: for each series, the first unplayed and unstarted episode after the one played most recently, as `03-data-model.md`, section 6, finds it. The series played most recently comes first. Episodes without a number (D51) are neither where it counts from nor what it offers.
- `recent`: what was added in the 30 days before the start of today (P7), newest first. Films are as they are. Episodes are folded into their series, which is placed at the time of its newest episode, and `new_episodes` counts the episodes added in those days. Nothing in that time leaves the row empty.
- `list`: My list, newest first (4.7).
- `library` keeps every row to one library. `404` for a library that does not exist.
- Home's `ETag` also carries the day, so that it moves when Recently added does.

The cards are shortened here. They are whole cards, so `recent` carries the `overview` and `genres` that Home's large cards draw.

### 4.3 `GET /items`

| Parameter | Meaning |
|---|---|
| `library` | Library id. Required unless `parent`, `series`, `ids`, `genre`, `list`, `collection` or `person` is given. |
| `type` | `movie` or `series` at the top of a library; left out, the library's own kind: films in a films library, series in a shows library, both in a mixed one. `season` or `episode` below a parent. With `genre`, `list` or `person` and no `library`: `movie` or `series` to narrow it, both when left out. |
| `parent` | Children of an item: the seasons of a series, the specials last, or the episodes of a season, in order. |
| `series` | With `type=episode`: every episode of a series in one list. |
| `ids` | Comma-separated ids, at most 200. For refreshing a few cards. It goes alone. The cards come in the order asked for, once each, and an id that no item has is left out. |
| `genre` | One genre name. Without `library`: the films and series of every library. |
| `list` | `1`: what the signed-in user has on My list (4.7). Unavailable items are included. It does not go with `library`, `parent` or `series`. |
| `collection` | A collection's id: its films. It needs no `library`, and `type` can only be `movie`. `404` for an id that is not a collection's. |
| `person` | A person's id: the films and series that credit them, newest first unless `sort` says otherwise. Each card gains `credit`, the credit of theirs the title shows first, such as `{"kind": "cast", "role": "Edda", "episodes": 12}`; `episodes` only on a series. It needs no `library`. `404` for an id that is not a person's. |
| `played` | `0` or `1`. For series, `0` means some episode outside the specials is unplayed. |
| `sort` | `title` (default), `added`, `date`, `rating`. Episodes default to season and episode order. With `list` there is also `listed`, its default. With `collection` and `person` the default is `date`. A series was `added` when its newest episode was. `date` is the release date, or the year where that is not known. Items without a date or a rating come last, in either order. Items that sort the same go by title. |
| `order` | `asc` or `desc`. Defaults to `asc` for `title` and `desc` for the others, except with `collection`, where `date` runs `asc`. |
| `limit` | 1 to 1000, default 100. |
| `cursor` | From `next` of the page before. A cursor goes with the sort and order it came from. |
| `offset` | In place of `cursor`, not with it: leave out this many rows. For a grid that jumps to a letter (below). |

```json
{"items": [{"id": 377, "type": "series", "library_id": 2, "title": "Harbor Lantern", "year": 2021, "counts": {"episodes": 24, "unplayed": 20, "seasons": 2}, "seq": 1258}], "next": "eyJ0IjoiaGFyYm9yIGxhbnRlcm4iLCJpIjozNzd9", "total": 241}
```

`total` is sent on the first page, and on every page asked for with `offset`.

`parent` and `series` take `type`, `limit` and `cursor` only.

**Letters.** A client with a local copy jumps to a letter by itself. One without, such as a browser, asks `GET /items/letters`, with the same `library`, `type`, `genre`, `played` and `list` as the list it shows. The answer is for that list sorted by title:

```json
{"total": 3120, "letters": [{"letter": "#", "offset": 0, "count": 12}, {"letter": "A", "offset": 12, "count": 240}, {"letter": "B", "offset": 252, "count": 198}]}
```

- `letter` is `A` to `Z`, `#` for titles that sort before A (digits and signs), and `~` for titles that sort after Z (other scripts). A letter without titles is left out.
- `offset` is where the letter begins. The client asks `GET /items` with that `offset` and draws from there.

### 4.4 `GET /items/{id}`

The detail object of section 2.2, or of 2.3 for a collection. `404` for an id that does not exist.

### 4.5 `GET /search`

Parameters: `q` (required), `types` (default `movie,series`; also `season`, `episode` and `collection`), `limit` (default 30, at most 100). Returns `{"items": [card, …]}` in relevance order, titles that rank the same by title. Matching is by substring, on titles, original titles and alternative titles, whatever the case. A query shorter than three characters matches the start of a title, or of its sort title, which leaves out a leading "The", "A" or "An".

### 4.6 `GET /genres`

Parameter: `library`, optional. Returns `{"genres": [{"name": "Drama", "items": 412, "libraries": [1, 2]}]}`, by name. Without `library` the count is across every library, and `libraries` says which of them hold the genre, in order. Genres come with metadata: until it has arrived, the list is empty.

### 4.7 My list

A person's own list of films and series to watch.

- `PUT /items/{id}/list` puts an item on the signed-in user's list, with the time of the request as its `listed_at`. `DELETE /items/{id}/list` takes it off. Both return `204`, and repeating either changes nothing: an item already on the list keeps its `listed_at`. One taken off and put back is listed anew.
- Only films and series can be listed. Anything else is `400`, and an id that no item has is `404`.
- The list is read with `GET /items?list=1`. Its newest entries are the `list` row of `GET /home`. A listed item's card says `listed_at`.
- The list travels in `/changes` as watch state does (section 5), so every device of a person shows the same list.

### 4.8 `GET /people/{id}`

The person object of section 2.4. `404` for an id that does not exist.

## 5. Keeping a local copy: `GET /changes`

A client keeps the library in its own database, draws its screens from that, and asks the server what changed.

Parameters: `since` (a sequence number, 0 for everything), `limit` (default 500, 1 to 2000). Anything else is `400`.

```json
{
  "since": 1200,
  "upto": 1259,
  "latest": 1259,
  "reset": false,
  "items": [{"id": 4813, "type": "episode", "library_id": 2, "title": "Low Tide", "overview": "The ferry does not come.", "series_id": 377, "series_title": "Harbor Lantern", "season": 1, "episode": 6, "runtime_ms": 1440000, "available": true, "added_at": 1795100000000, "seq": 1257}],
  "state": [{"item_id": 4812, "position_ms": 512000, "played": false, "play_count": 0, "last_played_at": 1795200000000, "seq": 1259}],
  "prefs": [{"series_id": 377, "audio_lang": "eng", "sub_lang": null, "sub_mode": "off", "seq": 1250}],
  "list": [{"item_id": 5031, "listed_at": 1795150000000, "seq": 1244}],
  "deleted": [4790, 4791],
  "libraries": [{"id": 2, "name": "Anime", "kind": "shows", "roots": [{"id": 2, "available": true}, {"id": 3, "available": false}], "seq": 1252}],
  "library_ids": [1, 2]
}
```

How a client uses it:

1. Start with `since=0`. Apply what comes back: upsert `items`, `state`, `prefs`, `list` and `libraries`, remove `deleted`, and drop any library that is not in `library_ids`, with its items.
2. Call again with `since` set to the `upto` it was given, until `upto` equals `latest`.
3. Keep `latest`. Later, ask again from there: when the app opens, when a screen that shows the library opens, and after its own playback.
4. If `reset` is true, the server no longer has the delete markers that far back, or `since` is ahead of its sequence, which means the copy came from another database. Drop the local copy and start from 0. A `reset` page holds nothing else but `latest` and `library_ids`. `since=0` is never reset.

Rules on the server side:

- Everything returned has a `seq` above `since` and at or below `upto`, but for the parents below. A page ends at a sequence boundary, so the rows of one sequence value are never split across two pages. One value is on at most 500 rows (`03-data-model.md`, section 5), so a page holds at most `limit` rows, or 500 when `limit` is lower, besides those parents. Rows that only another user sees do not count. A transaction that changes more than 500 rows, such as the first build of a large library, takes several values, and its changes can arrive over several pages.
- A parent comes before its children, so a page applied on its own never leaves an episode without its season. In `items`, films, series and collections come first, then seasons, then episodes. A season or a series whose own `seq` is above `upto`, because a later change counted a new episode, comes with its episodes and seasons on the page, and again at its own value.
- The page is read in one read transaction and written as it is read.
- `items` are cards without the parts that belong to a user: no `state`, no `listed_at`, and `counts` without `unplayed`. Those arrive in `state` and `list`, for the signed-in user only. A client counts unplayed episodes from its own copy.
- A new episode also returns its season and series, because their counts changed.
- A `list` row with `listed_at` null was taken off the list. A `prefs` row with `sub_mode` null was reset (7.8).
- Collections arrive in `items` like any item. One leaves through `deleted` when its library no longer holds two of its films.
- `libraries` are the entries of `GET /libraries` without `items` and `unplayed`, which a client counts from its own copy. Each carries its folders in `roots`. A disk that goes away changes one library entry here, not the cards under it.
- `library_ids` lists every library that exists now, in every response. That is how a client learns that a library was removed. It drops the library and every item in it: those items are not listed in `deleted`.

What the copy does not hold: files with their streams and chapters, credits, and people. Those come with `GET /items/{id}`, and the client keeps what it fetched. An item's `seq` moves when its files or credits change. A client that kept a detail from an earlier `seq` fetches it again when the page is next opened.

## 6. Images: `GET /img/{key}/{w}`

- `key` comes from an `images` entry, or from a person's `image`. `w` is one of its listed widths. Another width gets the nearest stored one at or above it, or the widest when none is.
- A logo is `image/png`, every other image `image/jpeg`. `404` for a key that is not an image's, and for an image whose files were deleted from the data directory, until the next scan makes a local image's again, or the next fetch a provider's.
- No authentication: keys are 128 random-looking bits and the content is artwork.
- `Cache-Control: public, max-age=31536000, immutable`. A changed image has a new key.
- The server never resizes an image in its own process. Local images, and provider images downloaded at one size, are scaled once to the widths kept by an ffmpeg child (`06-library-and-metadata.md`, section 8).
- A person's photo is fetched from the provider the first time a client asks for it, not when a library is matched (D33). That first request waits for the fetch. A photo the provider cannot give is `404`, and the server does not ask for it again for a day (P7). A photo whose files were deleted from the data directory is fetched again in the same way.
- **What a request without a token can cause.** Only a person's photo makes this route ask the provider, and only for a key that a stored credit gave, from the path the provider gave with it: a key that is no image's and no person's is `404` at once. So a client can cause at most one download for each person that the library credits, and after a failure one a day. One download runs for a key at a time, and every request for that key waits for it; at most two run at once for everyone, and a request that waits 15 s for one of them is answered `503` with code `busy` and `Retry-After: 5`. A download has 15 s. A provider that does not answer costs the requests that wait for it those 15 s and a `404`; one that is down, answers an error of its own, or answers `429` stops every download for a minute, or for what its `Retry-After` gives, at most an hour, and every request for a photo not on disk is answered `503` with `Retry-After` at once meanwhile. Without a metadata key nothing is fetched, and a photo not on disk is `404`. No other request waits on the provider, except the key's check (8.1) and the search for candidates (8.4).

## 7. Playback

### 7.1 Choosing how to play

The client decides, from the `files[]` entry and what it knows about its device:

1. If the device can decode the video codec, profile and bit depth, play the original file (7.2). Whether a format the device decodes only in software counts is the client's setting: on by default, off by default on a TV (D37).
2. If it cannot, ask for a transcode to something it can decode (7.3), and take audio and subtitles from the sidecar when it is ready (7.4).

Audio never needs a transcode for an mpv-based client: mpv decodes every codec the library uses.

### 7.2 `GET /files/{id}/raw`

The file as it is on disk.

- `Range` requests are supported and expected. Responses carry `Accept-Ranges: bytes`, the container's media type and an `ETag` made from the file's fingerprint: the fingerprint in quotes. A file not probed yet is served with the media type of its extension and no `ETag`.
- `404` if the file is unknown. `503` with code `unavailable` while the file is missing or its disk is away.
- The server copies from disk to socket in the kernel. It holds no buffer of its own.

`GET /files/{id}/external/{external_id}` returns a subtitle or audio file that sits next to the video, the same way: by range, without an `ETag`, as `application/x-subrip`, `text/x-ssa`, `text/vtt`, `audio/x-matroska`, `audio/ac3`, `audio/flac`, `audio/ogg` for Opus, `audio/vnd.dts`, `audio/mpeg` or `audio/aac` by its extension, and `application/octet-stream` for `.sub` and `.sup`. `404` for a track that is not that file's. `503` with code `unavailable` while the video is missing, its disk is away, or the track's file is gone.

`GET /files/{id}` returns the one `files[]` object, for a client that wants to see whether a sidecar has finished. A file that is not offered for playback is `404`: an extra, a file not probed yet, or one that ffprobe could not read.

### 7.3 `GET /files/{id}/transcode.mkv`

A Matroska stream produced as it is requested. The video is re-encoded. Everything else is copied.

| Parameter | Values | Default | Meaning |
|---|---|---|---|
| `v` | `hevc`, `h264` | required | Target video codec. Must be listed in `GET /server`. |
| `depth` | `8`, `10` | 10 for a 10-bit source with `v=hevc`, else 8 | Bit depth of the output. |
| `max_h` | pixels, 2 to 8640 | source height | Scale down if the source is taller. An odd height is taken one lower: the encoders need an even one. |
| `start_ms` | milliseconds | 0 | Where to start. |
| `seek` | `key`, `exact` | `key` | `key` starts at the keyframe at or before `start_ms`, which is fast. `exact` decodes forward to `start_ms`, which costs time in proportion to the distance from the keyframe. |
| `audio` | `all`, `none`, a stream index | `all` | Which audio streams to copy in. `none` is for use with the sidecar. An index is one of the file's own audio streams. |
| `subs` | `all`, `none` | `all` | Whether to copy subtitle streams in. |
| `fonts` | `1`, `0` | `1` | Whether to copy attachments in. |
| `ticket` | from `prepare` | none | Claims a warm-up (7.5). |
| `session` | client's playback id, at most 64 characters | none | Ties the stream to its progress reports: the session's first report with timings logs the stream's first byte and encoder (7.6). |

Behaviour:

- `Content-Type: video/x-matroska`, chunked, `Cache-Control: no-store`. No `Range`: the stream cannot seek.
- Timestamps are the original file's. A stream that starts at 12:45 says 12:45. With mpv, set `rebase-start-time=no` so the position it reports is the real one.
- The stream has no duration in it. The client takes the duration from `files[].duration_ms`.
- A seek outside what the player has buffered is a new request with a new `start_ms`. So is an audio switch when the sidecar is not in use. A device's new request that finds every slot taken ends that device's own running transcode and takes its slot, so that it never waits for a stream its player is leaving to notice.
- The stream ends when the file ends or the client disconnects. On disconnect the server kills ffmpeg at once.
- Response headers: `X-Runnel-Encoder` (such as `hevc_vaapi`) and `X-Runnel-Warmed` (`1` when a warm-up was claimed, `0` otherwise).
- The answer starts with ffmpeg's first output. Until then nothing is sent, so an error is still an ordinary answer.

Errors: `400` for a bad parameter. `404` for a file that is unknown or not offered for playback (7.2). `409` with code `unsupported` for a codec or depth the server cannot encode, and for a file without video, with what it can encode in the body: `{"error": {…}, "supported": {"video": ["h264"], "depth10": []}}`. `503` with code `busy` and `Retry-After: 5` when every transcode slot is in use. `503` with code `unavailable` when the file is, or while the server stops. `500` with code `encode_failed` when ffmpeg stopped before its first output on every rung of the ladder left to try (`05-playback.md`, section 4.3), with what ffmpeg said last.

### 7.4 `GET /files/{id}/sidecar.mkv`

The original file minus its video: every audio stream, every subtitle stream, every attachment, copied into a small Matroska file with its own index. The player adds it as a second source and switches audio and subtitles locally while the video comes from a transcode.

- Supports `Range`. It is an ordinary file once built.
- `404` with code `not_ready` when it does not exist yet. That request also queues the build.
- `files[].sidecar.ready` says whether it exists. A `prepare` call for a transcode queues it too.

### 7.5 `POST /files/{id}/prepare`

A hint that this file is about to be played. Sent when an item has had focus for about half a second.

For a file that will be played as it is:

```json
{"mode": "direct", "start_ms": 512000}
```

The server answers `204` at once, and reads the file's header and index in the background, which also wakes a sleeping disk (`05-playback.md`, section 3). Background work waits until the file's playback starts, or for 30 seconds when none does (`02-architecture.md`, section 5). `404` for a file that does not exist, and `503 unavailable` for one that is missing or whose disk is away.

For a file that will be transcoded, the body carries the transcode parameters:

```json
{"mode": "transcode", "v": "hevc", "depth": 10, "start_ms": 512000, "seek": "key", "audio": "all", "subs": "all", "fonts": 1}
```

The parameters are those of 7.3, with the same defaults: `depth`, `max_h`, `start_ms` and `fonts` are numbers, `audio` is `"all"`, `"none"` or a stream index as a number, and `seek` and `subs` are strings. The server starts ffmpeg now, holds its first output in memory, and answers once that output has come. A server that cannot encode what is asked answers `409 unsupported`, as one that encodes nothing does to any transcode. The other errors are those of 7.3. `200`:

```json
{"ticket": "p_7c1e4a90b2d3", "expires_in": 30, "sidecar": {"ready": false}}
```

- The transcode request that carries the ticket gets the held output at once and then the live stream.
- A ticket is good for one request and for 30 seconds. A transcode request with a ticket that is gone, or with different parameters, starts fresh. It never fails because of the ticket.
- One warm-up per token. A new `prepare` replaces the one before it, of either mode.
- One transcode's warm-up is held on the server at a time, and it takes a transcode slot while it waits. A newer transcode `prepare`, from anyone, replaces it. A transcode request that finds every slot taken puts it aside and takes its slot.
- `X-Runnel-Warmed: 1` on the transcode says that the ticket was claimed.
- `prepare` is always optional. A client that never calls it still works.

### 7.6 `POST /progress`

Sent every 10 seconds while playing, and at once on pause, resume, seek and stop.

```json
{
  "session": "f2b6f7c0-0d0a-4d39-9b76-0a2f5f3f6f10",
  "item_id": 4812,
  "file_id": 9901,
  "position_ms": 513200,
  "state": "playing",
  "mode": "transcode",
  "audio_idx": 2,
  "sub_idx": null,
  "timings": {"pressed_to_loaded_ms": 310, "pressed_to_playing_ms": 640},
  "stats": {"decoder": "mediacodec", "hardware": true, "dropped_frames": 0, "warmed": true}
}
```

| Field | Notes |
|---|---|
| `session` | Made by the client for each playback. At most 64 characters. |
| `item_id`, `file_id` | The film or the episode, and the file playing: one of the item's `files`. |
| `position_ms` | From the start of the item. For an item in parts, that is the durations of the parts before the one playing, plus the position in it. |
| `state` | `playing`, `paused` or `stopped`. |
| `mode` | `direct`, `transcode`, `sidecar` (a transcode with audio from the sidecar) or `remux` (a browser playing copied video, 7.9). |
| `audio_idx`, `sub_idx` | Optional. The tracks playing. Not stored. |
| `timings`, `stats` | Optional. Sent once, with the first report after playback starts. The server keeps them in `play_log`: the first report of a session that carries them makes the row. For a transcode the row also gets the server's side, from the session's last stream: its first byte, its encoder and whether it claimed a warm-up (7.3). |

Returns `204`. `400` when a field is missing or malformed, the item is not a film or an episode, or the file is not one of its files. `404` when there is no such item. What the server does with it:

- Saves the position. While `playing` it writes at most once every 10 seconds per session. `paused` and `stopped` always write.
- A position below 30 seconds is not kept, whatever the state: the item counts as not started. A report there clears a position kept before, and changes nothing else.
- A position from 30 seconds on is kept, with the file and the report's time as `last_played_at`. On a played item that makes it unplayed again, so that a second viewing in progress shows under Continue watching. Its play count stays.
- On `stopped`:
  - below 30 seconds: the position is cleared, as if the item was never started.
  - at or beyond 90% of the runtime: the item is marked played, its position is cleared, its play count goes up and `last_played_at` is the report's time.
  - otherwise the position is kept, and the item shows under Continue watching.
- The runtime is the played version's: its file's duration, or the sum of its parts' (`files[].duration_ms`). It is not the provider's `runtime_ms`, since a version can be longer or shorter than that. Where a duration is not known, a stop keeps the position.
- It does not learn track preferences from this. Those are set on purpose (7.8).

### 7.7 Played and unplayed

- `PUT /items/{id}/played` marks an item played: `played` true, the position cleared, `last_played_at` the time of the request, and a play count of at least 1. On a season it marks every episode in it, on a series every episode, the specials too, and on a collection every film. An item already played is left as it is.
- `DELETE /items/{id}/played` clears played and the position, with the same reach. The play count and `last_played_at` stay. An item that is neither played nor started is left as it is.
- `PUT /items/played` and `DELETE /items/played` take `{"ids": [4801, 4802, 4803]}`, at most 1000 ids, and do the same for each of them. That is one request for "Mark earlier episodes as played". An id that no item has, such as one a scan has just removed, is skipped. The forms with one id answer `404` for it.

All four return `204`. With `?return=previous` they return `200` and what they replaced, one entry for each item whose state changed:

```json
{"previous": [
  {"item_id": 4811, "position_ms": 0, "played": false, "play_count": 0, "last_played_at": null},
  {"item_id": 4812, "position_ms": 512000, "played": false, "play_count": 0, "last_played_at": 1795200000000}
]}
```

**Undo.** `PUT /items/state` with `{"states": [ … ]}` writes each entry as given, for the signed-in user, and returns `204`. Entries have the shape above, at most 2000 of them, so a longer `previous` goes back in several requests. An entry is for a film or an episode, and one with `played` true has `position_ms` 0: anything else is `400`. An entry for an item that no longer exists is skipped. A client asks for `previous`, keeps it while its Undo is on screen, and sends it back if Undo is pressed. Nothing else sets a position or a play count directly: playback uses `POST /progress`.

### 7.8 Track preferences

`PUT /series/{id}/prefs`, sent when the user picks an audio or subtitle track during an episode. `204`, and `404` for an id that is not a series:

```json
{"audio_lang": "eng", "sub_lang": null, "sub_mode": "off"}
```

`sub_mode` is required. A language is the `lang` of a track as the detail gives it, or null: at most 16 letters, digits and hyphens.

The next episode of that series then arrives with these in `prefs`, so it starts on the right tracks and no switch is needed. `PUT /auth/me/prefs` with the same body sets the user's defaults, which also apply to films. `204`.

- `DELETE /series/{id}/prefs` forgets a series' choice, so the series follows the user's defaults again. `204`. In `/changes` it is a `prefs` row with `sub_mode` null.
- The defaults are not in `/changes`. A client reads them with `GET /auth/me/prefs` when it starts, and a detail's `prefs` already applies them. Changing them takes a sequence value, so the `ETag`s of details move (section 1).
- `GET /auth/me/prefs` returns the defaults and every series with a choice of its own, by title, for a settings page:

```json
{"defaults": {"audio_lang": "eng", "sub_lang": "eng", "sub_mode": "auto"}, "series": [{"series_id": 377, "title": "Harbor Lantern", "audio_lang": "jpn", "sub_lang": "eng", "sub_mode": "on"}]}
```

### 7.9 Playing in a browser

A browser cannot open the Matroska stream of 7.3, and most cannot open the files themselves. For a page the server makes fragmented MP4, and the page plays it through Media Source Extensions (D39). This was tested in Chrome only: `05-playback.md`, section 13.

**The page decides**, as every client does (7.1), from `files[]` and from what the browser says it can decode (`MediaSource.isTypeSupported`, or `ManagedMediaSource` on an iPhone):

1. Video. If the browser can decode `video.mp4_codecs`, ask for the video as it is: `v=copy`. If not, ask for a codec that `GET /server` lists and the browser decodes, normally `h264`.
2. Audio, one track at a time. If the browser can decode that stream's `mp4_codecs`, ask for it as it is. If not, ask for `a=aac`.

**Two requests**, one stream each, appended to two source buffers:

- `GET /files/{id}/video.mp4`
- `GET /files/{id}/audio.mp4`

| Parameter | On | Values | Default | Meaning |
|---|---|---|---|---|
| `v` | video | `copy`, `h264`, `hevc` | required | Copy the video, or re-encode it. A re-encode takes a transcode slot, and `depth`, `max_h`, `seek` and `ticket` work as in 7.3. |
| `track` | audio | a stream index | required | Which audio stream. |
| `a` | audio | `copy`, `aac` | `copy` | Copy the audio, or re-encode it to AAC. |
| `start_ms` | both | milliseconds | 0 | Where to start. |
| `session` | both | client's playback id | none | Ties the streams to their progress reports. |

Behaviour:

- `Content-Type: video/mp4` or `audio/mp4`, chunked, `Cache-Control: no-store`. No `Range`.
- The response header `X-Runnel-Codecs` names what is in the stream, such as `av01.0.08M.10` or `mp4a.40.2`, for `addSourceBuffer`.
- Each stream is an initialisation segment and then fragments, and each fragment can be appended as it arrives.
- Timestamps are the file's own, as in 7.3. The buffered range therefore starts at the real position.
- Copied video always starts at the keyframe at or before `start_ms`. The page plays from there, or seeks forward inside its buffer to the exact position, which costs decode time.
- **A seek** outside what is buffered: stop both requests, empty both buffers, ask again with the new `start_ms`.
- **An audio switch**: stop the audio request, empty the audio buffer, ask for the other `track` from the current position. The video request is untouched.
- A copy does not take a transcode slot. A token has at most one video and one audio stream for a session: a new request ends the one before it.
- Errors as in 7.3, and `409` with code `unsupported` for a stream whose `mp4_codecs` is null when a copy is asked for.

**Subtitles and fonts** are separate requests, and the page draws them over the video (D40):

- `GET /files/{id}/subs/{idx}.ass` returns a text subtitle stream as an ASS file. A track in another text format is converted. A subtitle file that sits next to the video is `GET /files/{id}/external/{external_id}.ass`.
- `GET /files/{id}/attachments/{idx}` returns an attached font.
- The first request for any of them makes the server read through the file once and keep every text track and attachment. That request waits, for seconds on a large file. Later ones are answered from disk, with `Cache-Control: immutable` under an address that carries the file's fingerprint.
- Picture subtitles (PGS, VobSub) are not available to a page in version 1.

`POST /files/{id}/prepare` takes `"container": "mp4"` with a transcode's parameters, so that the ticket is claimed by `video.mp4`. With `"extract": true` it also starts the subtitle pass, so the tracks are ready when Play is pressed.

## 8. Administration

All of these need a token whose user is an admin. Anyone else gets `403`. The local admin token that the server writes into its data directory works for them too, from the server's own host only (`02-architecture.md`, section 6): it is how the subcommands reach the server. They serve the web interface's administration pages and the command-line tool. The apps use them only to approve a pairing code as someone else: `GET /admin/users` for the list to choose from.

### 8.1 Settings

Two settings can be changed while the server runs, and are kept in the database. Everything the server needs in order to start stays a flag or an environment variable (D17, D34).

`GET /admin/settings`:

```json
{
  "server_name": {"value": "Upstairs", "locked": false},
  "tmdb_key": {"set": true, "hint": "c41f", "ok": true, "checked_at": 1795200000000, "locked": false}
}
```

`PATCH /admin/settings` takes one or both, `{"server_name": "Upstairs", "tmdb_key": "…"}`, and returns the same body as the `GET`.

- `locked` is true when the server's environment sets the value. The environment wins, and changing a locked value here is `409` with code `locked`.
- The key is never sent back. `hint` is its last four characters, and null for a key shorter than 12 characters, of which four would give away too much.
- The key is TMDB's API read access token or its API key, either of them as TMDB's account settings show it: 1 to 1000 characters, printable, without spaces (`06-library-and-metadata.md`, section 6.4).
- A new key is checked against TMDB before it is stored, with one request. `400` with code `bad_key` when TMDB refuses it, or when it is not of that form. `502` with code `upstream` when TMDB cannot be reached, answers with something else, or has asked the server to wait. Neither stores it. `"tmdb_key": null` removes the key. A key is sent nowhere but to TMDB, and never back.
- `ok` and `checked_at` come from the last request the server made with the key: of the key stored here, or of the environment's. `ok` is null, and `checked_at` too, before any request. Without a key, `set` is false and `hint`, `ok` and `checked_at` are null.
- A server name has 1 to 40 characters, no control characters and no space at either end. `"server_name": null` removes it, and the host name is shown again. Any other field is `400`.

### 8.2 Libraries, folders and scans

| Endpoint | Purpose |
|---|---|
| `GET /admin/libraries` | Libraries with their folders, what a scan is doing now, and each folder's last scan. |
| `POST /admin/libraries` | Create: `{"name": "Anime", "kind": "shows", "profile": "anime", "roots": ["/media/anime"]}`. Returns `201` with `{"id": 2, "name": "Anime", "kind": "shows", "profile": "anime", "roots": [{"id": 2, "path": "/media/anime", "available": true}]}`, and the library's first scan starts. |
| `PATCH /admin/libraries/{id}` | Change: `{"name": "Anime", "profile": "anime", "kind": "mixed", "add_roots": ["/media/anime-3"], "remove_roots": [3]}`, any of them. Returns the library as `POST` does. |
| `DELETE /admin/libraries/{id}` | Remove the library, with its items and everyone's watch state for them. Returns `204`. |
| `GET /admin/fs` | The folders inside a folder on the server, for choosing a library's folders. Parameter: `path`. |
| `POST /admin/scan` | `{"library_id": 2}` or `{}` for all; an empty body is all too. Returns `202` with `{}`, and `404` for a library that does not exist. A scan asked for while one runs is gathered into the next. |
| `POST /admin/libraries/{id}/refresh` | Fetch details again for every matched film and series of the library whose metadata is past its age, 30 days, or 7 for a series that airs, and for what failed (`06-library-and-metadata.md`, section 6.3). What is younger is read from the provider's cache, so a second refresh asks the provider for nothing. Returns `202` with `{}`, `404` for a library that does not exist, and `409` with code `no_key` when no metadata key is set. |
| `GET /admin/parse-report` | How every file's name parsed (`06-library-and-metadata.md`, section 4), counted by class for each library, with the files asked for, and the files ffprobe could not read. Parameters: `library`; `class`, a class or `all`; and `noted=1` for the noted files only. A noted file makes an item and has a note: it is one of several files that make an item and whose names do not tell them apart, or it sits in a folder that is not a season folder (sections 4.2 to 4.4 there), or, in a mixed library, it is a film's whose files, read as episodes, give different numbers (section 4.10 there). A file read as an episode's has `season` and `episode`, a film's has neither. Left out, the files listed are those in `guess`, `unnumbered` and `none`, and the noted ones. `unnumbered` is an episode without a number (D51): it makes an item, and its `episode` is null; its reason is its note, which does not count it as noted. The names are parsed again for each request: nothing about it is stored. |

`GET /admin/libraries`:

```json
{"libraries": [
  {"id": 2, "name": "Anime", "kind": "shows", "profile": "anime", "items": 241,
   "scan": {"state": "probing", "done": 1108, "total": 1790},
   "roots": [
     {"id": 2, "path": "/media/anime", "available": true,
      "last_scan": {"at": 1795190000000, "ms": 96, "added": 2, "changed": 0, "removed": 0, "error": null}},
     {"id": 3, "path": "/media/anime-2", "available": false, "last_scan": null}
   ]}
]}
```

- Creating a library: `kind` is `movies`, `shows` or `mixed`, `profile` is `default` (when left out) or `anime`. A library has 1 to 32 folders, each an absolute path to a folder the server can open, stored without a trailing slash. A folder that is, contains or lies inside one that a library already scans is `409` with code `conflict`, and so is a name that is taken. Library names are compared with their case.
- Changing a library: `PATCH` changes everything it is given or nothing. `add_roots` are folders as when creating, and `remove_roots` the ids of the library's own folders. The library keeps 1 to 32 folders. `kind` is `mixed`, for a films or a shows library, or the library's own kind, which changes nothing: no other change of kind is allowed (D49), and a mixed library stays mixed. Any other kind is `400` with code `bad_request`, and so is any other field. Folders added are scanned at once, and so is a library made mixed: its folders are read again as a mixed library's, every folder that reads as it did keeps its items, their ids and everyone's watch state, and a folder that reads as the other kind now is a new item, its old one going to clients in `deleted` with the watch state on it (`06-library-and-metadata.md`, section 5). It cannot be undone. A folder removed takes what was built from it: its items go, with everyone's watch state for them, and reach clients in `deleted` (section 5).
- Removing a library: clients learn of it from `library_ids` (section 5). A removed library's id is never given to another, nor a removed item's (`03-data-model.md`, section 4).
- `items` counts the library's films and series.
- `scan.state` is `idle`, `walking` (going through the folders and building items from them, with no numbers yet), `probing` (reading what was found: `done` of `total` files) or `failed`: nothing is running, and the last scan of one of the folders ended with an error, which that folder's `last_scan.error` holds. A folder that is away is such an error. `done` and `total` are there while probing only.
- `last_scan` is the folder's last scan of any kind: asked for, nightly, or set off by a change on disk. `added`, `changed` and `removed` count files.

`GET /admin/parse-report`:

```json
{"libraries": [{"id": 2, "name": "Anime", "kind": "shows", "files": 1790,
                "counts": {"explicit": 1650, "anchored": 112, "dash": 20, "guess": 7, "unnumbered": 2, "none": 1}, "noted": 2, "unreadable": 1}],
 "files": [{"file_id": 4410, "library_id": 2, "path": "Salt Road (2010)/Season 01/Low Water 07.mkv", "class": "guess",
            "title": "Salt Road", "year": 2010, "season": 1, "episode": 7},
           {"file_id": 4502, "library_id": 2, "path": "Salt Road (2010)/S02/Salt Road - 03.mkv", "class": "anchored",
            "title": "Salt Road", "year": 2010, "season": 1, "episode": 3,
            "note": "in the folder \"S02\", which is not a season folder; one of 2 files that make S01E03, and their names do not tell them apart as versions or parts"}],
 "unreadable": [{"file_id": 5002, "library_id": 2, "path": "Salt Road (2010)/Season 02/Salt Road - S02E03.mkv",
                 "error": "Invalid data found when processing input"}]}
```

`GET /admin/fs?path=/srv/media`:

```json
{"path": "/srv/media", "parent": "/srv", "truncated": false, "folders": [
  {"name": "films", "path": "/srv/media/films", "entries": 1284, "readable": true},
  {"name": "shows", "path": "/srv/media/shows", "entries": 241, "readable": true}
]}
```

- Folders only, by name, at most 500. `truncated` says when there were more. Names that begin with a dot are left out. A link to a folder is listed as a folder: it can be a library's folder, though a scan follows no link to a folder inside one (`06-library-and-metadata.md`, section 2.3).
- `entries` counts what is directly inside a folder. Nothing deeper is counted: that would wake disks and could take long. A folder the server cannot read has `readable` false and `entries` null.
- Without `path` it lists `/`, whose `parent` is null. A `path` must be absolute: `400` otherwise. A path that is not a folder, or a folder the server cannot read, is `404`.

### 8.3 Background work

`GET /admin/tasks`:

```json
{"work": [
  {"kind": "probe", "library_id": 2, "state": "running", "paused_by": null, "done": 1108, "total": 1790, "failed": 0, "error": null, "since": 1795189000000},
  {"kind": "image", "library_id": 1, "state": "paused", "paused_by": "playback", "done": 0, "total": 212, "failed": 0, "error": null, "since": 1795189500000},
  {"kind": "fetch", "library_id": 1, "state": "failed", "paused_by": null, "done": 40, "total": 43, "failed": 3, "error": "TMDB answered 401", "since": 1795180000000}
]}
```

- One entry for each kind of work in each library, not one for each file. The kinds are in `06-library-and-metadata.md`, section 9.
- `state` is `running`, `queued`, `paused`, or `failed` when nothing is left to run and some of it failed after its retries.
- `paused_by` is `playback` while something is starting to play, or `provider` while a provider has asked the server to wait.
- `done` and `total` count the units since that entry's queue was last empty. `error` is the last failure's message: of a task that failed, or else of one that waits to be tried again.
- `POST /admin/tasks/cancel` with `{"kind": "image", "library_id": 1}` drops what is queued and what failed. What is running finishes. The next scan or refresh finds cancelled work again.
- `POST /admin/tasks/retry` with the same body queues what failed again.
- Both answer `{"count": 12}`: how many tasks they dropped or queued. `library_id` is null, or left out, for work that is no library's, such as `import`. Any other `kind` than those is `400`.

### 8.4 Review and matching

| Endpoint | Purpose |
|---|---|
| `GET /admin/review` | Items that are unmatched or matched with low confidence, with why. Parameters: `state` (`unmatched` or `review`), `limit`, `cursor`. The response carries `counts` for both states. |
| `GET /admin/items/{id}/candidates` | Search a provider for matches. Parameters: `q`, `year`, `provider`. |
| `PUT /admin/items/{id}/match` | Fix a match: `{"provider": "tmdb", "value": "123456", "season_offset": 0, "episode_offset": 0, "numbering": "seasons"}`. Stored by folder, so it survives rescans. |
| `DELETE /admin/items/{id}/match` | Forget the match. The item becomes unmatched. |
| `POST /admin/items/{id}/refresh` | Fetch metadata again, asking the provider whatever the age of what it has: a film's or a series' details, and every season the series holds. A season or an episode refreshes its series. Returns `202` with `{}`; the fetch runs in the background (`GET /admin/tasks`, kind `fetch`). An item without a match has nothing to fetch, and the call changes nothing. `404` for an item that does not exist, `400` for another type, `409` with code `no_key` when no metadata key is set. |

- **Leaving an item unmatched.** `PUT /admin/items/{id}/match` with `{"provider": "none"}`. The item keeps the title from its file name, leaves the review list and is not matched again. Its `match.state` is `ignored`. It is stored by folder like a match, and the `DELETE` undoes it.
- **A preview.** `PUT /admin/items/{id}/match?dry_run=1` writes nothing and returns how the files would map:

```json
{"files": 112, "mapped": 112, "unmapped": 0, "samples": [
  {"n": 1, "file": "Salt Road - 001.mkv", "season": 1, "episode": 1, "title": "The Long Swim"},
  {"n": 27, "file": "Salt Road - 027.mkv", "season": 2, "episode": 1, "title": "Low Water"},
  {"n": 58, "file": "Salt Road - 058.mkv", "season": 3, "episode": 1, "title": "North Light"},
  {"n": 85, "file": "Salt Road - 085.mkv", "season": 4, "episode": 1, "title": "The Ferry"},
  {"n": 112, "file": "Salt Road - 112.mkv", "season": 4, "episode": 28, "title": "Lanterns Out"}
]}
```

`samples` are the first file, the first file of each later season, and the last file, at most ten.

### 8.5 Import from Jellyfin

The import itself is in `06-library-and-metadata.md`, section 7.

| Endpoint | Purpose |
|---|---|
| `POST /admin/import/jellyfin/users` | `{"url": "…", "api_key": "…"}`. Returns Jellyfin's users, `{"version": "12.0", "users": [{"id": "…", "name": "ada"}]}`, for the form that maps them to people here. |
| `POST /admin/import/jellyfin` | Starts the import, or its dry run, with the body of `06-library-and-metadata.md`, section 7. Returns `202` with `{}` once Jellyfin and the people named are checked. `409` with code `conflict` while one is queued or running. |
| `GET /admin/import/jellyfin` | The state and the report of the running or the last import or dry run. With `full=1`, every row that did not join, for download. `404` before any has run. |

```json
{
  "state": "done",
  "dry_run": true,
  "at": 1795200000000,
  "finished_at": 1795200041000,
  "version": "12.4.0",
  "joined": 10480,
  "not_joined": 11,
  "series_joined": 241,
  "series_not_joined": 1,
  "matches": 3361,
  "states": {"ada": 5120, "noor": 2210},
  "missing": [{"type": "movie", "path": "/media/films/Old Film (1999)/Old Film (1999).mkv", "reason": "no file at that path here"}],
  "missing_more": 0,
  "error": null
}
```

- `state` is `running`, `done` or `failed`, and `error` says why a run failed. `missing` holds the first 100 rows that did not join, each with its `type` here (`movie`, `series` or `episode`), Jellyfin's path and the reason.
- Without `dry_run` the import is a dry run: it writes only with `"dry_run": false`.
- `joined` and `not_joined` count Jellyfin's films and episodes, which exit criterion 2 of stage 2 is about. `series_joined` and `series_not_joined` count its series. `matches` counts the films and series whose provider ids are written. `states` counts, for each person here, the films and episodes whose play state is written. A dry run counts what the import would write.
- Jellyfin's user names and the names of people here are compared without case. A user Jellyfin does not have, a person who is not here, or a path to rewrite that is not absolute is `400` with code `bad_request`.
- `full` is `1` or `0`.
- `400` with code `bad_key` when Jellyfin refuses the key. `502` with code `upstream` when it cannot be reached, or answers with something the server does not read.
- `url` is Jellyfin's address, `http` or `https`, with its base path if it has one. `api_key` is a key made in Jellyfin's dashboard: 1 to 200 printable characters, without `"` or `,`. Either one missing or malformed is `400` with code `bad_request`.
- `version` is the `Version` that Jellyfin's `GET /System/Info/Public` gives. The users are listed by name.

### 8.6 People and devices

| Endpoint | Purpose |
|---|---|
| `GET /admin/users` | `{"users": [{"id": 1, "name": "ada", "is_admin": true, "has_password": true, "devices": 3, "last_active_at": 1795203000000}]}`. |
| `POST /admin/users` | `{"name": "noor", "password": "…", "is_admin": false}`. Without a password the person signs in by pairing only. Returns `201` with `{"id": 2, "name": "noor", "is_admin": false}`. |
| `PATCH /admin/users/{id}` | Rename, set a new password, make admin or not: `{"name": "Noor", "password": "…", "is_admin": true}`, any of them. Returns the person as `GET /admin/users` lists them. |
| `DELETE /admin/users/{id}` | Removes the person, with their progress, their list and their sign-ins. Returns `204`. |
| `GET /admin/tokens`, `DELETE /admin/tokens/{id}` | Signed-in devices, one entry for each person on a device, shaped as in 3.5 with the `user` added: `{"devices": [{"id": 12, "name": "Living room TV", …, "current": false, "user": {"id": 1, "name": "ada"}}]}`, the one used last first. Deleting one signs it out, and returns `204`. |
| `GET /admin/pairings` | Devices waiting with a pairing code: `{"pairings": [{"code": "K7Q2M9", "device": {"name": "Bedroom TV", "client": "runnel-androidtv/1.0.0"}, "created_at": 1795203000000, "expires_in": 412}]}`. |

- The last admin cannot be removed or made a non-admin: `409` with code `last_admin`.
- `PATCH` changes everything it is given or nothing, and any other field is `400`. A new password leaves the person's devices signed in. `"password": null` takes the password away: the person signs in by pairing only.
- A removed person's rows of the start-time log keep their name, and their `user.id` is null (8.7). Their id is never given to another (`03-data-model.md`, section 4).
- A name has 1 to 40 characters, no control characters and no space at either end. Names are compared without case. A name that is taken is `409` with code `conflict`.
- The first person is always an admin, whatever `is_admin` says, so that a server always has one.
- The pairings list says that something is waiting. Approving is still done against the code on the device's own screen (3.3).

### 8.7 Playback

| Endpoint | Purpose |
|---|---|
| `GET /admin/sessions`, `DELETE /admin/sessions/{session}` | Playbacks in progress: who, on which device, what and how far in, how (`mode`, and for a transcode its target, its encoder and the memory of its ffmpeg child), and how long it took to start. Deleting one stops it. |
| `GET /admin/encoders` | What the server found it can encode, and when it checked. |
| `POST /admin/encoders/check` | Runs the check again (`05-playback.md`, section 8) and returns the result. It takes a few seconds. `503` with code `busy` while a transcode is running. |
| `GET /admin/stats` | Memory, the database, counts and limits. |
| `GET /admin/playlog` | The start-time log, newest first. Parameters: `limit` (1 to 1000, default 100), `cursor`. The server keeps its last 1,000 rows (P7). |

`GET /admin/sessions`:

```json
{"sessions": [
  {"session": "c7a1e0f2", "user": {"id": 1, "name": "ada"}, "device": {"name": "Living room TV", "client": "runnel-androidtv/1.0.0"},
   "item": {"id": 4812, "title": "Harbor Lantern · S1 E5"}, "file_id": 9001, "state": "playing", "position_ms": 512000,
   "mode": "transcode", "reported_at": 1795200010000, "started_in_ms": 640,
   "transcode": {"state": "streaming", "v": "hevc", "depth": 10, "encoder": "hevc_vaapi", "memory": 117440512,
                 "warmed": true, "first_byte_ms": 140, "since": 1795199990000}}
]}
```

- A row is a session that reports progress (7.6), a transcode that runs, or a transcode's warm-up that waits for its ticket. A transcode and the reports of its session are one row. A session whose last report said `stopped` is not listed, nor one that has not reported for 30 minutes.
- `session` is the client's id. A transcode asked for without one has an id of the server's, `s_` and a number, and a warm-up has its ticket.
- `state`, `position_ms`, `reported_at` and `started_in_ms` come from the session's reports, and are null for a transcode or a warm-up that has none. `started_in_ms` is the report's time from Play to the first frame.
- `transcode` is null for a direct play. Its `state` is `warming`, `held` or `streaming` (`05-playback.md`, section 5), `memory` is the ffmpeg child's resident memory, read every 2 seconds, and `first_byte_ms` is the time from the request to its first byte sent, null until then.
- Rows come oldest first, so that a page that refreshes keeps them in place.
- `DELETE /admin/sessions/{session}` stops a transcode or a warm-up: its ffmpeg child is killed and the stream ends. It answers `204`. A player may ask again. A session that only plays a file as it is is `409` with code `conflict`: the player reads the file by itself, and the server cannot stop it. An unknown id is `404`.

`GET /admin/encoders`:

```json
{"checked_at": 1795000001000, "hw": "vaapi", "encoders": [
  {"v": "hevc", "depth": 10, "how": "hardware", "encoder": "hevc_vaapi", "works": true, "error": null},
  {"v": "hevc", "depth": 8, "how": "hardware", "encoder": "hevc_vaapi", "works": true, "error": null},
  {"v": "h264", "depth": 8, "how": "hardware", "encoder": "h264_vaapi", "works": true, "error": null},
  {"v": "h264", "depth": 8, "how": "software", "encoder": "libx264", "works": true, "error": null}
]}
```

A target that has no row, such as AV1, is one this version of the server does not produce.

- The four rows are always there, in this order. `error` is null for a row that works, and otherwise says why it does not: ffmpeg's first line of error, `this ffmpeg has no hevc_vaapi`, `no render device at /dev/dri/renderD128`, `RUNNEL_HWACCEL is none`, or `ffmpeg is not on PATH` (`05-playback.md`, section 8).
- `hw` is `vaapi` when a hardware row works, and `none` otherwise.
- The first check runs before the server listens, so `checked_at` is always set.
- `POST /admin/encoders/check` answers as `GET` does, with the new result.

`GET /admin/stats`:

```json
{
  "version": "0.1.0",
  "started_at": 1794700000000,
  "memory": {"rss": 25165824},
  "database": {"size": 10066329, "seq": 1259},
  "counts": {"libraries": 2, "movies": 3120, "series": 241, "episodes": 7480, "files": 10705, "users": 3},
  "limits": {"slots": 1, "warmups": 1, "child_memory": 1073741824},
  "in_use": {"slots": 1, "warmups": 0}
}
```

`limits` are set when the server starts (P2) and can only be read here. `in_use` counts the transcode slots taken, warm-ups among them, and the transcodes' warm-ups held. `memory.rss` is the server's resident memory, read from `/proc` (0 where there is none). `database.size` is the database file without its write-ahead log.

A row of `GET /admin/playlog`:

```json
{"rows": [
  {"id": 412, "at": 1795200000000, "user": {"id": 1, "name": "ada"}, "device": "Living room TV", "client": "runnel-androidtv/1.0.0",
   "item_id": 4812, "title": "Harbor Lantern · S1 E5", "mode": "transcode", "warmed": true,
   "server_first_byte_ms": 140, "client_loaded_ms": 310, "client_playing_ms": 640,
   "decoder": "mediacodec", "hardware": true, "dropped_frames": 0, "encoder": "hevc_vaapi"}
], "next": null}
```

A row is a snapshot: it keeps the person's and the device's names and the title as they were, also after the device signs out or the item goes. Once the person is removed, `user.id` is null. A field the report did not carry is null, and `server_first_byte_ms` and `encoder` are null for a direct playback. Medians are the page's own arithmetic over the rows it has.

`GET /healthz`, outside `/api/v1`, answers `200` with no authentication, with `ok` as plain text. It reads nothing: it says that the server is up and answering, for a supervisor or a container's health check. A check that passes is logged at debug level only (`02-architecture.md`, section 9).

### 8.8 A newer release

From stage 5 (D47). `GET /admin/update`:

```json
{"check": true, "current": "v0.5.0", "newest": "v0.6.0", "newer": true,
 "url": "https://github.com/gureetk/runnel/releases/tag/v0.6.0", "checked_at": 1795200000000, "error": null}
```

- The server asks GitHub for the list of releases a few minutes after it starts, then once a day. The request sends nothing about the server or its library, not even its version.
- `check` is false when the environment sets `RUNNEL_UPDATE_CHECK=off` (`02-architecture.md`, section 6). Then nothing is asked, and `newest`, `newer`, `url`, `checked_at` and `error` are null.
- On a pre-release, `newest` counts pre-releases too. On a release, it counts releases only.
- `newer` is null for a build that is not a release, such as one built from source, whose version cannot be ordered against the releases.
- `error` is the message of the last check that failed. The last good answer is kept.
- The update itself is `deploy/install.sh` (D46). The server never replaces its own binary.
- It is not in `GET /server` (3.1), which needs no sign-in: how far behind a server is, is for its admins.

## 9. Status and error codes

| HTTP | `code` | When |
|---|---|---|
| 400 | `bad_request` | A parameter is missing or malformed. |
| 400 | `weak_password` | A password under ten characters. |
| 400 | `bad_key` | TMDB or Jellyfin refused the key it was given. |
| 401 | `unauthorized` | No token, or one that is unknown or revoked. |
| 401 | `wrong_login` | Name or password is wrong. |
| 403 | `forbidden` | The user may not do this. |
| 403 | `wrong_code` | The setup code is wrong. |
| 404 | `not_found` | No such item, file, image, person or pairing code. |
| 404 | `not_ready` | The sidecar is not built yet. |
| 409 | `unsupported` | The server cannot encode what was asked. |
| 500 | `encode_failed` | ffmpeg stopped before its first output, on every way the server has to encode the file (7.3). |
| 409 | `setup_done` | The first person already exists. |
| 409 | `locked` | The setting is fixed by the server's environment. |
| 409 | `no_key` | No metadata key is set, so nothing can be fetched. |
| 409 | `last_admin` | The change would leave the server without an admin. |
| 409 | `conflict` | It is already running, as a second import while one runs, or it exists already, as a name that is taken. |
| 410 | `expired` | A pairing code that ran out or was spent. |
| 429 | `too_many` | Too many wrong sign-ins, setup codes or code lookups, or too many pairings started. |
| 502 | `upstream` | TMDB or Jellyfin could not be reached, or answered with something unexpected. |
| 503 | `unavailable` | The file is missing or its disk is away. |
| 503 | `busy` | Every transcode slot is in use, or the encoders' check is asked for while a transcode runs, or a person's photo cannot be fetched now (6). |
| 500 | `internal` | A bug. The message carries an id that is also in the server's log. |

## 10. Worked examples

**Setting up a new server.** A browser opens the server's address. `GET /server` says `"setup_done": false`, so the web interface shows the first run. `POST /setup` with the code from the server's log makes the first person and signs the browser in. Then `GET /admin/fs` and `POST /admin/libraries` add a library, `PATCH /admin/settings` stores the metadata key, `GET /admin/encoders` shows what can be encoded, and `GET /admin/libraries` shows the first scan's progress.

**First start on a TV.** `GET /server` → `POST /auth/pair/start` → show the code → poll → token. Then `GET /changes?since=0` until `upto` equals `latest`, and draw Home from the local copy. `GET /home` gives Home's rows without waiting for the copy.

**Letting a TV in from a phone.** The TV calls `pair/start`, shows the code and polls. On the phone, `GET /auth/pair/K7Q2M9` returns the TV's name and client. The person compares the code with the TV's screen and presses Approve: `POST /auth/pair/approve`. The TV's next poll returns its token.

**Opening a film's page.** With a local copy, the title, artwork, overview, genres, rating and progress draw from the card at once. `GET /items/5031` adds the files, the credits and the collection, and the client keeps them. With the server away the page shows what it has: the card, and the rest if the page was opened before.

**Playing an episode the TV can decode.** Focus rests on the episode: `POST /files/9901/prepare` with `mode: direct`. Play is pressed: the player opens `/files/9901/raw?t=…` with a keyframe seek to the saved position and the tracks from `prefs`. Progress is reported every 10 seconds. At the end: `stopped`, then the client asks `/changes` and moves to `next`.

**Playing an episode the TV cannot decode.** Focus rests: `prepare` with `mode: transcode` returns a ticket. Play is pressed: the player opens `/files/9901/transcode.mkv?v=hevc&depth=10&start_ms=512000&ticket=…&t=…`. If `sidecar.ready` was true, the request says `audio=none&subs=none&fonts=0` and the player adds `/files/9901/sidecar.mkv` as its audio and subtitle source.

**Switching audio during a transcode.** With the sidecar: the player selects another track. Nothing is asked of the server. Without it: the player stops, and opens the transcode URL again with `start_ms` at the current position, `seek=exact`, and the new track chosen when the stream loads. Either way it then sends `PUT /series/377/prefs`.

**Seeking during a transcode.** Inside what the player has buffered: the player seeks by itself. Outside it: a new transcode request with the new `start_ms`.

**Marking a season as played, then Undo.** `PUT /items/378/played?return=previous` returns what each episode's state was. The client shows Undo. If it is pressed: `PUT /items/state` with that list.

**Playing an episode in a browser.** The page checks `av01.0.08M.10` and `opus` with the browser. Both are fine, so it asks for `/files/9901/video.mp4?v=copy&start_ms=512000` and `/files/9901/audio.mp4?track=1&start_ms=512000`, appends both, and plays from where the video's buffer starts. It asks for `/files/9901/subs/3.ass` and the font in `attachments`, and draws the subtitles on a canvas. When the person picks the English track, it replaces the audio request with `track=2` at the current position. In a browser that cannot decode AV1, the first request says `v=h264` instead.
