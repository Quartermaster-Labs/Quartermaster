package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/backends"
)

// The youtube_transcript tool: given a YouTube link, hand the model the video's
// captions as text so it can analyse a video it cannot watch.
//
// Fetching goes through yt-dlp (exec-per-request, same shape as upscale.go)
// rather than a hand-rolled scrape of the watch page's
// ytInitialPlayerResponse.captions block. That scrape is ~50 lines and breaks
// every time YouTube reshuffles the page or serves a bot check; yt-dlp exists
// precisely to absorb that churn. Cost: one more external binary, resolved by
// DlpPath (managed build, then PATH, then the bundle's bin\) with a clear
// ErrDlpMissing error when it's absent.

const (
	// ytTimeout caps one fetch. A caption-only pull is a couple of network round
	// trips; a minute is generous before we call it hung.
	ytTimeout = 60 * time.Second
	// ytParagraphSec is the coarse timestamp granularity. Raw auto-caption VTT
	// carries a "00:14:32.120 --> 00:14:33.880" line per 1-2s cue, which costs
	// more tokens than the words themselves. Merging into ~30s paragraphs with
	// ONE timestamp each keeps what timestamps are actually good for -- the model
	// can say "at 14:32 they claim X" and hand back a &t=872s link -- at a
	// fraction of the tokens.
	ytParagraphSec = 30
	// ytMaxTokens caps what a transcript may spend of the model's context. A
	// 3-hour video is ~40k tokens; past this we truncate and say so loudly rather
	// than silently blowing the window.
	ytMaxTokens = 12000
	ytCacheTTL  = 30 * time.Minute
	ytCacheMax  = 16
)

