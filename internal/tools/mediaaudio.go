package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Audio from a link, for the music playground's References tab: paste a page,
// get its soundtrack back to separate or transcribe.
//
// Same vetting as the transcript path (ParseMediaTarget), same yt-dlp, but the
// audio stream is downloaded as the site serves it (m4a or webm/opus) and never
// converted: conversion needs ffmpeg, which is not a dependency here, and the
// browser that asked for it decodes both containers itself.
const (
	// mediaAudioTimeout bounds one download. A song is a few MB; this is for a
	// slow site, not for a long video.
	mediaAudioTimeout = 3 * time.Minute
	// MaxMediaAudioSeconds refuses anything longer than a long track: an hour of
	// livestream is not a reference clip, and every later stage scales with it.
	MaxMediaAudioSeconds = 15 * 60
	// maxMediaAudioBytes caps the file yt-dlp may write (and that we read back).
	maxMediaAudioBytes = 64 << 20
)

// MediaAudio is one downloaded soundtrack.
type MediaAudio struct {
	Title    string
	Uploader string
	URL      string // the page a human would open, for the record of where it came from
	Site     string
	Duration int    // seconds, 0 when the site does not say
	Ext      string // container as served: "m4a", "webm", ...
	Data     []byte
}

// FetchMediaAudio downloads the best audio-only stream of one page.
func FetchMediaAudio(ctx context.Context, target string) (MediaAudio, error) {
	t, err := ParseMediaTarget(ctx, target)
	if err != nil {
		return MediaAudio{}, err
	}
	bin, err := DlpPath()
	if err != nil {
		return MediaAudio{}, err
	}
	dir, err := os.MkdirTemp("", "qm-media-audio-")
	if err != nil {
		return MediaAudio{}, err
	}
	defer os.RemoveAll(dir)

	ctx, cancel := context.WithTimeout(ctx, mediaAudioTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin,
		// m4a first: AAC decodes in every browser engine, opus in all but old Safari.
		"-f", "bestaudio[ext=m4a]/bestaudio[ext=webm]/bestaudio",
		"--max-filesize", fmt.Sprint(maxMediaAudioBytes),
		// Unknown duration passes (the "?"); a known long one is skipped, which
		// yt-dlp reports as success with no file, handled below.
		"--match-filter", fmt.Sprintf("duration <=? %d", MaxMediaAudioSeconds),
		"--write-info-json",
		"--no-playlist", "--no-warnings", "--no-progress", "--no-part",
		"-o", filepath.Join(dir, "a.%(ext)s"),
		"--", t.URL,
	)
	hideConsole(cmd)
	out, runErr := cmd.CombinedOutput()

	res := MediaAudio{URL: t.URL, Site: t.Site}
	if b, err := os.ReadFile(filepath.Join(dir, "a.info.json")); err == nil {
		var info struct {
			Title      string  `json:"title"`
			Uploader   string  `json:"uploader"`
			Duration   float64 `json:"duration"`
			WebpageURL string  `json:"webpage_url"`
			Domain     string  `json:"webpage_url_domain"`
		}
		if json.Unmarshal(b, &info) == nil {
			res.Title, res.Uploader, res.Duration = info.Title, info.Uploader, int(info.Duration)
			if t.ID == "" && strings.HasPrefix(info.WebpageURL, "http") {
				res.URL = info.WebpageURL
			}
			if t.ID == "" && info.Domain != "" {
				res.Site = strings.TrimPrefix(info.Domain, "www.")
			}
		}
	}

	path := findMediaAudio(dir)
	if path == "" {
		switch {
		case ctx.Err() != nil:
			return MediaAudio{}, fmt.Errorf("yt-dlp timed out after %s", mediaAudioTimeout)
		case runErr != nil:
			return MediaAudio{}, fmt.Errorf("yt-dlp failed: %s", firstLine(string(out)))
		case res.Duration > MaxMediaAudioSeconds:
			return MediaAudio{}, fmt.Errorf("%s is %d min long; references are capped at %d min", t.Site, res.Duration/60, MaxMediaAudioSeconds/60)
		}
		return MediaAudio{}, fmt.Errorf("yt-dlp downloaded no audio from this %s page (none offered, or larger than %d MB)", t.Site, maxMediaAudioBytes>>20)
	}
	st, err := os.Stat(path)
	if err != nil {
		return MediaAudio{}, err
	}
	if st.Size() > maxMediaAudioBytes {
		return MediaAudio{}, fmt.Errorf("the audio is larger than %d MB", maxMediaAudioBytes>>20)
	}
	res.Data, err = os.ReadFile(path)
	if err != nil {
		return MediaAudio{}, err
	}
	res.Ext = strings.TrimPrefix(filepath.Ext(path), ".")
	return res, nil
}

// findMediaAudio returns the media file yt-dlp wrote into dir, or "".
func findMediaAudio(dir string) string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, "a.") || strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".part") || strings.HasSuffix(n, ".ytdl") {
			continue
		}
		return filepath.Join(dir, n)
	}
	return ""
}

// MediaAudioMime is the Content-Type for a container yt-dlp wrote.
func MediaAudioMime(ext string) string {
	switch strings.ToLower(ext) {
	case "m4a", "mp4":
		return "audio/mp4"
	case "webm":
		return "audio/webm"
	case "opus", "ogg":
		return "audio/ogg"
	case "mp3":
		return "audio/mpeg"
	case "wav":
		return "audio/wav"
	case "flac":
		return "audio/flac"
	}
	return "application/octet-stream"
}
