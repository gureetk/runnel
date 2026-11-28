// SPDX-License-Identifier: GPL-3.0-or-later

package meta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/meta/tmdb"
	"github.com/gureetk/runnel/internal/tasks"
)

// Matcher searches the provider for the films and series that have no
// match, by the title and year their names give, and takes the best
// candidate when it is clearly the best: the background work of kind
// match (docs/06-library-and-metadata.md, sections 6.2 and 9).
type Matcher struct {
	db       *db.DB
	provider Provider
	key      tmdb.KeySource
	log      *slog.Logger
	now      func() time.Time
	// Queued, when set, is called after searches were queued.
	Queued func()
	// Matched, when set, is told of an item that a search matched, whose
	// metadata is then to be fetched.
	Matched func(ctx context.Context, itemID, libraryID int64)
}

// NewMatcher makes the matcher, which asks p with the key that key gives.
func NewMatcher(d *db.DB, p Provider, key tmdb.KeySource, log *slog.Logger) *Matcher {
	return &Matcher{db: d, provider: p, key: key, log: log, now: time.Now}
}

// Kind is the matcher's kind of background work: two at once, which share
// the provider's two requests at once with the fetches.
func (m *Matcher) Kind() tasks.Kind {
	return tasks.Kind{Name: "match", Limit: tmdb.Slots, Run: m.run}
}

// Queue queues a search for every film and series of a library, or of
// every library with 0, that needs one (db.QueueMatches). again queues
// what failed too, and what was searched for more than 30 days ago.
// Without a key it queues nothing. It returns how many it queued.
func (m *Matcher) Queue(ctx context.Context, libraryID int64, again bool) (int, error) {
	if key, err := m.key(ctx); err != nil || key == "" {
		return 0, err
	}
	n, err := m.db.QueueMatches(ctx, libraryID, again, Every.Milliseconds(), m.now().UnixMilli())
	if err == nil && n > 0 && m.Queued != nil {
		m.Queued()
	}
	return n, err
}

// Refresh queues one item's search, which asks the provider again
// whatever the age of what it answered before. An item that may not be
// searched for is left as it is when the search runs.
func (m *Matcher) Refresh(ctx context.Context, itemID, libraryID int64) error {
	if err := m.db.QueueMatch(ctx, itemID, libraryID, true, m.now().UnixMilli()); err != nil {
		return err
	}
	if m.Queued != nil {
		m.Queued()
	}
	return nil
}

// run searches for one item; what the provider cannot be asked for now
// pauses the kind, as a fetch does.
func (m *Matcher) run(ctx context.Context, t db.Task) error {
	var p fetchPayload
	if t.Payload != "" {
		if err := json.Unmarshal([]byte(t.Payload), &p); err != nil {
			return fmt.Errorf("match task: %w", err)
		}
	}
	err := m.match(ctx, db.ItemOfKey(t.Key), p.Force)
	switch {
	case errors.Is(err, tmdb.ErrNoKey):
		return nil
	case errors.Is(err, tmdb.ErrBadKey):
		return &tasks.Pause{Err: err, For: badKeyPause}
	}
	return tmdb.AsPause(err, m.now())
}

// searchNote is why a search took no match, as match_note keeps it, with
// what was searched for and when, so that a new title or year, or a
// month, searches again.
type searchNote struct {
	Reason     string  `json:"reason"` // no_results, low_score or too_close
	Candidates int     `json:"candidates"`
	Best       float64 `json:"best,omitempty"`
	Second     float64 `json:"second,omitempty"`
	Title      string  `json:"title"`
	Year       *int    `json:"year"`
	At         int64   `json:"at"`
}

