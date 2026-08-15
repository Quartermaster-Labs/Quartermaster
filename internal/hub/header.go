package hub

// Header prefetch: pull the first few MB of a repo file without downloading it.
//
// A GGUF's metadata KV section and tensor-info table sit at the very front of
// the file, so a single Range request buys the same header the local sizer
// reads off disk — which is what lets the browser answer "how much context does
// this fit?" before committing to 40 GB. Parsing is deliberately NOT done here:
// this package has no dependency on internal/autogen (see the package doc), so
// the caller gets bytes and decides what they mean.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// MaxHeaderBytes bounds how far into a file a header probe may reach. Measured
// headers run 6-8 MB — the cap is there so a malformed length in a hostile
// response can't turn a size probe into an unbounded read into memory.
const MaxHeaderBytes = 64 << 20

// FetchHead returns the first n bytes of one file in a repo.
func (m *Manager) FetchHead(ctx context.Context, src Source, repo, path string, n int64) ([]byte, error) {
	return m.FetchRange(ctx, src, repo, path, 0, n)
}

// FetchRange returns n bytes of one file in a repo starting at off. Short reads
// are not an error: a file smaller than off+n comes back truncated, and the
// caller (which is parsing a prefix on purpose) is the only one that can tell
// whether what arrived was enough.
//
// The offset exists so a caller that guessed too small can fetch the REMAINDER
// rather than re-requesting from zero. A real gguf header runs 6-8 MB (the
// tokenizer vocab dominates it), close enough to any sensible first guess that
// re-fetching on a miss would routinely cost more bytes than the header itself.
func (m *Manager) FetchRange(ctx context.Context, src Source, repo, path string, off, n int64) ([]byte, error) {
	if src == nil {
		return nil, errors.New("no hub source")
	}
	if off < 0 || n <= 0 || off+n > MaxHeaderBytes {
		return nil, fmt.Errorf("header range %d+%d out of range", off, n)
	}
	// Same two validators the download path runs, and for the same reason: both
	// values are about to be interpolated into a URL.
	if err := validRepoID(repo); err != nil {
		return nil, err
	}
	if err := validRepoPath(path); err != nil {
		return nil, err
	}
	url, err := src.FileURL(repo, path)
	if err != nil {
		return nil, err
	}
	if err := src.CheckURL(url); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", hfUserAgent)
	src.Authorize(req)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+n-1))

	// Host-pin every redirect hop, not just the first: a /resolve/ request is
	// answered with a CDN redirect, so checking only the initial URL checks the
	// one URL that was never going to serve the bytes.
	hc := *m.hc
	hc.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return src.CheckURL(r.URL.String())
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusPartialContent:
	case http.StatusOK:
		// The server ignored Range and is sending the whole file; the LimitReader
		// below is what keeps that from being a 40 GB surprise. It is also sending
		// from byte zero, so a caller asking for a later window has to be given
		// that window and not the head of the file under a false offset — the
		// bytes get appended to what it already holds.
		if off > 0 {
			if _, err := io.CopyN(io.Discard, resp.Body, off); err != nil {
				return nil, err
			}
		}
	default:
		return nil, hubHTTPError(resp)
	}
	return io.ReadAll(io.LimitReader(resp.Body, n))
}
