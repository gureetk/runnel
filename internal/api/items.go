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
	ID       int64           `json:"id"`
	Name     string          `json:"name"`
	Kind     string          `json:"kind"`
	Items    int             `json:"items"`
	Unplayed int             `json:"unplayed"`
	Roots    []rootStateBody `json:"roots"`
	Seq      int64           `json:"seq"`
}

// handleLibraries is GET /libraries (docs/04-api.md, section 4.1).
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
	unplayed, err := s.db.UnplayedByLibrary(r.Context(), callerOf(r.Context()).user.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []libraryBody{}
	for _, l := range libs {
		b := libraryBody{ID: l.ID, Name: l.Name, Kind: l.Kind, Items: counts[l.ID], Unplayed: unplayed[l.ID],
			Roots: []rootStateBody{}, Seq: l.UpdatedSeq}
		for _, root := range l.Roots {
			b.Roots = append(b.Roots, rootStateBody{ID: root.ID, Available: root.Available})
		}
		out = append(out, b)
	}
	writeJSON(w, http.StatusOK, map[string]any{"libraries": out})
}

// cardBody is a card (docs/04-api.md, section 2.1) as far as stage 2 has
// its data. What needs metadata is absent.
type cardBody struct {
	ID          int64                   `json:"id"`
	Type        string                  `json:"type"`
	LibraryID   int64                   `json:"library_id"`
	RootID      *int64                  `json:"root_id,omitempty"`
	Title       string                  `json:"title"`
	Year        *int                    `json:"year,omitempty"`
	SeriesID    *int64                  `json:"series_id,omitempty"`
	SeriesTitle string                  `json:"series_title,omitempty"`
	Season      *int                    `json:"season,omitempty"`
	Episode     *episodeNumber          `json:"episode,omitempty"`
	EpisodeEnd  *int                    `json:"episode_end,omitempty"`
	Absolute    *int                    `json:"absolute,omitempty"`
	Available   bool                    `json:"available"`
	Images      map[string]db.CardImage `json:"images,omitempty"`
	State       *stateBody              `json:"state,omitempty"`
	Counts      *countsBody             `json:"counts,omitempty"`
	ListedAt    *int64                  `json:"listed_at,omitempty"`
	NewEpisodes *int                    `json:"new_episodes,omitempty"`
	AddedAt     int64                   `json:"added_at"`
	Seq         int64                   `json:"seq"`
}

// countsBody is a series' or a season's counts. A series counts its
// episodes and seasons outside the specials. unplayed is the signed-in
// user's.
type countsBody struct {
	Episodes int  `json:"episodes"`
	Unplayed *int `json:"unplayed,omitempty"`
	Seasons  *int `json:"seasons,omitempty"`
}

// unplayedOf is a series' or a season's unplayed count, or nil where no
// user's parts were read.
func (p personal) unplayedOf(id int64) *int {
	if p.unplayed == nil {
		return nil
	}
	n := p.unplayed[id]
	return &n
}