// Candidate is one title a search found for an item, with its score.
type Candidate struct {
	Provider      string  `json:"provider"`
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"original_title"`
	Year          int     `json:"year"`
	PosterPath    string  `json:"-"`
	Score         float64 `json:"score"`
}

// match searches for one item and writes what it found.
func (m *Matcher) match(ctx context.Context, id int64, force bool) error {
	t, err := m.db.MatchTargetOf(ctx, id)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err != nil || !t.Searchable {
		return err
	}
	age := Every
	if force {
		age = 0
	}
	cands, err := m.Search(ctx, t.Type, t.Title, t.Year, age)
	if err != nil {
		return err
	}
	out := decide(cands)
	n := searchNote{Reason: out.reason, Candidates: len(cands), Title: t.Title, At: m.now().UnixMilli()}
	if t.Year > 0 {
		n.Year = &t.Year
	}
	if len(cands) > 0 {
		n.Best = cands[0].Score
	}
	if len(cands) > 1 {
		n.Second = cands[1].Score
	}
	note, _ := json.Marshal(n)
	so := db.SearchOutcome{Score: n.Best, Candidates: len(cands), Note: string(note)}
	if out.reason == "" {
		so.Provider, so.Value = "tmdb", cands[0].ID
	}
	matched, err := m.db.ApplySearch(ctx, id, t.Title, t.Year, so)
	if err != nil {
		return err
	}
	if matched {
		m.log.Info("matched by search", "item", id, "tmdb", cands[0].ID, "score", cands[0].Score)
		if m.Matched != nil {
			m.Matched(ctx, id, t.LibraryID)
		}
	}
	return nil
}

// decision is whether the best candidate is taken: reason is "" when it
// is, or why not.
type decision struct{ reason string }

// decide takes the best of candidates in order of score when it scores at
// least Accept and leads the second by at least Lead (P4).
func decide(cands []Candidate) decision {
	switch {
	case len(cands) == 0:
		return decision{"no_results"}
	case cands[0].Score < Accept:
		return decision{"low_score"}
	case len(cands) > 1 && round3(cands[0].Score-cands[1].Score) < Lead:
		return decision{"too_close"}
	}
	return decision{}
}

func round3(x float64) float64 { return math.Round(x*1000) / 1000 }

// Search asks the provider for the titles of a type that a title and a
// year may name, from its cache while its answer is younger than age, and
// scores them, best first. With a year it asks first for titles of that
// year; when none of them is within a year of it, it asks again without
// the year, so that a title dated otherwise at the provider is found.
func (m *Matcher) Search(ctx context.Context, typ, title string, year int, age time.Duration) ([]Candidate, error) {
	path, query := tmdb.MovieSearchPath, tmdb.MovieSearchQuery
	if typ == "series" {
		path, query = tmdb.SeriesSearchPath, tmdb.SeriesSearchQuery
	}
	cands, err := m.ask(ctx, path, query(title, year), title, year, age)
	if err != nil || year == 0 || slices.ContainsFunc(cands, func(c Candidate) bool { return c.Year >= year-1 && c.Year <= year+1 }) {
		return cands, err
	}
	more, err := m.ask(ctx, path, query(title, 0), title, year, age)
	if err != nil {
		return nil, err
	}
	for _, c := range more {
		if !slices.ContainsFunc(cands, func(o Candidate) bool { return o.ID == c.ID }) {
			cands = append(cands, c)
		}
	}
	slices.SortStableFunc(cands, func(a, b Candidate) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		}
		return 0
	})
	return cands, nil
}

// ask makes one search and scores what it answers, best first, the
// provider's order kept among equal scores.
func (m *Matcher) ask(ctx context.Context, path string, q url.Values, title string, year int, age time.Duration) ([]Candidate, error) {
	ans, err := m.provider.Get(ctx, path, q, age)
	if err != nil {
		return nil, err
	}
	if ans.Status == http.StatusNotFound {
		return nil, nil
	}
	var page tmdb.SearchPage
	if err := json.Unmarshal(ans.Body, &page); err != nil {
		return nil, fmt.Errorf("match: TMDB's answer to a search: %w", err)
	}
	cands := make([]Candidate, 0, len(page.Results))
	for _, r := range page.Results {
		if r.ID <= 0 {
			continue
		}
		name, original := r.Names()
		cands = append(cands, Candidate{Provider: "tmdb", ID: strconv.Itoa(r.ID), Title: name, OriginalTitle: original,
			Year: r.Year(), PosterPath: r.PosterPath, Score: round3(Score(title, year, name, original, r.Year()))})
	}
	slices.SortStableFunc(cands, func(a, b Candidate) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		}
		return 0
	})
	return cands, nil
}
