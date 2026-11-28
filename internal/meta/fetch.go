// SPDX-License-Identifier: GPL-3.0-or-later

package meta

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/meta/tmdb"
	"github.com/gureetk/runnel/internal/parse"
	"github.com/gureetk/runnel/internal/tasks"
)

// How old an answer may be before a fetch asks the provider again: a
// series that still airs is fetched weekly, everything else every 30 days
// (docs/06-library-and-metadata.md, section 6.3). An item's refresh always
// asks.
const (
	Every  = 30 * 24 * time.Hour
	Airing = 7 * 24 * time.Hour
)

// badKeyPause is how long fetching waits after the provider refused the
// key, unless a new key is set first.
const badKeyPause = time.Hour

// Provider is the metadata provider's client, as fetching uses it.
type Provider interface {
	Get(ctx context.Context, path string, q url.Values, maxAge time.Duration) (db.Cached, error)
}

// Fetcher fetches the metadata of matched films and series, with their
// seasons, and writes it onto them: the background work of kind fetch
// (section 9 there).
type Fetcher struct {
	db       *db.DB
	provider Provider
	key      tmdb.KeySource
	log      *slog.Logger
	now      func() time.Time
	// Queued, when set, is called after fetches were queued; Resume ends
	// a pause of the fetches, after a new key was set.
	Queued, Resume func()
	// Images, when set, is asked for the provider's images of what was
	// fetched (docs/06-library-and-metadata.md, section 8).
	Images interface {
		Want(ctx context.Context, libraryID, itemID int64, kind, ref string) error
	}
}

// want asks for an item's provider image of a kind.
func (f *Fetcher) want(ctx context.Context, libraryID, itemID int64, kind, ref string) error {
	if f.Images == nil || itemID == 0 {
		return nil
	}
	return f.Images.Want(ctx, libraryID, itemID, kind, ref)
}

// NewFetcher makes the fetcher, which asks p with the key that key gives.
func NewFetcher(d *db.DB, p Provider, key tmdb.KeySource, log *slog.Logger) *Fetcher {
	return &Fetcher{db: d, provider: p, key: key, log: log, now: time.Now}
}

// Kind is the fetcher's kind of background work: two at once, which share
// the provider's two requests at once.
func (f *Fetcher) Kind() tasks.Kind {
	return tasks.Kind{Name: "fetch", Limit: tmdb.Slots, Run: f.run}
}

// Queue queues a fetch for every film and series of a library, or of
// every library with 0, that needs one: never fetched, or past its age
// (db.QueueFetches). again queues what failed too, as the nightly round
// and a library's refresh do. Without a key it queues nothing. It returns
// how many it queued.
func (f *Fetcher) Queue(ctx context.Context, libraryID int64, again bool) (int, error) {
	if key, err := f.key(ctx); err != nil || key == "" {
		return 0, err
	}
	n, err := f.db.QueueFetches(ctx, libraryID, Every.Milliseconds(), Airing.Milliseconds(), again, f.now().UnixMilli())
	if err == nil && n > 0 && f.Queued != nil {
		f.Queued()
	}
	return n, err
}

// Refresh queues the fetch of one film or series that asks the provider
// again, whatever the age of what it has.
func (f *Fetcher) Refresh(ctx context.Context, itemID, libraryID int64) error {
	if err := f.db.QueueFetch(ctx, itemID, libraryID, true, f.now().UnixMilli()); err != nil {
		return err
	}
	if f.Queued != nil {
		f.Queued()
	}
	return nil
}

// KeySet is told that a new metadata key was set: everything that waits
// for metadata is queued, what failed too, and a pause that the old key's
// refusal began ends.
func (f *Fetcher) KeySet(ctx context.Context) {
	if _, err := f.Queue(ctx, 0, true); err != nil {
		f.log.Error("cannot queue fetches after a new metadata key", "err", err)
	}
	if f.Resume != nil {
		f.Resume()
	}
}

type fetchPayload struct {
	Force bool `json:"force"`
}

// run fetches one item. What the provider cannot be asked for now pauses
// the kind: a provider that asked to wait, or a key it refuses. Without a
// key there is nothing to do: setting one queues the fetches again.
func (f *Fetcher) run(ctx context.Context, t db.Task) error {
	var p fetchPayload
	if t.Payload != "" {
		if err := json.Unmarshal([]byte(t.Payload), &p); err != nil {
			return fmt.Errorf("fetch task: %w", err)
		}
	}
	err := f.fetch(ctx, db.ItemOfKey(t.Key), p.Force)
	switch {
	case errors.Is(err, tmdb.ErrNoKey):
		return nil
	case errors.Is(err, tmdb.ErrBadKey):
		return &tasks.Pause{Err: err, For: badKeyPause}
	}
	return tmdb.AsPause(err, f.now())
}

// note is why an item is on the review list, as match_note holds it.
type note struct {
	Reason   string `json:"reason"`
	Provider string `json:"provider"`
	Value    string `json:"value"`
}

