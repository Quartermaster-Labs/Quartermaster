package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const systemoneValidBody = `{"state":"fix three bugs","model":"kev-0.8b-q8_0",` +
	`"questions":{"department":{"type":"choice","instructions":"Which thinking effort?",` +
	`"criteria":{"off":"Trivial","low":"Routine","medium":"Substantial","high":"Hard"}}}}`

// runSystemoneGuard runs the guard against body and reports the status, whether
// the downstream handler ran, and the body bytes the downstream read.
func runSystemoneGuard(t *testing.T, contentType, body string) (status int, nextRan bool, forwarded string) {
	t.Helper()
	guard := (&Server{}).systemoneGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextRan = true
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("downstream body read: %v", err)
		}
		forwarded = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	guard.ServeHTTP(w, req)
	return w.Code, nextRan, forwarded
}

const (
	choiceQ = `{"type":"choice","instructions":"pick","criteria":{"a":"A","b":"B"}}`
	noulQ   = `{"type":"noul","instructions":"urgent?"}`
	scoreQ  = `{"type":"score","instructions":"rate","criteria":["Calm","Angry"]}`
)

func TestSystemoneGuard(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		body        string
		wantStatus  int
		wantNext    bool
	}{
		// valid — choice (baseline contract shape)
		{"valid choice", "application/json", systemoneValidBody, http.StatusOK, true},
		{"valid with unknown top-level field", "application/json",
			`{"state":"x","model":"m","questions":{"q":` + choiceQ + `},"future_field":1}`,
			http.StatusOK, true},

		// valid — state variants (string | object | array)
		{"state as object", "application/json",
			`{"state":{"tickets":[{"id":1,"text":"payout failing"}]},"model":"m","questions":{"q":` + choiceQ + `}}`,
			http.StatusOK, true},
		{"state as array", "application/json",
			`{"state":["payout failing","3 days"],"model":"m","questions":{"q":` + choiceQ + `}}`,
			http.StatusOK, true},

		// valid — noul
		{"valid noul without criteria", "application/json",
			`{"state":"x","model":"m","questions":{"q":` + noulQ + `}}`, http.StatusOK, true},
		{"valid noul with criteria", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"noul","instructions":"urgent?",` +
				`"criteria":{"true":"time-sensitive","false":"no urgency"}}}}`, http.StatusOK, true},
		{"noul criteria partial keys", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"noul","instructions":"i",` +
				`"criteria":{"true":"yes means x"}}}}`, http.StatusOK, true},

		// valid — score
		{"valid score", "application/json",
			`{"state":"x","model":"m","questions":{"q":` + scoreQ + `}}`, http.StatusOK, true},
		{"score 10 levels", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"score","instructions":"i",` +
				`"criteria":["1","2","3","4","5","6","7","8","9","10"]}}}`, http.StatusOK, true},

		// valid — structured instructions and criteria (string | object | array)
		{"instructions as object", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"noul",` +
				`"instructions":{"data":{"name":"John"},"question":"same as ` + "`data`" + `?"}}}}`,
			http.StatusOK, true},
		{"instructions as array", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"noul","instructions":["a","b"]}}}`,
			http.StatusOK, true},
		{"choice criteria value as object", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"choice","instructions":"i",` +
				`"criteria":{"billing":{"team":"pay","scope":["refunds"]}}}}}`, http.StatusOK, true},
		{"choice criteria value as array", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"choice","instructions":"i",` +
				`"criteria":{"a":["x","y"]}}}}`, http.StatusOK, true},
		{"choice criteria value null", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"choice","instructions":"i",` +
				`"criteria":{"a":null,"b":"B"}}}}`, http.StatusOK, true},
		{"score level as object", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"score","instructions":"i",` +
				`"criteria":[{"label":"calm"},[1,2]]}}}`, http.StatusOK, true},

		// content-type / malformed body
		{"no content type", "", systemoneValidBody, http.StatusUnsupportedMediaType, false},
		{"form content type", "multipart/form-data; boundary=x", systemoneValidBody, http.StatusUnsupportedMediaType, false},
		{"invalid json", "application/json", `{"state":`, http.StatusUnprocessableEntity, false},
		{"body not an object", "application/json", `["state"]`, http.StatusUnprocessableEntity, false},

		// state
		{"missing state", "application/json",
			`{"model":"m","questions":{"q":` + choiceQ + `}}`, http.StatusUnprocessableEntity, false},
		{"empty state string", "application/json",
			`{"state":"  ","model":"m","questions":{"q":` + choiceQ + `}}`, http.StatusUnprocessableEntity, false},
		{"state as number", "application/json",
			`{"state":42,"model":"m","questions":{"q":` + choiceQ + `}}`, http.StatusUnprocessableEntity, false},
		{"state as null", "application/json",
			`{"state":null,"model":"m","questions":{"q":` + choiceQ + `}}`, http.StatusUnprocessableEntity, false},
		{"state empty object", "application/json",
			`{"state":{},"model":"m","questions":{"q":` + choiceQ + `}}`, http.StatusUnprocessableEntity, false},
		{"state empty array", "application/json",
			`{"state":[],"model":"m","questions":{"q":` + choiceQ + `}}`, http.StatusUnprocessableEntity, false},

		// questions
		{"missing questions", "application/json", `{"state":"x","model":"m"}`, http.StatusUnprocessableEntity, false},
		{"questions not an object", "application/json",
			`{"state":"x","model":"m","questions":[{"type":"choice"}]}`, http.StatusUnprocessableEntity, false},
		{"questions empty", "application/json",
			`{"state":"x","model":"m","questions":{}}`, http.StatusUnprocessableEntity, false},

		// question shape
		{"question not an object", "application/json",
			`{"state":"x","model":"m","questions":{"q":"choice"}}`, http.StatusUnprocessableEntity, false},
		{"question missing type", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"instructions":"i","criteria":{"a":"b"}}}}`,
			http.StatusUnprocessableEntity, false},
		{"question unknown type", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"ranked","instructions":"i","criteria":{"a":"b"}}}}`,
			http.StatusUnprocessableEntity, false},
		{"question missing instructions", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"choice","criteria":{"a":"b"}}}}`,
			http.StatusUnprocessableEntity, false},
		{"question empty instructions", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"choice","instructions":" ","criteria":{"a":"b"}}}}`,
			http.StatusUnprocessableEntity, false},
		{"question instructions as number", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"choice","instructions":5,"criteria":{"a":"b"}}}}`,
			http.StatusUnprocessableEntity, false},
		{"question instructions empty object", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"choice","instructions":{},"criteria":{"a":"b"}}}}`,
			http.StatusUnprocessableEntity, false},

		// choice criteria
		{"choice missing criteria", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"choice","instructions":"i"}}}`,
			http.StatusUnprocessableEntity, false},
		{"choice empty criteria", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"choice","instructions":"i","criteria":{}}}}`,
			http.StatusUnprocessableEntity, false},
		{"choice criteria as array", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"choice","instructions":"i","criteria":["a"]}}}`,
			http.StatusUnprocessableEntity, false},
		{"choice criteria value as number", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"choice","instructions":"i","criteria":{"a":1}}}}`,
			http.StatusUnprocessableEntity, false},
		{"choice criteria value empty string", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"choice","instructions":"i","criteria":{"a":" "}}}}`,
			http.StatusUnprocessableEntity, false},

		// noul criteria
		{"noul criteria not an object", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"noul","instructions":"i","criteria":["a"]}}}`,
			http.StatusUnprocessableEntity, false},
		{"noul criteria bad key", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"noul","instructions":"i","criteria":{"maybe":"x"}}}}`,
			http.StatusUnprocessableEntity, false},

		// score criteria
		{"score missing criteria", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"score","instructions":"i"}}}`,
			http.StatusUnprocessableEntity, false},
		{"score criteria not an array", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"score","instructions":"i","criteria":{"a":"b"}}}}`,
			http.StatusUnprocessableEntity, false},
		{"score one level", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"score","instructions":"i","criteria":["only"]}}}`,
			http.StatusUnprocessableEntity, false},
		{"score eleven levels", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"score","instructions":"i",` +
				`"criteria":["1","2","3","4","5","6","7","8","9","10","11"]}}}`,
			http.StatusUnprocessableEntity, false},
		{"score level empty string", "application/json",
			`{"state":"x","model":"m","questions":{"q":{"type":"score","instructions":"i","criteria":["a"," "]}}}`,
			http.StatusUnprocessableEntity, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, nextRan, _ := runSystemoneGuard(t, c.contentType, c.body)
			if status != c.wantStatus {
				t.Errorf("status = %d, want %d", status, c.wantStatus)
			}
			if nextRan != c.wantNext {
				t.Errorf("next ran = %v, want %v", nextRan, c.wantNext)
			}
		})
	}
}

