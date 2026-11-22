// SPDX-License-Identifier: GPL-3.0-or-later

package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/model"
	"github.com/gureetk/runnel/internal/tasks"
)

// MissingShown is how many rows that did not join a report shows, unless
// every row is asked for (docs/04-api.md, section 8.5).
const MissingShown = 100

// ErrRunning is returned when an import is asked for while one runs.
var ErrRunning = errors.New("jellyfin: an import is running")

// ErrNoReport is returned when no import has run.
var ErrNoReport = errors.New("jellyfin: no import has run")

// RequestError is a request that cannot be run as it is: the message says
// why.
type RequestError struct{ Msg string }

func (e *RequestError) Error() string { return e.Msg }

// PathMap rewrites the start of Jellyfin's paths, for a Jellyfin that sees
// the files at other paths, as in a container.
type PathMap struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Request is an import to run (docs/06-library-and-metadata.md, section 7).
type Request struct {
	URL, APIKey string
	Users       map[string]string // a Jellyfin user's name -> the name of a person here
	Paths       []PathMap
	DryRun      bool
}

// Missing is a row of Jellyfin's that did not join an item here.
type Missing struct {
	Type   string `json:"type"` // movie, series or episode
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Report is what an import found and did, or would do in a dry run.
type Report struct {
	State      string         `json:"state"` // running, done or failed
	DryRun     bool           `json:"dry_run"`
	At         int64          `json:"at"`
	FinishedAt *int64         `json:"finished_at"`
	Version    string         `json:"version"`
	Joined     int            `json:"joined"`     // films and episodes
	NotJoined  int            `json:"not_joined"` // films and episodes
	Series     int            `json:"series_joined"`
	SeriesNot  int            `json:"series_not_joined"`
	Matches    int            `json:"matches"` // films and series whose provider ids are written
	States     map[string]int `json:"states"`  // by the name of the person here
	Missing    []Missing      `json:"missing"`
	MoreMissed int            `json:"missing_more"`
	Error      *string        `json:"error"`
}

// Gate is the playback gate, which the import waits at between pages.
type Gate interface {
	Wait(ctx context.Context) error
}

// Importer runs the import as the background queue's one task of kind
// "import" (docs/06-library-and-metadata.md, section 9), and keeps the last
// run's report in the data directory.
type Importer struct {
	db   *db.DB
	file string
	gate Gate
	log  *slog.Logger
	// Now is the clock and HTTP the client for Jellyfin's calls: time.Now
	// and a client that times out after a minute, unless a test sets them.
	Now  func() time.Time
	HTTP *http.Client
	// Queued, when set, is called after an import was queued.
	Queued func()

	mu      sync.Mutex
	current *Report // the queued or running import's, or nil
}

// NewImporter keeps its report under dataDir/import.
func NewImporter(d *db.DB, dataDir string, gate Gate, log *slog.Logger) *Importer {
	return &Importer{db: d, file: filepath.Join(dataDir, "import", "jellyfin-report.json"), gate: gate, log: log, Now: time.Now}
}

// Kind is the import's kind of background work, one at a time.
func (im *Importer) Kind() tasks.Kind { return tasks.Kind{Name: "import", Limit: 1, Run: im.runTask} }

// stored is a queued import, as the task's payload keeps it until the run
// ends.
type stored struct {
	URL    string            `json:"url"`
	APIKey string            `json:"api_key"`
	Users  map[string]string `json:"users"`
	Paths  []PathMap         `json:"paths"`
	DryRun bool              `json:"dry_run"`
}

type person struct {
	jellyfinID, here string
	id               int64
}

// Start checks a request against Jellyfin and the people here, at once,
// and then queues the import. It returns a *RequestError, ErrBadKey, an
// *UpstreamError or ErrRunning when it queues nothing.
func (im *Importer) Start(ctx context.Context, req Request) error {
	if im.busy(ctx) {
		return ErrRunning
	}
	c, err := New(req.URL, req.APIKey, im.HTTP)
	if err != nil {
		return &RequestError{strings.TrimPrefix(err.Error(), "jellyfin: ")}
	}
	for i, p := range req.Paths {
		if !path.IsAbs(p.From) || !path.IsAbs(p.To) {
			return &RequestError{"a path to rewrite goes from an absolute path to an absolute path"}
		}
		req.Paths[i] = PathMap{From: path.Clean(p.From), To: path.Clean(p.To)}
	}
	version, people, err := im.resolve(ctx, c, req)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(stored{req.URL, req.APIKey, req.Users, req.Paths, req.DryRun})
	if err != nil {
		return err
	}
	im.mu.Lock()
	defer im.mu.Unlock()
	if im.current != nil {
		return ErrRunning
	}
	if err := im.db.AddTask(ctx, "import", "jellyfin", 0, string(payload), im.Now().UnixMilli()); err != nil {
		return err
	}
	im.current = newReport(req.DryRun, version, people, im.Now())
	if im.Queued != nil {
		im.Queued()
	}
	return nil
}

func newReport(dry bool, version string, people []person, now time.Time) *Report {
	rep := &Report{State: "running", DryRun: dry, At: now.UnixMilli(), Version: version, States: map[string]int{}}
	for _, p := range people {
		rep.States[p.here] = 0
	}
	return rep
}

// busy reports whether an import is queued or running.
func (im *Importer) busy(ctx context.Context) bool {
	im.mu.Lock()
	running := im.current != nil
	im.mu.Unlock()
	if running {
		return true
	}
	n, err := im.db.CountTasks(ctx, "import", "queued", "running")
	return err != nil || n > 0
}

// resolve reads Jellyfin's version and users, and finds the people that
// the request maps them to.
func (im *Importer) resolve(ctx context.Context, c *Client, req Request) (string, []person, error) {
	version, err := c.Version(ctx)
	if err != nil {
		return "", nil, err
	}
	theirs, err := c.Users(ctx)
	if err != nil {
		return "", nil, err
	}
	ours, err := im.db.Users(ctx)
	if err != nil {
		return "", nil, err
	}
	var people []person
	for _, name := range sortedKeys(req.Users) {
		p := person{here: req.Users[name]}
		for _, u := range theirs {
			if strings.EqualFold(u.Name, name) {
				p.jellyfinID = u.ID
			}
		}
		for _, u := range ours {
			if strings.EqualFold(u.Name, p.here) {
				p.id, p.here = u.ID, u.Name
			}
		}
		switch {
		case p.jellyfinID == "":
			return "", nil, &RequestError{"Jellyfin has no user named " + name}
		case p.id == 0:
			return "", nil, &RequestError{"there is no person named " + p.here + " here"}
		}
		people = append(people, p)
	}
	return version, people, nil
}

// runTask runs a queued import. A failure is the report's, not the
// queue's: the import is not tried again by itself. A run that the
// server's stop cuts short is queued again at the next start.
func (im *Importer) runTask(ctx context.Context, t db.Task) error {
	var req stored
	if err := json.Unmarshal([]byte(t.Payload), &req); err != nil {
		return fmt.Errorf("import task: %w", err)
	}
	im.mu.Lock()
	rep := im.current
	if rep == nil {
		rep = newReport(req.DryRun, "", nil, im.Now())
		im.current = rep
	}
	im.mu.Unlock()
	start := time.Now()
	err := func() error {
		c, err := New(req.URL, req.APIKey, im.HTTP)
		if err != nil {
			return err
		}
		version, people, err := im.resolve(ctx, c, Request{Users: req.Users})
		if err != nil {
			return err
		}
		im.mu.Lock()
		rep.Version = version
		for _, p := range people {
			rep.States[p.here] += 0
		}
		im.mu.Unlock()
		return im.passes(ctx, c, Request{Paths: req.Paths, DryRun: req.DryRun}, people, rep)
	}()
	if ctx.Err() != nil {
		im.mu.Lock()
		im.current = nil
		im.mu.Unlock()
		return ctx.Err()
	}
	im.finish(rep, err, time.Since(start))
	return nil
}

// finish ends a run: its report is kept, and the next import may start.
func (im *Importer) finish(rep *Report, err error, took time.Duration) {
	im.mu.Lock()
	at := im.Now().UnixMilli()
	rep.FinishedAt = &at
	rep.State = "done"
	if err != nil {
		rep.State = "failed"
		msg := err.Error()
		rep.Error = &msg
	}
	final := *rep
	im.mu.Unlock()
	if werr := im.save(final); werr != nil {
		im.log.Error("cannot keep the Jellyfin import's report", "err", werr)
	}
	im.mu.Lock()
	im.current = nil
	im.mu.Unlock()
	// A run holds every joined item and a page at a time; give what it
	// freed back before idling (docs/02-architecture.md, section 8).
	debug.FreeOSMemory()
	im.log.Info("Jellyfin import "+final.State, "dry_run", final.DryRun, "joined", final.Joined, "not_joined", final.NotJoined,
		"series_joined", final.Series, "series_not_joined", final.SeriesNot, "matches", final.Matches, "states", final.States,
		"ms", took.Milliseconds(), "err", err)
}

// Report is the queued or running import's report, or else the last
// one's, read from the data directory; all of its rows that did not join
// with full, and otherwise the first MissingShown. ErrNoReport: no import
// has run.
func (im *Importer) Report(ctx context.Context, full bool) (Report, error) {
	im.mu.Lock()
	var rep Report
	running := im.current != nil
	if running {
		rep = *im.current
		rep.Missing = append([]Missing(nil), im.current.Missing...)
		rep.States = make(map[string]int, len(im.current.States))
		for k, v := range im.current.States {
			rep.States[k] = v
		}
	}
	im.mu.Unlock()
	if !running && im.busy(ctx) {
		// Queued again by a stop, and not running yet.
		return Report{State: "running", States: map[string]int{}, Missing: []Missing{}}, nil
	}
	if !running {
		b, err := os.ReadFile(im.file)
		if errors.Is(err, os.ErrNotExist) {
			return Report{}, ErrNoReport
		}
		if err != nil {
			return Report{}, err
		}
		if err := json.Unmarshal(b, &rep); err != nil {
			return Report{}, fmt.Errorf("jellyfin: read the last report: %w", err)
		}
	}
	if !full && len(rep.Missing) > MissingShown {
		rep.MoreMissed, rep.Missing = len(rep.Missing)-MissingShown, rep.Missing[:MissingShown]
	}
	if rep.Missing == nil {
		rep.Missing = []Missing{}
	}
	return rep, nil
}

// passes lists Jellyfin's items once for each person, joins them, and
// writes what they carry, or counts it in a dry run. The first pass also
// counts what joined and writes the provider ids. It waits at the playback
// gate before each page.
func (im *Importer) passes(ctx context.Context, c *Client, req Request, people []person, rep *Report) error {
	libs, err := im.db.Libraries(ctx)
	if err != nil {
		return err
	}
	var roots []model.Root
	for _, l := range libs {
		roots = append(roots, l.Roots...)
	}
	// Longer paths first, so that the deepest root that holds a path wins.
	sort.Slice(roots, func(i, j int) bool { return len(roots[i].Path) > len(roots[j].Path) })
	joined := map[string]db.Joined{} // Jellyfin's item id -> the item here, once joined
	passes := people
	if len(passes) == 0 {
		passes = []person{{}}
	}
	for n, p := range passes {
		first := n == 0
		for offset := 0; ; {
			if err := im.gate.Wait(ctx); err != nil {
				return err
			}
			page, err := c.Items(ctx, p.jellyfinID, offset)
			if err != nil {
				return err
			}
			var matches []db.ImportMatch
			var states []db.ImportState
			for _, it := range page.Items {
				j, ok := joined[it.ID]
				if first {
					var why string
					j, why, err = im.join(ctx, it, roots, req.Paths)
					if err != nil {
						return err
					}
					ok = why == ""
					im.count(rep, it, why)
					if ok {
						joined[it.ID] = j
						if ids := providerIDs(it, j.Type); len(ids) > 0 {
							matches = append(matches, db.ImportMatch{ItemID: j.ItemID, IDs: ids})
						}
					}
				}
				if ok && p.id != 0 && it.UserData != nil && (j.Type == "movie" || j.Type == "episode") {
					d := it.UserData
					states = append(states, db.ImportState{UserID: p.id, ItemID: j.ItemID, FileID: j.FileID, Played: d.Played,
						PlayCount: d.PlayCount, LastPlayedAt: d.LastPlayed(), PositionMS: d.PositionMS()})
				}
			}
			m, err := im.db.ImportMatches(ctx, matches, !req.DryRun)
			if err != nil {
				return err
			}
			s, err := im.db.ImportStates(ctx, states, !req.DryRun)
			if err != nil {
				return err
			}
			im.mu.Lock()
			rep.Matches += m
			if p.id != 0 {
				rep.States[p.here] += s
			}
			im.mu.Unlock()
			offset += len(page.Items)
			if len(page.Items) == 0 || offset >= page.Total {
				break
			}
		}
	}
	return nil
}

// count adds a row to the report: joined when why is "".
func (im *Importer) count(rep *Report, it Item, why string) {
	im.mu.Lock()
	defer im.mu.Unlock()
	series := it.Type == "Series"
	switch {
	case why == "" && series:
		rep.Series++
	case why == "":
		rep.Joined++
	case series:
		rep.SeriesNot++
	default:
		rep.NotJoined++
	}
	if why != "" {
		rep.Missing = append(rep.Missing, Missing{Type: typeHere[it.Type], Path: it.Path, Reason: why})
	}
}

// typeHere is the type here of each type of Jellyfin's that the import
// lists.
var typeHere = map[string]string{"Movie": "movie", "Series": "series", "Episode": "episode"}

// join finds the item that a Jellyfin item is here: a film or an episode
// by the file at its path, a series by its folder. why says why it did not
// join, or is "".
func (im *Importer) join(ctx context.Context, it Item, roots []model.Root, paths []PathMap) (db.Joined, string, error) {
	want, known := typeHere[it.Type]
	if !known {
		return db.Joined{}, "Jellyfin gave a " + it.Type + ", which the import does not read", nil
	}
	if it.Path == "" {
		return db.Joined{}, "Jellyfin gives no path for it", nil
	}
	local := Rewrite(it.Path, paths)
	var root *model.Root
	for i := range roots {
		if local == roots[i].Path || strings.HasPrefix(local, strings.TrimSuffix(roots[i].Path, "/")+"/") {
			root = &roots[i]
			break
		}
	}
	if root == nil {
		return db.Joined{}, "it is outside every library folder here, as " + local, nil
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(local, root.Path), "/")
	var j db.Joined
	var err error
	if want == "series" {
		j, err = im.db.SeriesOfFolder(ctx, root.ID, rel)
		if errors.Is(err, db.ErrNotFound) {
			return db.Joined{}, "no series here is built from that folder", nil
		}
	} else {
		j, err = im.db.ItemOfFile(ctx, root.ID, rel)
		switch {
		case errors.Is(err, db.ErrNotFound):
			return db.Joined{}, "no file at that path here", nil
		case errors.Is(err, db.ErrNoItem):
			return db.Joined{}, "the file is no film's or episode's here", nil
		}
	}
	if err != nil {
		return db.Joined{}, "", err
	}
	if j.Type != want {
		return db.Joined{}, "it is a " + j.Type + " here", nil
	}
	return j, "", nil
}

// Rewrite rewrites the start of a path by the map whose From is the
// longest that holds it, matching whole folders.
func Rewrite(p string, paths []PathMap) string {
	best := -1
	for i, m := range paths {
		from := strings.TrimSuffix(m.From, "/")
		if (p == from || strings.HasPrefix(p, from+"/") || from == "") && (best < 0 || len(from) > len(strings.TrimSuffix(paths[best].From, "/"))) {
			best = i
		}
	}
	if best < 0 {
		return p
	}
	from := strings.TrimSuffix(paths[best].From, "/")
	return path.Clean(paths[best].To + "/" + strings.TrimPrefix(p, from))
}

// providerIDs are the provider ids of a film or a series, by the names
// here.
func providerIDs(it Item, typ string) map[string]string {
	if typ != "movie" && typ != "series" {
		return nil
	}
	out := map[string]string{}
	for k, v := range it.ProviderIDs {
		if name, ok := Providers[strings.ToLower(k)]; ok && strings.TrimSpace(v) != "" {
			out[name] = strings.TrimSpace(v)
		}
	}
	return out
}

// save writes a report into the data directory, in place of the last.
func (im *Importer) save(rep Report) error {
	if err := os.MkdirAll(filepath.Dir(im.file), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	tmp := im.file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, im.file)
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
