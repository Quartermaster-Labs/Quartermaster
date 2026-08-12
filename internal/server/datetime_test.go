package server

import (
	"strings"
	"testing"
	"time"
)

func TestFormatDatetime(t *testing.T) {
	old := dtNow
	dtNow = func() time.Time { return time.Date(2026, 8, 7, 15, 30, 0, 0, time.UTC) }
	defer func() { dtNow = old }()

	got, err := formatDatetime("UTC", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Friday, 7 August 2026") {
		t.Errorf("got %q", got)
	}

	// Whole calendar days: the afternoon clock must not shorten the count.
	got, err = formatDatetime("UTC", "2026-08-14")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "7 day(s) from today") {
		t.Errorf("until: got %q", got)
	}

	got, _ = formatDatetime("UTC", "2026-08-01")
	if !strings.Contains(got, "6 day(s) ago") {
		t.Errorf("past date: got %q", got)
	}

	got, _ = formatDatetime("UTC", "2026-08-07")
	if !strings.Contains(got, "is today") {
		t.Errorf("today: got %q", got)
	}
}

func TestFormatDatetimeTimezone(t *testing.T) {
	old := dtNow
	dtNow = func() time.Time { return time.Date(2026, 8, 7, 23, 30, 0, 0, time.UTC) }
	defer func() { dtNow = old }()

	// tzdata is embedded (Windows ships no zoneinfo), so a named zone must
	// resolve — and roll the date over.
	got, err := formatDatetime("Asia/Tokyo", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "8 August 2026") {
		t.Errorf("tokyo: got %q", got)
	}

	// An unknown zone is an error, never a silent fall back to server local.
	if _, err := formatDatetime("Mars/Olympus", ""); err == nil {
		t.Error("unknown timezone accepted")
	}
}

func TestParseDatetimeArgs(t *testing.T) {
	tz, until := parseDatetimeArgs(`{"timezone":"Europe/Bucharest","until":"2026-12-25"}`)
	if tz != "Europe/Bucharest" || until != "2026-12-25" {
		t.Fatalf("got %q %q", tz, until)
	}
	if tz, _ := parseDatetimeArgs(`{"tz":"UTC"}`); tz != "UTC" {
		t.Errorf("alias: got %q", tz)
	}
	if tz, until := parseDatetimeArgs(`{}`); tz != "" || until != "" {
		t.Errorf("empty: got %q %q", tz, until)
	}
}