// cardJSON is a card, with the parts of p that belong to the signed-in
// user: a film's or an episode's state, a series' or a season's unplayed
// count, when a film or a series went on My list. A zero p leaves them
// out.
func cardJSON(c db.Card, p personal) cardBody {
	b := cardBody{ID: c.ID, Type: c.Type, LibraryID: c.LibraryID, Title: c.Title, Available: c.Available, Images: c.Images,
		AddedAt: c.AddedAt, Seq: c.Seq}
	if c.RootID != 0 {
		b.RootID = &c.RootID
	}
	if c.Year != 0 {
		b.Year = &c.Year
	}
	if p.states != nil && (c.Type == "movie" || c.Type == "episode") {
		b.State = p.state(c.ID)
	}
	if at, ok := p.listed[c.ID]; ok && (c.Type == "movie" || c.Type == "series") {
		b.ListedAt = &at
	}
	switch c.Type {
	case "episode":
		b.SeriesID, b.SeriesTitle = &c.SeriesID, c.SeriesTitle
		b.Season, b.Episode = &c.Season, &episodeNumber{N: c.Episode, None: c.Unnumbered}
		if c.EpisodeEnd != 0 {
			b.EpisodeEnd = &c.EpisodeEnd
		}
		if c.Absolute != 0 {
			b.Absolute = &c.Absolute
		}
	case "season":
		b.SeriesID, b.SeriesTitle, b.Season = &c.SeriesID, c.SeriesTitle, &c.Season
		b.Counts = &countsBody{Episodes: c.Episodes, Unplayed: p.unplayedOf(c.ID)}
	case "series":
		b.Counts = &countsBody{Episodes: c.Episodes, Unplayed: p.unplayedOf(c.ID), Seasons: &c.Seasons}
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
	Z bool    `json:"z"`
	S int     `json:"s"`
	P float64 `json:"p"`
	T string  `json:"t"`
	I int64   `json:"i"`
}

type seasonCursor struct {
	Z bool  `json:"z"`
	S int   `json:"s"`
	I int64 `json:"i"`
}

type numberCursor struct {
	P float64 `json:"p"`
	A int     `json:"a"`
	T string  `json:"t"`
	I int64   `json:"i"`
}

// itemsParams are the parameters of GET /items (docs/04-api.md,
// section 4.3).
var itemsParams = map[string]bool{"library": true, "type": true, "series": true, "parent": true, "ids": true, "genre": true,
	"list": true, "collection": true, "person": true, "played": true, "sort": true, "order": true, "limit": true,
	"cursor": true, "offset": true}

// handleItems is GET /items: the episodes of a series in season and
// episode order, specials last; the children of an item, the seasons of a
// series or the episodes of a season, in their order; some items by id;
// or a list of films and series, a library's, a genre's or the signed-in
// user's, in one of its sorts.
func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for k := range q {
		if !itemsParams[k] {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "GET /items does not take the parameter "+strconv.Quote(k))
			return
		}
	}
	for _, k := range []string{"collection", "person"} {
		if q.Has(k) {
			s.fail(w, r, http.StatusBadRequest, "bad_request", k+" arrives with metadata, in a later version of the server")
			return
		}
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
	if q.Has("parent") || q.Has("series") {
		for _, k := range []string{"library", "ids", "genre", "list", "played", "sort", "order", "offset"} {
			if q.Has(k) {
				s.fail(w, r, http.StatusBadRequest, "bad_request", k+" does not go with parent or series")
				return
			}
		}
	}
	if !s.etag(w, r, "") {
		return
	}
	switch {
	case q.Has("ids"):
		s.itemsByID(w, r)
		return
	case !q.Has("parent") && !q.Has("series"):
		s.list(w, r, limit)
		return
	}
	first := q.Get("cursor") == ""

	var cards []db.Card
	var total int
	var next func(db.Card) string
	var err error
	if v := q.Get("parent"); v != "" {
		id, perr := strconv.ParseInt(v, 10, 64)
		parent, gerr := s.db.Item(r.Context(), id)
		switch {
		case perr != nil || errors.Is(gerr, db.ErrNotFound) || (gerr == nil && parent.Type != "series" && parent.Type != "season"):
			s.fail(w, r, http.StatusNotFound, "not_found", "no such series or season")
			return
		case gerr != nil:
			s.internal(w, r, gerr)
			return
		}
		child := map[string]string{"series": "season", "season": "episode"}[parent.Type]
		if t := q.Get("type"); t != "" && t != child {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "the children of a "+parent.Type+" are of type "+strconv.Quote(child))
			return
		}
		if first {
			total, err = s.db.CountChildren(r.Context(), id, child)
		}
		if child == "season" {
			var c seasonCursor
			if !first && !decodeCursor(q.Get("cursor"), &c) {
				s.fail(w, r, http.StatusBadRequest, "bad_request", "the cursor is not one this server gave")
				return
			}
			if first {
				c = seasonCursor{S: math.MinInt32}
			}
			if err == nil {
				cards, err = s.db.SeasonsOf(r.Context(), id, db.SeasonKey{Specials: c.Z, Season: c.S, ID: c.I}, limit+1)
			}
			next = func(c db.Card) string { return encodeCursor(seasonCursor{Z: c.Season == 0, S: c.Season, I: c.ID}) }
		} else {
			var c numberCursor
			if !first && !decodeCursor(q.Get("cursor"), &c) {
				s.fail(w, r, http.StatusBadRequest, "bad_request", "the cursor is not one this server gave")
				return
			}
			if first {
				c = numberCursor{P: math.Inf(-1)}
			}
			if err == nil {
				cards, err = s.db.EpisodesOfSeason(r.Context(), id, db.NumberKey{Place: c.P, Absolute: c.A, Sort: c.T, ID: c.I}, limit+1)
			}
			next = func(c db.Card) string {
				return encodeCursor(numberCursor{P: c.Place, A: c.Absolute, T: c.SortTitle, I: c.ID})
			}
		}
	} else if v := q.Get("series"); v != "" {
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
			c = episodeCursor{S: math.MinInt32, P: math.Inf(-1)}
			total, err = s.db.CountEpisodes(r.Context(), id)
		}
		if err == nil {
			cards, err = s.db.EpisodesOfSeries(r.Context(), id, db.EpisodeKey{Specials: c.Z, Season: c.S, Place: c.P, Sort: c.T, ID: c.I}, limit+1)
		}
		next = func(c db.Card) string {
			return encodeCursor(episodeCursor{Z: c.Season == 0, S: c.Season, P: c.Place, T: c.SortTitle, I: c.ID})
		}
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.page(w, r, cards, limit, next, first, total)
}

