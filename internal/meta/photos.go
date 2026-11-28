// SPDX-License-Identifier: GPL-3.0-or-later

package meta

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/meta/tmdb"
	"github.com/gureetk/runnel/internal/tasks"
)

// photoSize is the size of TMDB's people's photos that is downloaded:
// 632 pixels high, 421 wide at 2:3, as wide as the widest width kept
// (docs/06-library-and-metadata.md, sections 6.4 and 8).
const photoSize = "h632"

// The bounds on fetching a photo for a request, which needs no token
// (docs/04-api.md, section 6).
const (
	// PhotoSlots is how many photos are downloaded at once.
	PhotoSlots = 2
	// photoTimeout is how long one download and its scaling may take.
	photoTimeout = 15 * time.Second
	// slotWait is how long a request waits for a slot before it is told
	// to come back.
	slotWait = 15 * time.Second
	// busyRetry is when a request that found every slot taken is told to
	// come back.
	busyRetry = 5 * time.Second
	// photoRetry is how long a photo that could not be fetched is not
	// tried again (P7).
	photoRetry = 24 * time.Hour
	// photoPause is how long no photo is downloaded after the provider
	// could not be reached or failed, and the most a 429 may ask.
	photoPause    = time.Minute
	photoPauseMax = time.Hour
)

// photoRef is the shape of a provider's path that is fetched: one file
// name below the size, nothing that could change the address.
var photoRef = regexp.MustCompile(`^/[A-Za-z0-9_-]+\.[A-Za-z0-9]+$`)

// Photos fetches a person's photo the first time a client asks for it
// (D33), and never before: matching downloads none.
type Photos struct {
	art  *Artwork
	key  tmdb.KeySource
	log  *slog.Logger
	http *http.Client
	now  func() time.Time

	slots chan struct{}

	mu     sync.Mutex
	active map[string]*photoCall // the download of each key under way
	until  time.Time             // no download before this
}

// photoCall is one download, which every request for its key waits on.
type photoCall struct {
	done chan struct{}
	ok   bool
	wait time.Duration // > 0: come back after it
}

// NewPhotos fetches photos into the artwork's folder, while a metadata
// key is set.
func NewPhotos(art *Artwork, key tmdb.KeySource, log *slog.Logger) *Photos {
	return &Photos{art: art, key: key, log: log, now: time.Now,
		http:   &http.Client{Timeout: photoTimeout},
		slots:  make(chan struct{}, PhotoSlots),
		active: map[string]*photoCall{},
	}
}

// Photo answers whether the photo of key is on disk, fetching it first
// when it may. With ok false and wait 0 there is no such photo, now or
// for a day; a wait asks the client to come back after it.
//
// Only a key that a person's row holds is fetched, from the path the
// provider's credits gave. One download runs for a key at a time, and at
// most PhotoSlots at once. A try that fails is not repeated for a day,
// and one that met a provider it could not reach, or that answered 429
// or an error of its own, stops every download for a minute, or for what
// a 429 asks.
func (p *Photos) Photo(ctx context.Context, key string) (ok bool, wait time.Duration) {
	ph, err := p.art.db.PhotoOf(ctx, key)
	if err != nil {
		if !errors.Is(err, db.ErrNotFound) && ctx.Err() == nil {
			p.log.Error("cannot read a person's photo", "err", err)
		}
		return false, 0
	}
	if ph.FetchedAt > 0 && p.there(key) {
		return true, 0
	}
	now := p.now()
	if ph.TriedAt > 0 && now.Sub(time.UnixMilli(ph.TriedAt)) < photoRetry {
		return false, 0
	}
	if !photoRef.MatchString(ph.Ref) || p.art.ffmpeg == "" || p.art.ffprobe == "" {
		return false, 0
	}
	if k, err := p.key(ctx); err != nil || k == "" {
		return false, 0 // without a key nothing is fetched
	}
	p.mu.Lock()
	if left := p.until.Sub(now); left > 0 {
		p.mu.Unlock()
		return false, left
	}
	call, running := p.active[key]
	if !running {
		call = &photoCall{done: make(chan struct{})}
		p.active[key] = call
		// The download goes on when the request that began it goes away:
		// another may be waiting for it, and its result is kept.
		go p.fetch(key, ph, call)
	}
	p.mu.Unlock()
	select {
	case <-call.done:
		return call.ok, call.wait
	case <-ctx.Done():
		return false, 0
	}
}

// there is whether every width of the photo of key is on disk.
func (p *Photos) there(key string) bool {
	for _, w := range db.PhotoWidths {
		if _, err := os.Stat(p.art.Path(key, "photo", w)); err != nil {
			return false
		}
	}
	return true
}