// VideoID matches YouTube's 11-character video id. Everything handed to
// yt-dlp is rebuilt from a match of this, so a tool argument can never inject
// an argument or a different URL.
var VideoID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// ytLang guards the --sub-langs value the same way (e.g. "en", "pt-BR").
var ytLang = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})?$`)

// ytTagRE strips the inline karaoke markup auto-captions carry inside cue text:
// "<00:00:01.639><c> word</c>".
var ytTagRE = regexp.MustCompile(`<[^>]*>`)

// ytCueTimeRE matches a VTT cue timing line, capturing the start time.
var ytCueTimeRE = regexp.MustCompile(`^(\d{2,}):(\d{2}):(\d{2})[.,](\d{3})\s+-->`)

// ErrDlpMissing is the "no yt-dlp on this box" failure, so HTTP callers can
// map it to a 503 (service capability absent) instead of a 502 (upstream fault).
var ErrDlpMissing = errors.New("yt-dlp is not installed, so YouTube transcripts are unavailable. Install it from https://github.com/yt-dlp/yt-dlp (or re-run the quartermaster installer and tick the yt-dlp helper) and make sure it is on PATH or in the bundle's bin\\yt-dlp folder")

type Transcript struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Uploader string `json:"uploader"`
	Duration int    `json:"duration"` // seconds, 0 if unknown
	Text     string `json:"text"`
}

var ytCache struct {
	mu sync.Mutex
	m  map[string]ytCacheEntry
}

type ytCacheEntry struct {
	tr Transcript
	at time.Time
}

// ParseVideoID pulls the video id out of anything a model is likely to pass:
// a full watch URL, youtu.be short link, /shorts/, /embed/, or a bare id.
func ParseVideoID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if VideoID.MatchString(s) {
		return s
	}
	// Normalise to something url-ish so a bare "youtube.com/watch?v=..." parses.
	work := s
	if !strings.Contains(work, "://") {
		work = "https://" + work
	}
	u, err := url.Parse(work)
	if err != nil {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	switch host {
	case "youtu.be":
		if id := strings.Trim(u.Path, "/"); VideoID.MatchString(id) {
			return id
		}
	case "youtube.com", "m.youtube.com", "music.youtube.com", "youtube-nocookie.com":
		if v := u.Query().Get("v"); VideoID.MatchString(v) {
			return v
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		for i, p := range parts {
			if (p == "shorts" || p == "embed" || p == "v" || p == "live") && i+1 < len(parts) {
				if VideoID.MatchString(parts[i+1]) {
					return parts[i+1]
				}
			}
		}
	}
	return ""
}

// DlpPath finds the yt-dlp binary: a managed install first, then PATH, then
// beside our own exe (the packaged-install case, where backends ship in the same
// directory).
func DlpPath() (string, error) {
	// A build the user installed from the Backends tab wins: it is the copy this
	// app can keep updated, and it is only there because they asked for it. The
	// manager is a plain directory scan, so building one here is cheap.
	if got := backends.NewManager("", nil).Installed("yt-dlp"); len(got) > 0 {
		return got[0].Exe, nil
	}
	// LookPath already applies PATHEXT on Windows; the explicit .exe only matters
	// for the exe-dir fallback below.
	names := []string{"yt-dlp", "yt-dlp.exe"}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p, nil
		}
	}
	// Bundle layout: the Windows installer's optional yt-dlp task drops it in
	// bin\yt-dlp (fetch-backend.ps1), same as the inference backends.
	if self, err := os.Executable(); err == nil {
		dir := filepath.Dir(self)
		for _, sub := range []string{"", filepath.Join("bin", "yt-dlp")} {
			for _, n := range names {
				p := filepath.Join(dir, sub, n)
				if st, err := os.Stat(p); err == nil && !st.IsDir() {
					return p, nil
				}
			}
		}
	}
	return "", ErrDlpMissing
}

// GetTranscript downloads captions for one video and returns them as
// timestamped paragraphs. lang is a BCP-47-ish prefix ("en", "pt-BR"); empty
// means English.
func GetTranscript(ctx context.Context, videoID, lang string) (Transcript, error) {
	if !VideoID.MatchString(videoID) {
		return Transcript{}, fmt.Errorf("not a valid YouTube video id")
	}
	if lang == "" {
		lang = "en"
	}
	if !ytLang.MatchString(lang) {
		return Transcript{}, fmt.Errorf("invalid language code %q", lang)
	}
	if tr, ok := ytCacheGet(videoID + "\x00" + lang); ok {
		return tr, nil
	}

	bin, err := DlpPath()
	if err != nil {
		return Transcript{}, err
	}
	dir, err := os.MkdirTemp("", "qm-yt-")
	if err != nil {
		return Transcript{}, err
	}
	defer os.RemoveAll(dir)

	ctx, cancel := context.WithTimeout(ctx, ytTimeout)
	defer cancel()

	// Manual subs first, auto-generated as the fallback (yt-dlp writes whichever
	// exists to the same t.<lang>.vtt and won't clobber a manual track with an
	// auto one). No --convert-subs: YouTube serves vtt natively, and converting
	// would drag in an ffmpeg dependency for nothing.
	out := filepath.Join(dir, "t")
	cmd := exec.CommandContext(ctx, bin,
		"--skip-download",
		"--write-subs", "--write-auto-subs",
		// Exactly two tracks, NOT "en.*": that glob also matches YouTube's
		// machine-translated tracks (en-de-DE, en-ja, en-pt-BR, en-es-419, ...),
		// which meant ~7 downloads per call and an HTTP 429 mid-run. "<lang>" is
		// the manual/CC track when one exists (much cleaner than ASR) and
		// "<lang>-orig" is the auto-caption fallback; findVTT prefers the former.
		"--sub-langs", lang+","+lang+"-orig",
		"--sub-format", "vtt",
		"--write-info-json",
		"--no-playlist", "--no-warnings", "--no-progress",
		"-o", out,
		"--", "https://www.youtube.com/watch?v="+videoID,
	)
	hideConsole(cmd)
	stderr, runErr := cmd.CombinedOutput()

	tr := Transcript{ID: videoID}
	if b, err := os.ReadFile(out + ".info.json"); err == nil {
		var info struct {
			Title    string  `json:"title"`
			Uploader string  `json:"uploader"`
			Duration float64 `json:"duration"`
		}
		if json.Unmarshal(b, &info) == nil {
			tr.Title, tr.Uploader, tr.Duration = info.Title, info.Uploader, int(info.Duration)
		}
	}

	vtt, err := findVTT(dir, lang)
	if err != nil {
		if ctx.Err() != nil {
			return Transcript{}, fmt.Errorf("yt-dlp timed out after %s", ytTimeout)
		}
		// No caption file: either the video has none in this language, or yt-dlp
		// itself failed. Surface the real reason -- an empty transcript the model
		// narrates over is the worst outcome.
		if runErr != nil {
			return Transcript{}, fmt.Errorf("yt-dlp failed: %s", firstLine(string(stderr)))
		}
		return Transcript{}, fmt.Errorf("no %s transcript available for this video (captions are disabled and no auto-caption exists)", lang)
	}

	raw, err := os.ReadFile(vtt)
	if err != nil {
		return Transcript{}, err
	}
	tr.Text = vttToParagraphs(string(raw))
	if strings.TrimSpace(tr.Text) == "" {
		return Transcript{}, fmt.Errorf("transcript for this video is empty")
	}
	ytCachePut(videoID+"\x00"+lang, tr)
	return tr, nil
}

// findVTT picks the caption file yt-dlp wrote, preferring an exact language
// match over a regional variant (t.en.vtt over t.en-GB.vtt).
func findVTT(dir, lang string) (string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var best string
	for _, e := range ents {
		n := e.Name()
		if !strings.HasSuffix(n, ".vtt") {
			continue
		}
		if strings.HasSuffix(n, "."+lang+".vtt") {
			return filepath.Join(dir, n), nil
		}
		if best == "" {
			best = filepath.Join(dir, n)
		}
	}
	if best == "" {
		return "", fmt.Errorf("no vtt written")
	}
	return best, nil
}

// vttToParagraphs turns raw WebVTT into "[m:ss] text" paragraphs of about
// ytParagraphSec each.
//
// Two things make raw auto-caption VTT 2-3x the tokens of the actual words:
// per-cue timing lines, and the rolling window (each cue repeats the previous
// cue's last line so captions scroll). Both are removed here -- a line is
// emitted only if it differs from the last line emitted.
func vttToParagraphs(raw string) string {
	var (
		paras     []string
		cur       []string
		curStart  = -1
		lastLine  string
		cueStart  = -1
		inCue     bool
		flushPara = func() {
			if len(cur) == 0 {
				return
			}
			paras = append(paras, fmt.Sprintf("[%s] %s", ytClock(curStart), strings.Join(cur, " ")))
			cur, curStart = nil, -1
		}
	)

	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if m := ytCueTimeRE.FindStringSubmatch(line); m != nil {
			h, _ := strconv.Atoi(m[1])
			mi, _ := strconv.Atoi(m[2])
			s, _ := strconv.Atoi(m[3])
			cueStart = h*3600 + mi*60 + s
			inCue = true
			continue
		}
		t := strings.TrimSpace(line)
		if t == "" {
			inCue = false
			continue
		}
		if !inCue {
			continue // WEBVTT header, NOTE/STYLE blocks, cue identifiers
		}
		// Collapse whitespace after stripping markup: "to the<c> show</c>" leaves a
		// double space, which would also defeat the rolling-repeat dedupe below
		// (the next cue repeats the same words with single spacing).
		t = strings.Join(strings.Fields(html.UnescapeString(ytTagRE.ReplaceAllString(t, ""))), " ")
		if t == "" || t == lastLine {
			continue // blank after tag-stripping, or the rolling repeat
		}
		lastLine = t
		if curStart < 0 {
			curStart = cueStart
		} else if cueStart-curStart >= ytParagraphSec {
			flushPara()
			curStart = cueStart
		}
		cur = append(cur, t)
	}
	flushPara()
	return strings.Join(paras, "\n\n")
}

// ytClock renders seconds as m:ss, or h:mm:ss past an hour -- the same shape
// YouTube itself shows, so a model quoting a timestamp matches the player.
func ytClock(sec int) string {
	if sec < 0 {
		sec = 0
	}
	h, m, s := sec/3600, (sec%3600)/60, sec%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// FormatTranscript renders the tool result the model sees: a header it
// can cite from, then the paragraphs, truncated at ytMaxTokens with an explicit
// marker. Truncation is announced in the header AND at the cut so the model
// can't summarise the first third and present it as the whole video.
func FormatTranscript(tr Transcript, citation, maxTokens int) string {
	if maxTokens <= 0 || maxTokens > ytMaxTokens {
		maxTokens = ytMaxTokens
	}
	body, cutAt, truncated := ytTruncate(tr.Text, maxTokens)

	var head strings.Builder
	if citation > 0 {
		head.WriteString(fmt.Sprintf("[%d] ", citation))
	}
	head.WriteString("YouTube transcript")
	if tr.Title != "" {
		head.WriteString(fmt.Sprintf(" - %q", tr.Title))
	}
	var meta []string
	if tr.Uploader != "" {
		meta = append(meta, tr.Uploader)
	}
	if tr.Duration > 0 {
		meta = append(meta, ytClock(tr.Duration))
	}
	if len(meta) > 0 {
		head.WriteString(" (" + strings.Join(meta, ", ") + ")")
	}
	head.WriteString("\nhttps://www.youtube.com/watch?v=" + tr.ID + "\n")
	head.WriteString("Timestamps are [m:ss] into the video; link a moment as ?v=" + tr.ID + "&t=<seconds>s.\n")
	if truncated {
		head.WriteString(fmt.Sprintf("INCOMPLETE: only the first %s of this video fits in context. Everything after %s is missing - say so instead of presenting this as the whole video.\n", cutAt, cutAt))
	}
	head.WriteString("\n")

	if truncated {
		body += fmt.Sprintf("\n\n[transcript truncated at %s - the rest of the video is not included]", cutAt)
	}
	return head.String() + body
}

// ytTruncate keeps whole paragraphs up to a rough token budget (~4 chars per
// token) and reports the timestamp it stopped at.
func ytTruncate(text string, maxTokens int) (out string, cutAt string, truncated bool) {
	budget := maxTokens * 4
	if len(text) <= budget {
		return text, "", false
	}
	paras := strings.Split(text, "\n\n")
	n, kept := 0, 0
	for _, p := range paras {
		if n+len(p)+2 > budget {
			break
		}
		n += len(p) + 2
		kept++
	}
	if kept == 0 {
		kept = 1
	}
	cutAt = "the end of the included text"
	if kept < len(paras) {
		if i := strings.Index(paras[kept], "]"); i > 1 && strings.HasPrefix(paras[kept], "[") {
			cutAt = paras[kept][1:i]
		}
	}
	return strings.Join(paras[:kept], "\n\n"), cutAt, true
}

func ytCacheGet(key string) (Transcript, bool) {
	ytCache.mu.Lock()
	defer ytCache.mu.Unlock()
	e, ok := ytCache.m[key]
	if !ok || time.Since(e.at) > ytCacheTTL {
		return Transcript{}, false
	}
	return e.tr, true
}

func ytCachePut(key string, tr Transcript) {
	ytCache.mu.Lock()
	defer ytCache.mu.Unlock()
	if ytCache.m == nil {
		ytCache.m = map[string]ytCacheEntry{}
	}
	if len(ytCache.m) >= ytCacheMax {
		var oldest string
		var oldestAt time.Time
		for k, v := range ytCache.m {
			if oldest == "" || v.at.Before(oldestAt) {
				oldest, oldestAt = k, v.at
			}
		}
		delete(ytCache.m, oldest)
	}
	ytCache.m[key] = ytCacheEntry{tr: tr, at: time.Now()}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "no output"
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