// page answers a page of cards: at most limit of them, the next cursor
// when there are more, and the total on a first page.
func (s *Server) page(w http.ResponseWriter, r *http.Request, cards []db.Card, limit int, next func(db.Card) string, withTotal bool, total int) {
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
	mine, err := s.personalOf(r.Context(), cards)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for _, c := range cards {
		body.Items = append(body.Items, cardJSON(c, mine))
	}
	if withTotal {
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

// audioBody and subtitleBody are a file's tracks: its own streams, by
// idx, and the files beside it, by external_id, with idx null. Such a file
// is not probed: its codec is what its extension says, or null, and its
// channels are not known.
type audioBody struct {
	Idx        *int    `json:"idx"`
	Codec      *string `json:"codec"`
	Channels   *int    `json:"channels"`
	Layout     *string `json:"layout"`
	Lang       *string `json:"lang"`
	Title      *string `json:"title"`
	Default    bool    `json:"default"`
	Forced     bool    `json:"forced"`
	ExternalID *int64  `json:"external_id"`
}

type subtitleBody struct {
	Idx             *int    `json:"idx"`
	Codec           *string `json:"codec"`
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
			b.Audio = append(b.Audio, audioBody{Idx: &s.Index, Codec: &s.Codec, Channels: &s.Channels, Layout: strOrNil(s.ChannelLayout),
				Lang: strOrNil(s.Lang), Title: strOrNil(s.Title), Default: s.Default, Forced: s.Forced})
		case "subtitle":
			b.Subtitles = append(b.Subtitles, subtitleBody{Idx: &s.Index, Codec: &s.Codec, Lang: strOrNil(s.Lang), Title: strOrNil(s.Title),
				Default: s.Default, Forced: s.Forced, HearingImpaired: s.HearingImpaired})
		case "attachment":
			b.Attachments = append(b.Attachments, attachmentBody{Idx: s.Index, Filename: strOrNil(s.Filename), MIME: strOrNil(s.MIME), Size: s.Size})
		}
	}
	for _, c := range f.Chapters {
		b.Chapters = append(b.Chapters, chapterBody{StartMS: c.StartMS, Title: strOrNil(c.Title)})
	}
	for _, t := range f.Tracks {
		id := t.ID
		if t.Kind == "audio" {
			b.Audio = append(b.Audio, audioBody{Codec: strOrNil(t.Codec), Lang: strOrNil(t.Lang), Title: strOrNil(t.Title),
				Default: t.Default, Forced: t.Forced, ExternalID: &id})
		} else {
			b.Subtitles = append(b.Subtitles, subtitleBody{Codec: strOrNil(t.Codec), Lang: strOrNil(t.Lang), Title: strOrNil(t.Title),
				Default: t.Default, Forced: t.Forced, HearingImpaired: t.HearingImpaired, ExternalID: &id})
		}
	}
	return b
}