// fetch downloads one photo, crops and scales it, and records how that
// went.
func (p *Photos) fetch(key string, ph db.Photo, call *photoCall) {
	defer func() {
		p.mu.Lock()
		delete(p.active, key)
		p.mu.Unlock()
		close(call.done)
	}()
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	case <-time.After(slotWait):
		call.wait = busyRetry
		return
	}
	p.mu.Lock()
	left := p.until.Sub(p.now())
	p.mu.Unlock()
	if left > 0 { // a download that failed while this one waited
		call.wait = left
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), photoTimeout)
	defer cancel()
	err := p.download(ctx, key, ph.Ref)
	now := p.now()
	var pause *tasks.Pause
	switch {
	case err == nil:
		color, cerr := p.color(ctx, key)
		if cerr != nil {
			p.log.Warn("cannot read the colour of a person's photo", "person", ph.PersonID, "err", cerr)
		}
		if err := p.art.db.SetPhoto(context.Background(), key, ph.Ref, color, now.UnixMilli()); err != nil {
			p.log.Error("cannot record a person's photo", "err", err)
		}
		call.ok = true
		return
	case errors.As(err, &pause):
		// ffmpeg or ffprobe cannot be run: not the photo's fault.
		p.log.Error("cannot scale a person's photo", "err", err)
		return
	}
	var down *photoDown
	if errors.As(err, &down) {
		p.mu.Lock()
		p.until = now.Add(down.wait)
		p.mu.Unlock()
	}
	p.log.Warn("a person's photo could not be fetched", "person", ph.PersonID, "err", err)
	if err := p.art.db.PhotoFailed(context.Background(), key, ph.Ref, now.UnixMilli()); err != nil {
		p.log.Error("cannot record a person's photo that failed", "err", err)
	}
}

// photoDown is a failure of the provider rather than of the photo: it
// could not be reached, or answered 429 or an error of its own. wait is
// how long no photo is downloaded after it.
type photoDown struct {
	err  error
	wait time.Duration
}

func (e *photoDown) Error() string { return e.err.Error() }

// download fetches the photo at ref and writes it at every width kept,
// cropped to 2:3. The download goes to the image folder as it arrives,
// and is removed once scaled.
func (p *Photos) download(ctx context.Context, key, ref string) error {
	if err := os.MkdirAll(filepath.Join(p.art.dir, key[:2]), 0o755); err != nil {
		return err
	}
	file := filepath.Join(p.art.dir, key[:2], fmt.Sprintf("%s.download%x%s", key, rand.Uint64(), path.Ext(ref)))
	defer os.Remove(file)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.art.base+photoSize+ref, nil)
	if err != nil {
		return err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return &photoDown{fmt.Errorf("photo: %w", err), photoPause}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return &photoDown{errors.New("photo: the provider answered 429"), retryAfter(resp.Header.Get("Retry-After"), p.now())}
	case resp.StatusCode >= 500:
		return &photoDown{fmt.Errorf("photo: the provider answered %d", resp.StatusCode), photoPause}
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("photo: the provider answered %d", resp.StatusCode)
	}
	if err := writeAtMost(file, resp.Body, maxImage); err != nil {
		if errors.Is(err, errTooLarge) {
			return err
		}
		return &photoDown{fmt.Errorf("photo: %w", err), photoPause}
	}
	for _, w := range db.PhotoWidths {
		out := p.art.Path(key, "photo", w)
		tmp := fmt.Sprintf("%s.tmp%x.jpg", out, rand.Uint64())
		h := int(float64(w)/db.PhotoAspect + 0.5)
		crop := "crop=w='min(iw,trunc(ih*2/3))':h='min(ih,trunc(iw*3/2))',scale=" + strconv.Itoa(w) + ":" + strconv.Itoa(h)
		if _, err := p.art.ffrun(ctx, p.art.ffmpeg, "-v", "error", "-nostdin", "-y", "-i", file,
			"-vf", crop, "-frames:v", "1", "-q:v", "3", tmp); err != nil {
			os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, out); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	return nil
}

// color is the average colour of the photo's smallest width.
func (p *Photos) color(ctx context.Context, key string) (string, error) {
	rgb, err := p.art.ffrun(ctx, p.art.ffmpeg, "-v", "error", "-nostdin", "-i", p.art.Path(key, "photo", db.PhotoWidths[0]),
		"-vf", "scale=1:1:flags=area", "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1")
	if err != nil {
		return "", err
	}
	if len(rgb) < 3 {
		return "", errors.New("ffmpeg gave no colour")
	}
	return fmt.Sprintf("#%02x%02x%02x", rgb[0], rgb[1], rgb[2]), nil
}

// retryAfter is how long a 429 asks to wait, in seconds or until a date:
// a minute without one, an hour at most.
func retryAfter(h string, now time.Time) time.Duration {
	d := photoPause
	if s, err := strconv.Atoi(h); err == nil && s >= 0 {
		d = time.Duration(s) * time.Second
	} else if t, err := http.ParseTime(h); err == nil {
		d = t.Sub(now)
	}
	return min(max(d, time.Second), photoPauseMax)
}

// Drop deletes the people's photos fetched before before (D53), so that
// the next request fetches each again, and returns how many went.
func (p *Photos) Drop(ctx context.Context, before time.Time) (int, error) {
	keys, err := p.art.db.DropStalePhotos(ctx, before.UnixMilli())
	if err != nil {
		return 0, err
	}
	p.art.removeFiles(keys)
	return len(keys), nil
}
