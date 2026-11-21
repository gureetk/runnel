// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/gureetk/runnel/internal/db"
)

type rootStateBody struct {
	ID        int64 `json:"id"`
	Available bool  `json:"available"`
}

type libraryBody struct {
	ID    int64           `json:"id"`
	Name  string          `json:"name"`
	Kind  string          `json:"kind"`
	Items int             `json:"items"`
	Roots []rootStateBody `json:"roots"`
	Seq   int64           `json:"seq"`
}

// handleLibraries is GET /libraries (docs/04-api.md, section 4.1).
// unplayed arrives with watch state.
func (s *Server) handleLibraries(w http.ResponseWriter, r *http.Request) {
	libs, err := s.db.Libraries(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	counts, err := s.db.ItemCounts(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []libraryBody{}
	for _, l := range libs {
		b := libraryBody{ID: l.ID, Name: l.Name, Kind: l.Kind, Items: counts[l.ID], Roots: []rootStateBody{}, Seq: l.UpdatedSeq}
		for _, root := range l.Roots {
			b.Roots = append(b.Roots, rootStateBody{ID: root.ID, Available: root.Available})
		}
		out = append(out, b)
	}
	writeJSON(w, http.StatusOK, map[string]any{"libraries": out})
}

// cardBody is a card as stage 1 fills it (docs/04-api.md, section 2.1).
// What needs watch state, metadata or artwork is absent.
type cardBody struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"`
	LibraryID   int64  `json:"library_id"`
	RootID      *int64 `json:"root_id,omitempty"`
	Title       string `json:"title"`
	Year        *int   `json:"year,omitempty"`
	SeriesID    *int64 `json:"series_id,omitempty"`
	SeriesTitle string `json:"series_title,omitempty"`
	Season      *int   `json:"season,omitempty"`
	Episode     *int   `json:"episode,omitempty"`
	AddedAt     int64  `json:"added_at"`
	Seq         int64  `json:"seq"`
}

func cardJSON(c db.Card) cardBody {
	b := cardBody{ID: c.ID, Type: c.Type, LibraryID: c.LibraryID, Title: c.Title, AddedAt: c.AddedAt, Seq: c.Seq}
	if c.RootID != 0 {
		b.RootID = &c.RootID
	}
	if c.Year != 0 {
		b.Year = &c.Year
	}
	if c.Type == "episode" {
		b.SeriesID, b.SeriesTitle = &c.SeriesID, c.SeriesTitle
		b.Season, b.Episode = &c.Season, &c.Episode
	}
	return b
}

func encodeCursor(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string, v any) bool {
	b, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && json.Unmarshal(b, v) == nil
}

type titleCursor struct {
	T string `json:"t"`
	I int64  `json:"i"`
}

type episodeCursor struct {
	Z bool  `json:"z"`
	S int   `json:"s"`
	E int   `json:"e"`
	I int64 `json:"i"`
}

// itemsParams are the parameters of GET /items that this server takes.
// The others of docs/04-api.md, section 4.3, arrive with stage 2.
var itemsParams = map[string]bool{"library": true, "type": true, "series": true, "sort": true, "limit": true, "cursor": true}

// handleItems is GET /items: the films or series of a library by title, or
// the episodes of a series in season and episode order, specials last.
func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for k := range q {
		if !itemsParams[k] {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "this server does not take the parameter "+strconv.Quote(k)+" yet")
			return
		}
	}
	if v := q.Get("sort"); v != "" && v != "title" {
		s.fail(w, r, http.StatusBadRequest, "bad_request", `this server sorts by "title" only, so far`)
		return
	}
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "limit is 1 to 1000")
			return
		}
		limit = n
	}
	first := q.Get("cursor") == ""

	var cards []db.Card
	var total int
	var next func(db.Card) string
	var err error
	if v := q.Get("series"); v != "" {
		id, perr := strconv.ParseInt(v, 10, 64)
		series, gerr := s.db.Item(r.Context(), id)
		switch {
		case perr != nil || errors.Is(gerr, db.ErrNotFound) || (gerr == nil && series.Type != "series"):
			s.fail(w, r, http.StatusNotFound, "not_found", "no such series")
			return
		case gerr != nil:
			s.internal(w, r, gerr)
			return
		}
		if t := q.Get("type"); t != "" && t != "episode" {
			s.fail(w, r, http.StatusBadRequest, "bad_request", `with series, type is "episode"`)
			return
		}
		var c episodeCursor
		if !first && !decodeCursor(q.Get("cursor"), &c) {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "the cursor is not one this server gave")
			return
		}
		if first {
			c = episodeCursor{S: math.MinInt32, E: math.MinInt32}
			total, err = s.db.CountEpisodes(r.Context(), id)
		}
		if err == nil {
			cards, err = s.db.EpisodesOfSeries(r.Context(), id, db.EpisodeKey{Specials: c.Z, Season: c.S, Episode: c.E, ID: c.I}, limit+1)
		}
		next = func(c db.Card) string {
			return encodeCursor(episodeCursor{Z: c.Season == 0, S: c.Season, E: c.Episode, I: c.ID})
		}
	} else {
		v := q.Get("library")
		id, perr := strconv.ParseInt(v, 10, 64)
		lib, gerr := s.db.Library(r.Context(), id)
		switch {
		case v == "":
			s.fail(w, r, http.StatusBadRequest, "bad_request", "library or series is needed")
			return
		case perr != nil || errors.Is(gerr, db.ErrNotFound):
			s.fail(w, r, http.StatusNotFound, "not_found", "no such library")
			return
		case gerr != nil:
			s.internal(w, r, gerr)
			return
		}
		typ := q.Get("type")
		if typ == "" {
			typ = map[string]string{"movies": "movie", "shows": "series"}[lib.Kind]
		}
		if typ != "movie" && typ != "series" {
			s.fail(w, r, http.StatusBadRequest, "bad_request", `at the top of a library, type is "movie" or "series"`)
			return
		}
		var c titleCursor
		if !first && !decodeCursor(q.Get("cursor"), &c) {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "the cursor is not one this server gave")
			return
		}
		if first {
			total, err = s.db.CountByType(r.Context(), id, typ)
		}
		if err == nil {
			cards, err = s.db.ItemsByTitle(r.Context(), id, typ, c.T, c.I, limit+1)
		}
		next = func(c db.Card) string { return encodeCursor(titleCursor{T: c.SortTitle, I: c.ID}) }
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}

	body := struct {
		Items []cardBody `json:"items"`
		Next  *string    `json:"next"`
		Total *int       `json:"total,omitempty"`
	}{Items: []cardBody{}}
	if len(cards) > limit {
		n := next(cards[limit-1])
		body.Next = &n
		cards = cards[:limit]
	}
	for _, c := range cards {
		body.Items = append(body.Items, cardJSON(c))
	}
	if first {
		body.Total = &total
	}
	writeJSON(w, http.StatusOK, body)
}

