// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

package main

import (
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

func termios(fd uintptr, req uintptr, t *syscall.Termios) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(t))); e != 0 {
		return e
	}
	return nil
}

// isTerminal reports whether fd is a terminal.
func isTerminal(fd uintptr) bool {
	var t syscall.Termios
	return termios(fd, syscall.TCGETS, &t) == nil
}

// readSecret reads one line from the terminal fd with echo off, and puts
// the terminal back as it was, also when the read is interrupted.
func readSecret(fd uintptr) (string, error) {
	var old syscall.Termios
	if err := termios(fd, syscall.TCGETS, &old); err != nil {
		return "", err
	}
	quiet := old
	quiet.Lflag &^= syscall.ECHO
	quiet.Lflag |= syscall.ICANON | syscall.ISIG
	quiet.Iflag |= syscall.ICRNL
	if err := termios(fd, syscall.TCSETS, &quiet); err != nil {
		return "", err
	}
	restore := func() { termios(fd, syscall.TCSETS, &old) }
	defer restore()

	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	defer func() { signal.Stop(interrupted); close(done) }()
	go func() {
		select {
		case <-interrupted:
			restore()
			os.Exit(130)
		case <-done:
		}
	}()

	var line []byte
	var b [1]byte
	for {
		n, err := syscall.Read(int(fd), b[:])
		if err != nil {
			return "", err
		}
		if n == 0 {
			if len(line) == 0 {
				return "", io.EOF
			}
			break
		}
		if b[0] == '\n' {
			break
		}
		line = append(line, b[0])
		if len(line) > 1024 {
			return "", errors.New("the password is too long")
		}
	}
	return string(line), nil
}
