package server

import (
	"bytes"
	"time"

	"github.com/mostlygeek/llama-swap/internal/event"
	"github.com/mostlygeek/llama-swap/internal/shared"
	"github.com/tidwall/gjson"
)

// liveEmitInterval throttles how often a LiveTokensEvent is published while a
// stream is in flight. Fast enough to read as "live", slow enough to avoid
// flooding the event bus on high token rates.
const liveEmitInterval = 200 * time.Millisecond

// liveTokenCounter scans streaming SSE chunks as they pass through the response
// copier and emits throttled LiveTokensEvents so the UI can show a running
// tokens/sec readout before the final metrics are parsed. It is best-effort:
// each content-bearing chunk counts as one token (llama-server emits one token
// per chunk), and a streamed usage object, if present, overrides the estimate.
type liveTokenCounter struct {
	model      string
	start      time.Time
	buf        []byte // carry partial trailing line between Write calls
	tokens     int
	firstToken time.Time // zero until the first generated token lands
	lastEmit   time.Time
}

func newLiveTokenCounter(model string, start time.Time) *liveTokenCounter {
	return &liveTokenCounter{model: model, start: start}
}

// noteToken records the first-token timestamp the first time a token is seen,
// so emit can report a measured time-to-first-token.
func (c *liveTokenCounter) noteToken() {
	if c.firstToken.IsZero() {
		c.firstToken = time.Now()
	}
}

// feed consumes a chunk of raw SSE body, updating the token count for any
// complete lines and emitting a throttled progress event.
func (c *liveTokenCounter) feed(b []byte) {
	c.buf = append(c.buf, b...)

	for {
		nl := bytes.IndexByte(c.buf, '\n')
		if nl == -1 {
			break // wait for the rest of this line
		}
		line := bytes.TrimSpace(c.buf[:nl])
		c.buf = c.buf[nl+1:]
		c.consumeLine(line)
	}

	if time.Since(c.lastEmit) >= liveEmitInterval {
		c.emit()
	}
}

func (c *liveTokenCounter) consumeLine(line []byte) {
	const prefix = "data:"
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return
	}
	data := bytes.TrimSpace(line[len(prefix):])
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) || !gjson.ValidBytes(data) {
		return
	}
	parsed := gjson.ParseBytes(data)

	// A streamed usage object is authoritative — prefer it over the estimate.
	for _, p := range usagePaths {
		if u := parsed.Get(p); u.Exists() {
			if _, out, _, ok := extractUsageTokens(u); ok && int(out) > c.tokens {
				c.tokens = int(out)
				c.noteToken()
			}
		}
	}

	// Otherwise count any chunk that carried generated output as one token.
	// Chat deltas: visible content, reasoning, OR streamed tool-call arguments
	// (agentic tool calls emit arguments here, not in `content` — without this
	// the rate decays to zero while the model is writing a tool call).
	if d := parsed.Get("choices.0.delta"); d.Exists() {
		if d.Get("content").String() != "" ||
			d.Get("reasoning_content").String() != "" ||
			d.Get("tool_calls").Exists() {
			c.tokens++
			c.noteToken()
			return
		}
	}
	// Legacy completions API streams generated text at choices.0.text.
	if t := parsed.Get("choices.0.text"); t.Exists() && t.String() != "" {
		c.tokens++
		c.noteToken()
	}
}

func (c *liveTokenCounter) emit() {
	c.lastEmit = time.Now()
	ttft := -1
	if !c.firstToken.IsZero() {
		ttft = int(c.firstToken.Sub(c.start).Milliseconds())
	}
	event.Emit(shared.LiveTokensEvent{
		Model:        c.model,
		OutputTokens: c.tokens,
		ElapsedMs:    int(time.Since(c.start).Milliseconds()),
		FirstTokenMs: ttft,
	})
}
