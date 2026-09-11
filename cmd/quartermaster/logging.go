package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// defaultLogMaxBytes caps the live log file before it is rotated. The
// quartermaster runs for months at a time, so an unbounded append would
// eventually cost more than the crash history it is keeping.
const defaultLogMaxBytes = 10 << 20

// fileLog is an io.Writer that appends to one file and, once the file reaches
// maxBytes, moves it aside to <path>.old (discarding the previous .old) and
// continues in a fresh file. One backup is the point: it keeps "what was
// happening before this episode" without keeping a year of access logs.
type fileLog struct {
	path     string
	maxBytes int64
	mu       sync.Mutex
	f        *os.File
	size     int64
}

func newFileLog(path string, maxBytes int64) (*fileLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	l := &fileLog{path: path, maxBytes: maxBytes}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

// open creates (or re-opens) the live file. A file that is ALREADY at the cap
// when we start — the previous run never rotated, or the cap shrank — is
// rotated first, so we never append into an oversized file.
func (l *fileLog) open() error {
	if st, err := os.Stat(l.path); err == nil && st.Size() >= l.maxBytes {
		_ = os.Remove(l.path + ".old")
		_ = os.Rename(l.path, l.path+".old")
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f = f
	l.size = st.Size()
	return nil
}

func (l *fileLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return 0, fmt.Errorf("log file %s is closed", l.path)
	}
	if l.size+int64(len(p)) > l.maxBytes {
		if err := l.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

// rotate closes the live file, moves it to <path>.old and starts a fresh one.
// The remove-before-rename is for Windows, where a rename over an existing
// file fails outright.
func (l *fileLog) rotate() error {
	if err := l.f.Close(); err != nil {
		return err
	}
	l.f = nil
	_ = os.Remove(l.path + ".old")
	if err := os.Rename(l.path, l.path+".old"); err != nil {
		// The live file vanished underneath us (moved by another tool, say);
		// fall through and start clean rather than dropping the log.
	}
	return l.open()
}

func (l *fileLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
