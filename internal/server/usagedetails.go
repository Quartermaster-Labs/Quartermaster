package server

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/quartermaster-labs/quartermaster/internal/chain"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Usage-detail enrichment.
//
// llama-server reports KV cache reuse and prefill counts in its own `timings`
// object, which no OpenAI client reads, and reports nothing at all for
// reasoning tokens. Both have a standard home in the OpenAI usage object
// (`prompt_tokens_details.cached_tokens`,
// `completion_tokens_details.reasoning_tokens`), so this middleware fills those
// in on the way out. It only ever *adds* fields the upstream left absent -- an
// upstream that reports them itself is passed through untouched.
//
// The cached count is real (`timings.cache_n`). The reasoning count is not
// available anywhere, so it is derived by splitting the authoritative output
// token count between the reasoning and content text in the same response, in
// proportion to their length. That is an estimate, so it is labelled as one:
// the sibling `reasoning_tokens_estimated: true` is written next to it. Do not
// emit the estimate without the label.
//
// Enrichment degrades to a passthrough rather than risking the response body:
// non-200, compressed, non-JSON/SSE, and unparseable payloads are forwarded
// byte for byte.

// CreateUsageDetailsMiddleware returns middleware that fills the standard
// OpenAI usage-detail fields from llama-server's `timings`.
func CreateUsageDetailsMiddleware() chain.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				next.ServeHTTP(w, r)
				return
			}
			uw := newUsageDetailWriter(w)
			next.ServeHTTP(uw, r)
			uw.finish()
		})
	}
}

type usageMode int

const (
	// usageUndecided is the state before WriteHeader picks a mode.
	usageUndecided usageMode = iota
	usagePassthrough
	usageSSE
	usageBuffered
)

// usageDetailWriter rewrites the usage object of an OpenAI-shaped response.
//
// A streaming response is rewritten frame by frame as it passes (the usage
// object rides in the final chunk, by which point the reasoning/content split
// is fully counted). A single-shot JSON response has no such ordering, so it is
// withheld until the handler returns and written once, with Content-Length
// corrected.
type usageDetailWriter struct {
	http.ResponseWriter
	mode        usageMode
	status      int
	wroteHeader bool
	finished    bool
	buf         bytes.Buffer

	// running rune counts of the reasoning and content text seen so far, used
	// to split the output token count between them.
	reasonChars  int
	contentChars int
}

func newUsageDetailWriter(w http.ResponseWriter) *usageDetailWriter {
	return &usageDetailWriter{ResponseWriter: w, status: http.StatusOK}
}

func (w *usageDetailWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	w.mode = w.pickMode(status)
	if w.mode != usageBuffered {
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *usageDetailWriter) pickMode(status int) usageMode {
	if status != http.StatusOK {
		return usagePassthrough
	}
	h := w.Header()
	// A compressed body would have to be decoded and re-encoded to touch;
	// llama-server does not compress, so degrade instead of paying for it.
	if h.Get("Content-Encoding") != "" {
		return usagePassthrough
	}
	ct := h.Get("Content-Type")
	switch {
	case strings.Contains(ct, "text/event-stream"):
		return usageSSE
	case strings.Contains(ct, "application/json"):
		return usageBuffered
	default:
		return usagePassthrough
	}
}

func (w *usageDetailWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	switch w.mode {
	case usageSSE:
		if _, err := w.ResponseWriter.Write(w.enrichSSE(b)); err != nil {
			return 0, err
		}
		// Report the caller's own byte count: an enriched frame is longer than
		// what the handler handed us, and a short/long write breaks io.Writer.
		return len(b), nil
	case usageBuffered:
		return w.buf.Write(b)
	case usagePassthrough, usageUndecided:
		return w.ResponseWriter.Write(b)
	default:
		return w.ResponseWriter.Write(b)
	}
}

// Flush forwards to the underlying writer, except while a single-shot JSON body
// is being withheld -- there is nothing to flush until finish writes it.
func (w *usageDetailWriter) Flush() {
	if w.mode == usageBuffered {
		return
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying writer (websocket upgrades) and drops this
// response out of enrichment: raw framed data is not ours to rewrite.
func (w *usageDetailWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
	}
	w.mode = usagePassthrough
	return hj.Hijack()
}

// finish releases a withheld single-shot JSON body. Called once by the
// middleware after the handler returns; a no-op in every other mode.
func (w *usageDetailWriter) finish() {
	if w.finished {
		return
	}
	w.finished = true
	if w.mode != usageBuffered {
		return
	}
	body := w.buf.Bytes()
	if out := w.enrichJSONBody(body); out != nil {
		body = out
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.ResponseWriter.WriteHeader(w.status)
	if len(body) > 0 {
		_, _ = w.ResponseWriter.Write(body)
	}
}

// enrichSSE rewrites any frame in b that carries a usage object, and counts the
// reasoning/content text of every frame on the way past. It returns b itself
// when nothing changed, so an untouched stream stays byte-identical.
func (w *usageDetailWriter) enrichSSE(b []byte) []byte {
	if !bytes.Contains(b, []byte("data:")) {
		return b
	}
	lines := bytes.Split(b, []byte("\n"))
	changed := false
	for i, line := range lines {
		prefix, payload, ok := sseData(line)
		if !ok {
			continue
		}
		parsed := gjson.ParseBytes(payload)
		if !parsed.IsObject() {
			continue
		}
		reason, content := messageChars(parsed)
		w.reasonChars += reason
		w.contentChars += content
		if !parsed.Get("usage").Exists() {
			continue
		}
		out := enrichUsagePayload(payload, parsed, w.reasonChars, w.contentChars)
		if out == nil {
			continue
		}
		lines[i] = append(append([]byte{}, prefix...), out...)
		changed = true
	}
	if !changed {
		return b
	}
	return bytes.Join(lines, []byte("\n"))
}

// enrichJSONBody rewrites a single-shot response body, or returns nil when
// there is nothing to add.
func (w *usageDetailWriter) enrichJSONBody(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return nil
	}
	parsed := gjson.ParseBytes(body)
	if !parsed.Get("usage").Exists() {
		return nil
	}
	reason, content := messageChars(parsed)
	return enrichUsagePayload(body, parsed, w.reasonChars+reason, w.contentChars+content)
}

// sseData splits an SSE line into its `data:` prefix and JSON payload. The
// prefix is returned verbatim so a rewritten frame keeps the upstream's exact
// spelling (`data:` and `data: ` are both legal).
func sseData(line []byte) (prefix, payload []byte, ok bool) {
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil, nil, false
	}
	rest := line[len("data:"):]
	trimmed := bytes.TrimLeft(rest, " ")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, nil, false
	}
	return line[:len(line)-len(trimmed)], trimmed, true
}