func (n note) String() string { b, _ := json.Marshal(n); return string(b) }

// fetch reads a film's or a series' details, and a series' seasons, from
// the provider, or from its cache while young enough, and writes them onto
// the item.
func (f *Fetcher) fetch(ctx context.Context, id int64, force bool) error {
	target, err := f.db.FetchTargetOf(ctx, id)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if (target.Type != "movie" && target.Type != "series") || (target.MatchState != "matched" && target.MatchState != "manual") {
		return nil
	}
	tmdbID := target.IDs["tmdb"]
	if tmdbID == "" {
		if tmdbID, err = f.find(ctx, target); err != nil || tmdbID == "" {
			return err
		}
	}
	age := Every
	if target.Airing {
		age = Airing
	}
	if force {
		age = 0
	}
	path, query := tmdb.MoviePath(tmdbID), tmdb.MovieQuery()
	if target.Type == "series" {
		path, query = tmdb.SeriesPath(tmdbID), tmdb.SeriesQuery()
	}
	ans, err := f.provider.Get(ctx, path, query, age)
	if err != nil {
		return err
	}
	if ans.Status == http.StatusNotFound {
		f.log.Info("TMDB has no title of an item's id; it is on the review list", "item", id, "tmdb", tmdbID)
		return f.db.MarkReview(ctx, id, note{Reason: "id_not_found", Provider: "tmdb", Value: tmdbID}.String())
	}
	now := f.now().UnixMilli()
	if target.Type == "movie" {
		var m tmdb.Movie
		if err := json.Unmarshal(ans.Body, &m); err != nil {
			return fmt.Errorf("fetch: TMDB's details of film %s: %w", tmdbID, err)
		}
		if err := f.db.ApplyMetadata(ctx, id, filmMetadata(m), now); err != nil {
			return err
		}
		return f.wantAll(ctx, target.LibraryID, id, m.PosterPath, m.BackdropPath, m.Images.Logo())
	}
	var s tmdb.Series
	if err := json.Unmarshal(ans.Body, &s); err != nil {
		return fmt.Errorf("fetch: TMDB's details of series %s: %w", tmdbID, err)
	}
	if err := f.db.ApplyMetadata(ctx, id, seriesMetadata(s), now); err != nil {
		return err
	}
	if err := f.wantAll(ctx, target.LibraryID, id, s.PosterPath, s.BackdropPath, s.Images.Logo()); err != nil {
		return err
	}
	// A running count waits for its mapping to seasons (section 6.5 there).
	if target.Numbering == "absolute" {
		return nil
	}
	last := 0
	if s.LastEpisode != nil {
		last = s.LastEpisode.Season
	}
	for _, n := range target.Seasons {
		if !s.HasSeason(n) {
			// The provider lists no such season: the season and its
			// episodes keep their titles until the series' next refresh.
			if _, err := f.db.ApplySeason(ctx, id, db.SeasonMetadata{Number: n}, parse.SortTitle, now); err != nil {
				return err
			}
			continue
		}
		seasonAge := Every
		if s.Airing() && n >= last {
			seasonAge = Airing
		}
		if force {
			seasonAge = 0
		}
		ans, err := f.provider.Get(ctx, tmdb.SeasonPath(tmdbID, n), tmdb.SeasonQuery(), seasonAge)
		if err != nil {
			return err
		}
		var se tmdb.Season
		if ans.Status == http.StatusOK {
			if err := json.Unmarshal(ans.Body, &se); err != nil {
				return fmt.Errorf("fetch: TMDB's season %d of series %s: %w", n, tmdbID, err)
			}
		}
		written, err := f.db.ApplySeason(ctx, id, seasonMetadata(n, se), parse.SortTitle, now)
		if err != nil {
			return err
		}
		if err := f.want(ctx, target.LibraryID, written.SeasonID, "poster", se.PosterPath); err != nil {
			return err
		}
		for _, ep := range slices.Sorted(maps.Keys(written.Stills)) {
			if err := f.want(ctx, target.LibraryID, ep, "thumb", written.Stills[ep]); err != nil {
				return err
			}
		}
	}
	return nil
}

// wantAll asks for a film's or a series' poster, backdrop and logo.
func (f *Fetcher) wantAll(ctx context.Context, libraryID, id int64, poster, backdrop, logo string) error {
	for _, im := range [][2]string{{"poster", poster}, {"backdrop", backdrop}, {"logo", logo}} {
		if err := f.want(ctx, libraryID, id, im[0], im[1]); err != nil {
			return err
		}
	}
	return nil
}

// Again queues the fetch of the films and series that the items given
// are, or belong to, reading what the cache holds: after their local
// images went, the provider's take their place.
func (f *Fetcher) Again(ctx context.Context, items []int64) {
	if key, err := f.key(ctx); err != nil || key == "" {
		return
	}
	queued := false
	for _, id := range items {
		owner, lib, err := f.db.FetchOwner(ctx, id)
		if err == nil && owner != 0 {
			err = f.db.QueueFetch(ctx, owner, lib, false, f.now().UnixMilli())
			queued = true
		}
		if err != nil && !errors.Is(err, db.ErrNotFound) && ctx.Err() == nil {
			f.log.Error("cannot queue a fetch after a local image went", "item", id, "err", err)
		}
	}
	if queued && f.Queued != nil {
		f.Queued()
	}
}