// The guard must forward the body byte for byte — every downstream consumer
// (dispatch, metrics capture, upstream) sees exactly what the client sent.
func TestSystemoneGuard_ForwardsBodyUnchanged(t *testing.T) {
	_, nextRan, forwarded := runSystemoneGuard(t, "application/json", systemoneValidBody)
	if !nextRan {
		t.Fatal("downstream handler did not run")
	}
	if forwarded != systemoneValidBody {
		t.Errorf("forwarded body = %q, want %q", forwarded, systemoneValidBody)
	}
}

// End to end through the real route table: model chain + guard + dispatch.
func TestServer_SystemoneRoute(t *testing.T) {
	s := newTestServer(newStubRouter([]string{"kev-0.8b-q8_0"}, "upstream-ok"), nil)

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		return w
	}

	t.Run("valid body reaches the router", func(t *testing.T) {
		w := post(systemoneValidBody)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
		}
		if got := w.Body.String(); got != "upstream-ok" {
			t.Errorf("body = %q, want %q", got, "upstream-ok")
		}
	})

	t.Run("valid noul and score reach the router", func(t *testing.T) {
		for _, body := range []string{
			`{"state":"x","model":"kev-0.8b-q8_0","questions":{"urgent":{"type":"noul","instructions":"urgent?"}}}`,
			`{"state":"x","model":"kev-0.8b-q8_0","questions":{"level":{"type":"score","instructions":"rate","criteria":["a","b"]}}}`,
		} {
			if w := post(body); w.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
			}
		}
	})

	t.Run("malformed body rejected before dispatch", func(t *testing.T) {
		w := post(`{"state":"x","model":"kev-0.8b-q8_0","questions":{}}`)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", w.Code)
		}
		if strings.Contains(w.Body.String(), "upstream-ok") {
			t.Error("request reached the router despite failing validation")
		}
	})

	t.Run("missing model rejected by context middleware", func(t *testing.T) {
		w := post(`{"state":"x","questions":{"q":{"type":"choice","instructions":"i","criteria":{"a":"b"}}}}`)
		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", w.Code)
		}
	})
}