// messageChars counts the reasoning and content runes across a payload's
// choices, handling both the streaming (`delta`) and single-shot (`message`)
// shapes, and both spellings of the reasoning field.
func messageChars(parsed gjson.Result) (reason, content int) {
	parsed.Get("choices").ForEach(func(_, choice gjson.Result) bool {
		part := choice.Get("delta")
		if !part.Exists() {
			part = choice.Get("message")
		}
		if !part.Exists() {
			return true
		}
		reason += utf8.RuneCountInString(part.Get("reasoning_content").String())
		reason += utf8.RuneCountInString(part.Get("reasoning").String())
		content += utf8.RuneCountInString(part.Get("content").String())
		return true
	})
	return reason, content
}

// enrichUsagePayload adds the missing usage details to one JSON payload and
// returns the rewritten bytes, or nil when there was nothing to add.
func enrichUsagePayload(payload []byte, parsed gjson.Result, reasonChars, contentChars int) []byte {
	usage := parsed.Get("usage")
	if !usage.IsObject() {
		return nil
	}
	timings := parsed.Get("timings")
	out, changed := payload, false

	// Cache reuse: real, straight off llama-server's timings.
	if !usage.Get("prompt_tokens_details.cached_tokens").Exists() {
		if cached := timings.Get("cache_n"); cached.Exists() && cached.Int() >= 0 {
			if v, err := sjson.SetBytes(out, "usage.prompt_tokens_details.cached_tokens", cached.Int()); err == nil {
				out, changed = v, true
			}
		}
	}

	// Reasoning tokens: estimated, and labelled as estimated.
	if !usage.Get("completion_tokens_details.reasoning_tokens").Exists() {
		total := usage.Get("completion_tokens").Int()
		if predicted := timings.Get("predicted_n"); predicted.Exists() {
			total = predicted.Int()
		}
		if est := splitReasoningTokens(total, reasonChars, contentChars); est > 0 {
			v, err := sjson.SetBytes(out, "usage.completion_tokens_details.reasoning_tokens", est)
			if err == nil {
				v, err = sjson.SetBytes(v, "usage.completion_tokens_details.reasoning_tokens_estimated", true)
			}
			if err == nil {
				out, changed = v, true
			}
		}
	}

	if !changed {
		return nil
	}
	return out
}

// splitReasoningTokens apportions total output tokens to the reasoning text by
// its share of the generated runes. Exact in aggregate, approximate in the
// split -- which is why callers label the result as an estimate.
func splitReasoningTokens(total int64, reasonChars, contentChars int) int64 {
	if total <= 0 || reasonChars <= 0 {
		return 0
	}
	est := int64(math.Round(float64(total) * float64(reasonChars) / float64(reasonChars+contentChars)))
	if est > total {
		est = total
	}
	if est < 1 {
		est = 1
	}
	return est
}