type videoBody struct {
	Idx      int     `json:"idx"`
	Codec    string  `json:"codec"`
	Profile  *string `json:"profile"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	BitDepth int     `json:"bit_depth"`
	PixFmt   string  `json:"pix_fmt"`
	FPS      float64 `json:"fps"`
	HDR      *string `json:"hdr"`
}

type audioBody struct {
	Idx      int     `json:"idx"`
	Codec    string  `json:"codec"`
	Channels int     `json:"channels"`
	Layout   *string `json:"layout"`
	Lang     *string `json:"lang"`
	Title    *string `json:"title"`
	Default  bool    `json:"default"`
	Forced   bool    `json:"forced"`
}

type subtitleBody struct {
	Idx             int     `json:"idx"`
	Codec           string  `json:"codec"`
	Lang            *string `json:"lang"`
	Title           *string `json:"title"`
	Default         bool    `json:"default"`
	Forced          bool    `json:"forced"`
	HearingImpaired bool    `json:"hearing_impaired"`
	ExternalID      *int64  `json:"external_id"`
}

type chapterBody struct {
	StartMS int64   `json:"start_ms"`
	Title   *string `json:"title"`
}

type attachmentBody struct {
	Idx      int     `json:"idx"`
	Filename *string `json:"filename"`
	MIME     *string `json:"mime"`
	Size     int64   `json:"size"`
}

type fileBody struct {
	ID               int64            `json:"id"`
	Label            *string          `json:"label"`
	Part             int              `json:"part"`
	Available        bool             `json:"available"`
	Container        string           `json:"container"`
	Size             int64            `json:"size"`
	DurationMS       *int64           `json:"duration_ms"`
	BitRate          *int64           `json:"bit_rate"`
	AttachmentsBytes int64            `json:"attachments_bytes"`
	Video            *videoBody       `json:"video"`
	Audio            []audioBody      `json:"audio"`
	Subtitles        []subtitleBody   `json:"subtitles"`
	Chapters         []chapterBody    `json:"chapters"`
	Attachments      []attachmentBody `json:"attachments"`
}

func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func int64OrNil(n int64) *int64 {
	if n == 0 {
		return nil
	}
	return &n
}

func fileJSON(f db.FileDetail) fileBody {
	b := fileBody{ID: f.ID, Label: strOrNil(f.Label), Part: f.Part, Available: f.Available, Container: f.Container,
		Size: f.Size, DurationMS: int64OrNil(f.DurationMS), BitRate: int64OrNil(f.BitRate), AttachmentsBytes: f.AttachmentsBytes,
		Audio: []audioBody{}, Subtitles: []subtitleBody{}, Chapters: []chapterBody{}, Attachments: []attachmentBody{}}
	for _, s := range f.Streams {
		switch s.Kind {
		case "video":
			if b.Video != nil {
				continue // the first video stream is the one shown
			}
			v := videoBody{Idx: s.Index, Codec: s.Codec, Profile: strOrNil(s.Profile), Width: s.Width, Height: s.Height,
				BitDepth: s.BitDepth, PixFmt: s.PixFmt, HDR: strOrNil(s.HDR)}
			if s.FPSDen > 0 {
				v.FPS = math.Round(float64(s.FPSNum)/float64(s.FPSDen)*1000) / 1000
			}
			b.Video = &v
		case "audio":
			b.Audio = append(b.Audio, audioBody{Idx: s.Index, Codec: s.Codec, Channels: s.Channels, Layout: strOrNil(s.ChannelLayout),
				Lang: strOrNil(s.Lang), Title: strOrNil(s.Title), Default: s.Default, Forced: s.Forced})
		case "subtitle":
			b.Subtitles = append(b.Subtitles, subtitleBody{Idx: s.Index, Codec: s.Codec, Lang: strOrNil(s.Lang), Title: strOrNil(s.Title),
				Default: s.Default, Forced: s.Forced, HearingImpaired: s.HearingImpaired})
		case "attachment":
			b.Attachments = append(b.Attachments, attachmentBody{Idx: s.Index, Filename: strOrNil(s.Filename), MIME: strOrNil(s.MIME), Size: s.Size})
		}
	}
	for _, c := range f.Chapters {
		b.Chapters = append(b.Chapters, chapterBody{StartMS: c.StartMS, Title: strOrNil(c.Title)})
	}
	return b
}

// handleItem is GET /items/{id}: the card and, for a film or an episode,
// its files with their streams and chapters, which is what a player needs
// (docs/04-api.md, sections 2.2 and 4.4).
func (s *Server) handleItem(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	c, gerr := s.db.Item(r.Context(), id)
	switch {
	case err != nil || errors.Is(gerr, db.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not_found", "no such item")
		return
	case gerr != nil:
		s.internal(w, r, gerr)
		return
	}
	body := struct {
		cardBody
		Files *[]fileBody `json:"files,omitempty"` // films and episodes; a series' episodes are GET /items?series=
	}{cardBody: cardJSON(c)}
	if c.Type == "movie" || c.Type == "episode" {
		files, err := s.db.ItemFiles(r.Context(), id)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		list := []fileBody{}
		for _, f := range files {
			list = append(list, fileJSON(f))
		}
		body.Files = &list
	}
	writeJSON(w, http.StatusOK, body)
}
