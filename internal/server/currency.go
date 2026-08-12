package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The `convert_currency` tool: one FX rate, fetched, never remembered.
//
// Shopping is the reason this exists. The assistant asks which currency the user
// buys in, then finds the best option priced in another one — and a model
// converting from memory quotes a training-cutoff rate with total confidence,
// which is exactly the kind of wrong that looks right. The rate is read live and
// the tool result states its date, so the answer can be attributed.
//
// Two upstreams, both keyless: Frankfurter (ECB daily reference rates, the more
// authoritative but only ~30 currencies) then open.er-api.com (~160 currencies)
// for the pairs ECB does not publish.

const (
	fxTimeout  = 12 * time.Second
	fxCacheTTL = 6 * time.Hour
	fxCacheMax = 128
	// maxConverts caps calls per turn. A comparison of five products needs at
	// most a handful; a loop re-asking the same pair is a bug, not research.
	maxConverts = 8
)

var fxClient = &http.Client{Timeout: fxTimeout}

type fxQuote struct {
	From, To string
	Rate     float64
	Date     string // as-of date the source reports
	Source   string
}

type fxCacheEntry struct {
	q  *fxQuote
	at time.Time
}

var (
	fxCacheMu sync.Mutex
	fxCache   = map[string]fxCacheEntry{}
)

// normCurrency validates and uppercases an ISO-4217-shaped code. The value comes
// from the model and is interpolated into an upstream URL path/query, so
// anything that is not exactly three letters is refused rather than escaped.
func normCurrency(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) != 3 {
		return ""
	}
	for i := 0; i < 3; i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return ""
		}
	}
	return s
}