// handleItem is GET /items/{id}: the detail of docs/04-api.md, sections
// 2.2 and 4.4. A film or an episode has its files, versions and parts with
// their streams and chapters, which is what a player needs; an episode its
// neighbours; a series its numbering and its seasons.
func (s *Server) handleItem(w http.ResponseWriter, r *http.Request) {
	if !s.etag(w, r, "") {
		return
	}
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
	m, err := s.db.MatchOf(r.Context(), id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	user := callerOf(r.Context()).user.ID
	var seasons []db.Card
	if c.Type == "series" {
		if seasons, err = s.db.SeasonsOf(r.Context(), id, db.SeasonKey{Season: math.MinInt32}, 1000); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	mine, err := s.personalOf(r.Context(), append([]db.Card{c}, seasons...))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	body := detailBody{cardBody: cardJSON(c, mine), OriginalTitle: nil, ExternalIDs: m.ExternalIDs,
		Match: matchBody{State: m.State}}
	if m.Confidence != 0 {
		body.Match.Confidence = &m.Confidence
	}
	if c.Type == "movie" || c.Type == "episode" || c.Type == "series" {
		series := c.SeriesID
		if c.Type == "series" {
			series = c.ID
		}
		p, err := s.db.PrefsFor(r.Context(), user, series)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		pb := prefsJSON(p)
		body.Prefs = &pb
	}
	switch c.Type {
	case "movie", "episode":
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
		if c.Type == "episode" {
			prev, next, err := s.db.Neighbours(r.Context(), c)
			if err != nil {
				s.internal(w, r, err)
				return
			}
			if prev.ID != 0 {
				body.Prev = &neighbourBody{ID: prev.ID}
			}
			if next.ID != 0 {
				body.Next = &neighbourBody{ID: next.ID, Title: next.Title, Season: &next.Season,
					Episode: &episodeNumber{N: next.Episode, None: next.Unnumbered}}
			}
		}
	case "series":
		body.Numbering = m.Numbering
		list := []seasonBody{}
		for _, se := range seasons {
			list = append(list, seasonBody{ID: se.ID, Season: se.Season, Title: se.Title,
				Counts: countsBody{Episodes: se.Episodes, Unplayed: mine.unplayedOf(se.ID)}})
		}
		body.Seasons = &list
		at, ok, err := s.db.ResumeOf(r.Context(), user, id)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if ok {
			body.Resume = &resumeBody{ID: at.ID, Season: at.Season, Episode: episodeNumber{N: at.Episode, None: at.Unnumbered},
				PositionMS: at.PositionMS}
		}
	}
	writeJSON(w, http.StatusOK, body)
}

// detailBody is an item's detail (docs/04-api.md, section 2.2), as far as
// stage 2 has its data: the card; for a film, an episode or a series the
// track choice to start with; for a film or an episode its files; for an
// episode its neighbours; for a series its numbering, its seasons and
// where its Play button starts.
type detailBody struct {
	cardBody
	OriginalTitle *string           `json:"original_title"`
	ExternalIDs   map[string]string `json:"external_ids"`
	Match         matchBody         `json:"match"`
	Prefs         *prefsBody        `json:"prefs,omitempty"`
	Numbering     string            `json:"numbering,omitempty"`
	Seasons       *[]seasonBody     `json:"seasons,omitempty"`
	Resume        *resumeBody       `json:"resume,omitempty"`
	Files         *[]fileBody       `json:"files,omitempty"`
	Prev          *neighbourBody    `json:"prev,omitempty"`
	Next          *neighbourBody    `json:"next,omitempty"`
}

type matchBody struct {
	State      string   `json:"state"`
	Confidence *float64 `json:"confidence"`
}

type seasonBody struct {
	ID     int64      `json:"id"`
	Season int        `json:"season"`
	Title  string     `json:"title"`
	Counts countsBody `json:"counts"`
}

type resumeBody struct {
	ID         int64         `json:"id"`
	Season     int           `json:"season"`
	Episode    episodeNumber `json:"episode"`
	PositionMS int64         `json:"position_ms"`
}

type neighbourBody struct {
	ID      int64          `json:"id"`
	Title   string         `json:"title,omitempty"`
	Season  *int           `json:"season,omitempty"`
	Episode *episodeNumber `json:"episode,omitempty"`
}

// episodeNumber is an episode's number as JSON: the number, or null for an
// episode without a number (D51).
type episodeNumber struct {
	N    int
	None bool
}

func (e episodeNumber) MarshalJSON() ([]byte, error) {
	if e.None {
		return []byte("null"), nil
	}
	return strconv.AppendInt(nil, int64(e.N), 10), nil
}

func (e *episodeNumber) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*e = episodeNumber{None: true}
		return nil
	}
	n, err := strconv.Atoi(string(b))
	*e = episodeNumber{N: n}
	return err
}
