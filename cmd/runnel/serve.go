// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gureetk/runnel/internal/api"
	"github.com/gureetk/runnel/internal/auth"
	"github.com/gureetk/runnel/internal/config"
	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/library"
	"github.com/gureetk/runnel/internal/meta"
	"github.com/gureetk/runnel/internal/meta/jellyfin"
	"github.com/gureetk/runnel/internal/play"
	"github.com/gureetk/runnel/internal/probe"
	"github.com/gureetk/runnel/internal/scan"
	"github.com/gureetk/runnel/internal/tasks"
)

// shutdownHTTP is how long open requests get to finish after SIGTERM. What
// is still open then, such as a player's stream, is cut, so that the whole
// shutdown takes under 5 seconds (docs/02-architecture.md, section 5).
const shutdownHTTP = 3 * time.Second

func serve(args []string, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "runnel serve takes no arguments: its settings are RUNNEL_ environment variables")
		return 2
	}
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "runnel: %v\n", err)
		return 1
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if err := runServer(ctx, cfg, log); err != nil {
		log.Error("stopped", "err", err)
		return 1
	}
	return 0
}

// runServer runs the server until ctx ends, then shuts it down in order:
// stop taking requests, cut what is still open, close the database once
// its writer is done.
func runServer(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("data directory: %w", err)
	}
	database, err := db.Open(ctx, filepath.Join(cfg.DataDir, "runnel.db"))
	if err != nil {
		return err
	}
	defer database.Close()

	started := time.Now()
	instanceID, err := database.InstanceID(ctx)
	if err != nil {
		return err
	}
	name := cfg.Name
	if name == "" {
		if name, err = os.Hostname(); err != nil || name == "" {
			name = "runnel"
		}
	}
	// A new local admin token at every start, for the subcommands.
	adminHash, err := auth.WriteAdminToken(cfg.DataDir)
	if err != nil {
		return err
	}
	// Without a person, the first run needs the setup code (D35): the one
	// an earlier start made, or a new one.
	setupCode, err := database.SetupCode(ctx, auth.NewSetupCode)
	if err != nil {
		return err
	}
	if setupCode != "" {
		log.Warn("no person exists yet: open the server's address and enter the setup code, or run runnel user add",
			"setup_code", auth.ShowSetupCode(setupCode))
	}

	// The background queue, paused while a playback starts, and the probes
	// it runs. Without ffprobe the server still indexes, and probes
	// nothing: the probes wait in the queue.
	gate := tasks.NewGate()
	queue := tasks.New(database, gate, log)
	ffprobe := cfg.FFprobe
	if ffprobe == "" {
		ffprobe, _ = exec.LookPath("ffprobe")
	}
	if ffprobe == "" {
		log.Warn("ffprobe is not on PATH: nothing will be probed; set RUNNEL_FFPROBE to its path")
	} else {
		queue.Register(scan.NewProbes(database, probe.New(ffprobe, scan.ProbeWorkers), log).Kind())
	}
	// Images are scaled by an ffmpeg child, and need ffprobe for their
	// size.
	ffmpeg := cfg.FFmpeg
	if ffmpeg == "" {
		ffmpeg, _ = exec.LookPath("ffmpeg")
	}
	if ffmpeg == "" {
		log.Warn("ffmpeg is not on PATH: images in library folders are not shown; set RUNNEL_FFMPEG to its path")
	}
	artwork := meta.NewArtwork(database, cfg.DataDir, ffmpeg, ffprobe, log)
	artwork.Queued = queue.Wake
	queue.Register(artwork.Kind())
	// The Jellyfin import is a task of its own, which waits at the gate
	// between pages too.
	importer := jellyfin.NewImporter(database, cfg.DataDir, gate, log)
	importer.Queued = queue.Wake
	queue.Register(importer.Kind())

	scanner := scan.New(database, log)
	scanner.Queued = queue.Wake
	scanner.Images = artwork.Sync
	// The watcher follows each root the scanner has checked, and asks it
	// to scan what changed.
	watcher := scan.NewWatcher(scanner, log)
	scanner.Checked = watcher.Watch
	scanner.Forgotten = watcher.Forget
	scanner.Built = func(ctx context.Context, libraryID int64) error {
		start := time.Now()
		res, err := library.Build(ctx, database, libraryID, time.Now().UnixMilli())
		if err != nil {
			return err
		}
		log.Info("items built", "library", libraryID, "ms", time.Since(start).Milliseconds(),
			"added", res.Added, "changed", res.Changed, "removed", res.Removed)
		return nil
	}
	// At start and each night: the delete markers older than 90 days go,
	// with what only they could tell (docs/03-data-model.md, section 5),
	// and so do the files of images whose items went (section 4 there).
	scanner.Nightly = func(ctx context.Context) {
		if p, err := database.Prune(ctx, time.Now().UnixMilli()); err != nil {
			log.Error("pruning failed", "err", err)
		} else if p.Tombstones+p.Listings+p.Prefs > 0 {
			log.Info("pruned", "tombstones", p.Tombstones, "listings", p.Listings, "prefs", p.Prefs, "floor", p.Floor)
		}
		if n, err := artwork.Sweep(ctx); err != nil && ctx.Err() == nil {
			log.Error("cannot delete unused image files", "err", err)
		} else if n > 0 {
			log.Info("deleted unused image files", "files", n)
		}
	}
	background, stopBackground := context.WithCancel(context.Background())
	defer stopBackground()
	scanning := make(chan struct{})
	go func() {
		defer close(scanning)
		scanner.Run(background)
	}()
	queueing := make(chan struct{})
	go func() {
		defer close(queueing)
		if err := queue.Run(background); err != nil {
			log.Error("background work stopped", "err", err)
		}
	}()
	watching := make(chan struct{})
	go func() {
		defer close(watching)
		watcher.Run(background)
	}()

	// What the server can encode, checked before it listens, so that the
	// first GET /server already says (docs/05-playback.md, section 8).
	encoders := play.NewEncoders(encoderCheck(cfg, ffmpeg))
	checkStart := time.Now()
	checked, _ := encoders.Check(ctx)
	logEncoders(log, cfg, checked, time.Since(checkStart))

	handler := api.New(database, log, api.Config{
		Info:           api.Info{Name: name, NameFromEnv: cfg.Name != "", Version: version, InstanceID: instanceID},
		AdminTokenHash: adminHash,
		Scans:          scanner,
		Gate:           gate,
		Images:         artwork,
		Work:           queue,
		Imports:        importer,
		Encoders:       encoders,
		Slots:          cfg.TranscodeSlots,
		StartedAt:      started,
	}).Handler()
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: a player keeps a file's stream open for as long
		// as it plays. JSON routes set their own deadline.
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	log.Info("listening", "addr", ln.Addr().String(), "version", version,
		"data", cfg.DataDir, "schema", db.SchemaVersion())

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}

	log.Info("stopping")
	start := time.Now()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownHTTP)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		srv.Close()
	}
	if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Debug("requests stopped", "ms", time.Since(start).Milliseconds())
	// Stop the scanner and the queue: ffprobe children are killed, and a
	// write in progress ends before the database closes.
	stopBackground()
	<-scanning
	<-queueing
	<-watching
	log.Debug("background work stopped", "ms", time.Since(start).Milliseconds())
	if err := database.Close(); err != nil {
		return err
	}
	log.Info("stopped", "ms", time.Since(start).Milliseconds())
	return nil
}

// encoderCheck is the check of what this server can encode, with the
// settings of docs/02-architecture.md, section 6.
func encoderCheck(cfg config.Config, ffmpeg string) play.Check {
	return play.Check{FFmpeg: ffmpeg, HWAccel: cfg.HWAccel, Device: cfg.VAAPIDevice}
}

// logEncoders says what the check found. A GPU that was asked for and does
// not work is a warning; one that is simply not there is not.
func logEncoders(log *slog.Logger, cfg config.Config, res play.Result, took time.Duration) {
	attrs := []any{"hw", res.HW(), "ms", took.Milliseconds()}
	for _, r := range res.Rows {
		if r.Works {
			attrs = append(attrs, play.RowName(r), "works")
		} else {
			attrs = append(attrs, play.RowName(r), r.Error)
		}
	}
	log.Info("encoders checked", attrs...)
	video, _ := res.Video()
	switch {
	case len(video) == 0:
		log.Warn("this server can transcode nothing: run runnel doctor to see why")
	case cfg.HWAccel == play.HWVAAPI && res.HW() != "vaapi":
		log.Warn("RUNNEL_HWACCEL is vaapi, and the GPU encodes nothing: transcodes are in software; run runnel doctor to see why")
	}
}
