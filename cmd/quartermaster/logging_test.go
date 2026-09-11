package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileLog_RotatesAtCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quartermaster.log")
	l, err := newFileLog(path, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Cross the cap several times: the live file must never stay over it, and
	// the old bytes must survive in .old rather than being dropped.
	first := strings.Repeat("a", 10)
	second := strings.Repeat("b", 10)
	third := strings.Repeat("c", 10)
	for _, s := range []string{first, second, third} {
		if _, err := l.Write([]byte(s + "\n")); err != nil {
			t.Fatal(err)
		}
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("live file missing: %v", err)
	}
	if st.Size() > 32 {
		t.Fatalf("live file grew to %d bytes past the 32-byte cap", st.Size())
	}

	live, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(live), third) {
		t.Fatalf("live file lost the newest data: %q", live)
	}

	old, err := os.ReadFile(path + ".old")
	if err != nil {
		t.Fatalf("rotated backup missing: %v", err)
	}
	if !strings.Contains(string(old), first) {
		t.Fatalf("backup lost the earliest data: %q", old)
	}
}

func TestFileLog_RotatesOnOpenWhenAlreadyAtCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quartermaster.log")
	// A file left at the cap by a previous run (which never rotated) must not
	// be appended into.
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 32)), 0o644); err != nil {
		t.Fatal(err)
	}

	l, err := newFileLog(path, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	if _, err := l.Write([]byte("new\n")); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > 32 {
		t.Fatalf("live file is %d bytes after a capped start, cap 32", st.Size())
	}
	old, err := os.ReadFile(path + ".old")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(old), "xxx") {
		t.Fatalf("backup should hold the previous run's bytes: %q", old)
	}
}

func TestFileLog_WriteAfterCloseFails(t *testing.T) {
	dir := t.TempDir()
	l, err := newFileLog(filepath.Join(dir, "quartermaster.log"), 32)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Write([]byte("late\n")); err == nil {
		t.Fatal("write after close should fail")
	}
}
