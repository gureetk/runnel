// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux

package scan

import (
	"context"
	"errors"
	"log/slog"
)

// Folders are watched with inotify, which only Linux has. Elsewhere every
// root is scanned every pollEvery.
type watchSys struct{}

func newWatchSys() (*watchSys, error) { return nil, errors.New("this system has no inotify") }

func (s *watchSys) close()                                            {}
func (s *watchSys) watching(int64) bool                               { return false }
func (s *watchSys) addTree(*slog.Logger, int64, string, string) error { return errors.ErrUnsupported }
func (s *watchSys) removeTree(int64, string)                          {}
func (w *Watcher) read(context.Context)                               {}
func isNoSpace(error) bool                                            { return false }