// find asks the provider for its title of the item's IMDb or TheTVDB id,
// keeps its id, and returns it. An item whose ids find nothing goes on the
// review list, and "" is returned.
func (f *Fetcher) find(ctx context.Context, t db.FetchTarget) (string, error) {
	for _, src := range []struct{ provider, source string }{{"imdb", "imdb_id"}, {"tvdb", "tvdb_id"}} {
		v := t.IDs[src.provider]
		if v == "" {
			continue
		}
		ans, err := f.provider.Get(ctx, tmdb.FindPath(v), tmdb.FindQuery(src.source), Every)
		if err != nil {
			return "", err
		}
		var found tmdb.Found
		if ans.Status == http.StatusOK {
			if err := json.Unmarshal(ans.Body, &found); err != nil {
				return "", fmt.Errorf("fetch: TMDB's answer for %s %s: %w", src.provider, v, err)
			}
		}
		id := 0
		if t.Type == "movie" && len(found.Movies) > 0 {
			id = found.Movies[0].ID
		} else if t.Type == "series" && len(found.Series) > 0 {
			id = found.Series[0].ID
		}
		if id != 0 {
			tmdbID := strconv.Itoa(id)
			return tmdbID, f.db.SetMatchID(ctx, t.ID, "tmdb", tmdbID)
		}
	}
	var n note
	for _, p := range []string{"imdb", "tvdb"} {
		if v := t.IDs[p]; v != "" {
			n = note{Reason: "id_not_found", Provider: p, Value: v}
			break
		}
	}
	if n.Reason == "" {
		// Matched without an id any provider is asked by: nothing to do.
		return "", nil
	}
	f.log.Info("TMDB finds no title for an item's id; it is on the review list", "item", t.ID, "provider", n.Provider, "id", n.Value)
	return "", f.db.MarkReview(ctx, t.ID, n.String())
}

// rating is TMDB's average of its users' votes, to one decimal, or 0 when
// nobody has voted.
func rating(avg float64, votes int) float64 {
	if votes == 0 {
		return 0
	}
	return math.Round(avg*10) / 10
}

func names(ns []tmdb.Named) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		if n.Name != "" {
			out = append(out, n.Name)
		}
	}
	return out
}

func filmMetadata(m tmdb.Movie) db.Metadata {
	ids := map[string]string{}
	if v := cmp.Or(m.ExternalIDs.ImdbID, m.ImdbID); v != "" {
		ids["imdb"] = v
	}
	return db.Metadata{Title: strings.TrimSpace(m.Title), SortTitle: parse.SortTitle(strings.TrimSpace(m.Title)),
		OriginalTitle: m.OriginalTitle, Overview: m.Overview, Date: m.ReleaseDate,
		RuntimeMS: int64(m.Runtime) * 60000, Rating: rating(m.VoteAverage, m.VoteCount),
		ContentRating: m.Certification(), Genres: names(m.Genres), IDs: ids}
}

func seriesMetadata(s tmdb.Series) db.Metadata {
	ids := map[string]string{}
	if s.ExternalIDs.ImdbID != "" {
		ids["imdb"] = s.ExternalIDs.ImdbID
	}
	if s.ExternalIDs.TvdbID != 0 {
		ids["tvdb"] = strconv.Itoa(s.ExternalIDs.TvdbID)
	}
	var runtime int64
	if len(s.EpisodeRunTime) > 0 {
		runtime = int64(s.EpisodeRunTime[0]) * 60000
	}
	var aliases []string
	for _, a := range s.AlternativeTitles.Results {
		if a.Title != "" {
			aliases = append(aliases, a.Title)
		}
	}
	title := strings.TrimSpace(s.Name)
	return db.Metadata{Title: title, SortTitle: parse.SortTitle(title), OriginalTitle: s.OriginalName, Overview: s.Overview,
		Date: s.FirstAirDate, RuntimeMS: runtime, Rating: rating(s.VoteAverage, s.VoteCount),
		ContentRating: s.Certification(), Genres: names(s.Genres), Aliases: aliases, IDs: ids, Airing: s.Airing()}
}

func seasonMetadata(n int, s tmdb.Season) db.SeasonMetadata {
	out := db.SeasonMetadata{Number: n, Title: strings.TrimSpace(s.Name), Overview: s.Overview, Date: s.AirDate}
	for _, e := range s.Episodes {
		out.Episodes = append(out.Episodes, db.EpisodeMetadata{Number: e.Number, Title: strings.TrimSpace(e.Name),
			Overview: e.Overview, Date: e.AirDate, RuntimeMS: int64(e.Runtime) * 60000, Rating: rating(e.VoteAverage, e.VoteCount),
			Still: e.StillPath})
	}
	return out
}