// parseConvertArgs reads {amount, from, to} off the model's tool call. Field
// aliases are accepted for the same reason the other tools accept them: a weak
// model half-follows the schema, and refusing on `source`/`target` costs a round
// to gain nothing.
func parseConvertArgs(raw string) (float64, string, string) {
	var a struct {
		Amount any    `json:"amount"`
		Value  any    `json:"value"`
		From   string `json:"from"`
		Source string `json:"source"`
		To     string `json:"to"`
		Target string `json:"target"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return 0, "", ""
	}
	amount := 1.0
	for _, v := range []any{a.Amount, a.Value} {
		switch n := v.(type) {
		case float64:
			amount = n
		case string:
			// "1,299.00" / "€1299" — strip the grouping and any symbol the model
			// copied off the page rather than failing the call over it.
			var b strings.Builder
			for _, r := range n {
				if (r >= '0' && r <= '9') || r == '.' || r == '-' {
					b.WriteRune(r)
				}
			}
			if f := b.String(); f != "" {
				fmt.Sscanf(f, "%g", &amount)
			}
		default:
			continue
		}
		break
	}
	from := normCurrency(a.From)
	if from == "" {
		from = normCurrency(a.Source)
	}
	to := normCurrency(a.To)
	if to == "" {
		to = normCurrency(a.Target)
	}
	return amount, from, to
}

func fetchFxRate(ctx context.Context, from, to string) (*fxQuote, error) {
	if from == to {
		return &fxQuote{From: from, To: to, Rate: 1, Date: "n/a", Source: "same currency"}, nil
	}
	key := from + "/" + to
	fxCacheMu.Lock()
	if e, ok := fxCache[key]; ok && time.Since(e.at) < fxCacheTTL {
		fxCacheMu.Unlock()
		return e.q, nil
	}
	fxCacheMu.Unlock()

	q, err := fxFrankfurter(ctx, from, to)
	if err != nil {
		// ECB does not publish every currency; the second source is what makes
		// the tool usable outside the majors. The first error is kept only if
		// both fail, since it is the more informative one.
		q2, err2 := fxOpenERAPI(ctx, from, to)
		if err2 != nil {
			return nil, fmt.Errorf("%v (and fallback: %v)", err, err2)
		}
		q = q2
	}

	fxCacheMu.Lock()
	if len(fxCache) >= fxCacheMax {
		fxCache = map[string]fxCacheEntry{} // cheap whole-map reset; entries are short-lived
	}
	fxCache[key] = fxCacheEntry{q: q, at: time.Now()}
	fxCacheMu.Unlock()
	return q, nil
}

func fxGetJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", pageUserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := fxClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, req.URL.Host)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

var fxFrankfurterBase = "https://api.frankfurter.app"

func fxFrankfurter(ctx context.Context, from, to string) (*fxQuote, error) {
	var r struct {
		Date  string             `json:"date"`
		Rates map[string]float64 `json:"rates"`
	}
	url := fmt.Sprintf("%s/latest?base=%s&symbols=%s", fxFrankfurterBase, from, to)
	if err := fxGetJSON(ctx, url, &r); err != nil {
		return nil, err
	}
	rate, ok := r.Rates[to]
	if !ok || rate <= 0 {
		return nil, fmt.Errorf("no ECB rate for %s/%s", from, to)
	}
	return &fxQuote{From: from, To: to, Rate: rate, Date: r.Date, Source: "ECB reference rate via frankfurter.app"}, nil
}

var fxOpenERAPIBase = "https://open.er-api.com/v6/latest"

func fxOpenERAPI(ctx context.Context, from, to string) (*fxQuote, error) {
	var r struct {
		Result  string             `json:"result"`
		Updated string             `json:"time_last_update_utc"`
		Rates   map[string]float64 `json:"rates"`
	}
	if err := fxGetJSON(ctx, fmt.Sprintf("%s/%s", fxOpenERAPIBase, from), &r); err != nil {
		return nil, err
	}
	if r.Result != "" && r.Result != "success" {
		return nil, fmt.Errorf("open.er-api.com: %s", r.Result)
	}
	rate, ok := r.Rates[to]
	if !ok || rate <= 0 {
		return nil, fmt.Errorf("no rate for %s/%s", from, to)
	}
	date := r.Updated
	if i := strings.Index(date, " 00:00"); i > 0 {
		date = strings.TrimSpace(date[:i])
	}
	return &fxQuote{From: from, To: to, Rate: rate, Date: date, Source: "open.er-api.com"}, nil
}

// formatFxRate renders the tool message. The as-of date and the rate itself are
// spelled out so the model can attribute the number, and the closing line is
// there because a converted price presented alone reads as the shop's own —
// the user pays the printed one.
func formatFxRate(amount float64, q *fxQuote) string {
	return fmt.Sprintf(
		"%s %s = %s %s\nRate: 1 %s = %s %s (%s, as of %s).\nThis is a reference rate, not a checkout rate — cards and shops add their own margin. Always show the price the page actually states as well as the converted figure.",
		trimNum(amount), q.From, trimNum(amount*q.Rate), q.To,
		q.From, trimNum(q.Rate), q.To, q.Source, q.Date,
	)
}

// trimNum prints money without exponent notation or trailing-zero noise: 2 dp
// for anything price-shaped, more only for a small rate (0.00021 JPY/USD).
func trimNum(f float64) string {
	if f != 0 && (f < 0.01 && f > -0.01) {
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", f), "0"), ".")
	}
	return fmt.Sprintf("%.2f", f)
}

// handleAPIFxRate — GET /api/fx?from=USD&to=DKK. The same rate the tool uses,
// exposed to the browser for the ask-wizard: when the model writes budget
// brackets before it knows which currency the user spends in, the wizard rewrites
// the amounts instead of showing a list denominated in the wrong money.
//
// Same-origin for the same reason as /api/websearch (upstream CORS is not
// something to bet the UI on), and it reuses fetchFxRate's 6h cache, so a repaint
// costs nothing. `from`/`to` go through normCurrency before touching a URL.
func (s *Server) handleAPIFxRate(w http.ResponseWriter, r *http.Request) {
	from := normCurrency(r.URL.Query().Get("from"))
	to := normCurrency(r.URL.Query().Get("to"))
	if from == "" || to == "" {
		http.Error(w, "from and to must be 3-letter currency codes", http.StatusBadRequest)
		return
	}
	q, err := fetchFxRate(r.Context(), from, to)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"from": q.From, "to": q.To, "rate": q.Rate, "date": q.Date, "source": q.Source,
	})
}
