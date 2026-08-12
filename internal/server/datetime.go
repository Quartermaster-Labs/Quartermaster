package server

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	// Windows ships no zoneinfo database, so a `timezone` argument would fail on
	// exactly the platform this runs on. Embed the IANA data (~450 KB) instead.
	_ "time/tzdata"
)

// The `get_datetime` tool: what day is it, in a named timezone, and how long
// until some date.
//
// A model has no clock. Today's date only reaches it stamped into search
// results (formatSearchResults), so with no search in the turn it answers date
// questions from its training cutoff — "is the sale still on", "how many days
// until the 14th", "what day of the week is the delivery estimate". Date
// arithmetic is also something small models get wrong routinely (month lengths,
// leap years). Purely local: no network, no cache, no per-turn cap worth
// enforcing beyond a runaway stop.

const maxDatetime = 8

// dtNow is a var so tests can pin the clock.
var dtNow = time.Now

func parseDatetimeArgs(raw string) (string, string) {
	var a struct {
		Timezone string `json:"timezone"`
		TZ       string `json:"tz"`
		Until    string `json:"until"`
		Date     string `json:"date"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return "", ""
	}
	tz := strings.TrimSpace(a.Timezone)
	if tz == "" {
		tz = strings.TrimSpace(a.TZ)
	}
	until := strings.TrimSpace(a.Until)
	if until == "" {
		until = strings.TrimSpace(a.Date)
	}
	return tz, until
}

// dtLocation resolves an IANA name. The value is model text but goes to
// LoadLocation, not to a filesystem path we build — an unknown name is an
// error, never a fallback to local time, because silently answering in the
// wrong timezone is the failure this tool exists to prevent.
func dtLocation(tz string) (*time.Location, error) {
	if tz == "" {
		return time.Local, nil
	}
	if strings.EqualFold(tz, "utc") || strings.EqualFold(tz, "gmt") {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q — use an IANA name like Europe/Bucharest, America/New_York or UTC", tz)
	}
	return loc, nil
}

var dtLayouts = []string{
	"2006-01-02T15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
	"02/01/2006",
	"January 2, 2006",
	"2 January 2006",
	"Jan 2, 2006",
}

// formatDatetime is the whole tool result: current time in the requested zone,
// plus the gap to `until` when one was given.
func formatDatetime(tz, until string) (string, error) {
	loc, err := dtLocation(tz)
	if err != nil {
		return "", err
	}
	now := dtNow().In(loc)
	zone, offset := now.Zone()
	var b strings.Builder
	fmt.Fprintf(&b, "Current date and time: %s (%s, UTC%s).\n", now.Format("Monday, 2 January 2006, 15:04"), zone, offsetString(offset))
	fmt.Fprintf(&b, "ISO: %s  ·  week %s  ·  day %d of the year.", now.Format(time.RFC3339), isoWeek(now), now.YearDay())
	if until == "" {
		return b.String(), nil
	}

	var target time.Time
	ok := false
	for _, l := range dtLayouts {
		if t, err := time.ParseInLocation(l, until, loc); err == nil {
			target = t
			ok = true
			break
		}
	}
	if !ok {
		fmt.Fprintf(&b, "\nCould not read %q as a date — pass it as YYYY-MM-DD.", until)
		return b.String(), nil
	}
	// Whole calendar days, counted from midnight to midnight: "3 days until
	// Friday" must not flip to 2 because it is currently the afternoon.
	d0 := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	d1 := time.Date(target.Year(), target.Month(), target.Day(), 0, 0, 0, 0, loc)
	days := int(math.Round(d1.Sub(d0).Hours() / 24))
	switch {
	case days == 0:
		fmt.Fprintf(&b, "\n%s is today (%s).", target.Format("2 January 2006"), target.Format("Monday"))
	case days > 0:
		fmt.Fprintf(&b, "\n%s (%s) is %d day(s) from today — %s.", target.Format("2 January 2006"), target.Format("Monday"), days, weeksPhrase(days))
	default:
		fmt.Fprintf(&b, "\n%s (%s) was %d day(s) ago.", target.Format("2 January 2006"), target.Format("Monday"), -days)
	}
	return b.String(), nil
}

func weeksPhrase(days int) string {
	switch {
	case days < 7:
		return "this week"
	case days < 60:
		return fmt.Sprintf("about %.1f weeks", float64(days)/7)
	default:
		return fmt.Sprintf("about %.1f months", float64(days)/30.44)
	}
}

func isoWeek(t time.Time) string {
	y, w := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}

func offsetString(sec int) string {
	sign := "+"
	if sec < 0 {
		sign = "-"
		sec = -sec
	}
	return fmt.Sprintf("%s%02d:%02d", sign, sec/3600, (sec%3600)/60)
}
