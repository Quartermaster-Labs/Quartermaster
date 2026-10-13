package server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/shared"
	"github.com/tidwall/gjson"
)

// /v1/systemone — pass-through to the model's own systemone endpoint, validating
// the request against the Typesafe contract (https://docs.typesafe.ai/api.md)
// BEFORE dispatch pays for a model load. The body (state + typed questions) and
// the upstream's answer flow through the model chain untouched. Model
// resolution, auth, scoping, metrics and the scheduler all come from the
// regular modelPostJSONRoutes dispatch (server.go:163) — no special handler,
// no path rewrite: the upstream serves /v1/systemone natively.
//
// Validation mirrors the public contract, not what one local build happens to
// implement: an upstream that lacks a type answers with its own error, which
// passes through. Unknown top-level fields pass so the contract can grow
// without a quartermaster release. Errors use 422 like the contract.

// systemoneAllowedTypes is the whitelist of question `type` values. Widen it
// when the contract grows a new type — never let an unknown type through, a
// silently-misparsed question reads as a confident wrong answer.
var systemoneAllowedTypes = map[string]bool{
	"noul":   true,
	"choice": true,
	"score":  true,
}

const (
	systemoneMaxChoiceOptions = 255
	systemoneMinScoreLevels   = 2
	systemoneMaxScoreLevels   = 10
)

// systemoneGuard validates the /v1/systemone JSON body, then forwards the
// request with the body restored byte for byte.
func (s *Server) systemoneGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			shared.SendResponse(w, r, http.StatusUnsupportedMediaType, "systemone: Content-Type must be application/json")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		if err != nil {
			r.Body = io.NopCloser(bytes.NewReader(nil))
			shared.SendResponse(w, r, http.StatusBadRequest, "systemone: cannot read request body")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if verr := validateSystemone(body); verr != nil {
			shared.SendResponse(w, r, http.StatusUnprocessableEntity, "systemone: "+verr.Error())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// validateSystemone checks one systemone body against the Typesafe contract.
func validateSystemone(body []byte) error {
	if !gjson.ValidBytes(body) {
		return fmt.Errorf("body is not valid JSON")
	}
	parsed := gjson.ParseBytes(body)
	if !parsed.IsObject() {
		return fmt.Errorf("body must be a JSON object")
	}

	if err := validateSystemoneText(parsed.Get("state"), "state"); err != nil {
		return err
	}

	questions := parsed.Get("questions")
	if !questions.Exists() {
		return fmt.Errorf(`"questions" is required`)
	}
	if !questions.IsObject() {
		return fmt.Errorf(`"questions" must be an object keyed by question name`)
	}
	entries := questions.Map()
	if len(entries) == 0 {
		return fmt.Errorf(`"questions" must contain at least one question`)
	}

	for name, q := range entries {
		if err := validateSystemoneQuestion(name, q); err != nil {
			return err
		}
	}
	return nil
}

func validateSystemoneQuestion(name string, q gjson.Result) error {
	if !q.IsObject() {
		return fmt.Errorf("questions.%s must be an object", name)
	}
	typ := q.Get("type")
	if typ.Type != gjson.String || !systemoneAllowedTypes[typ.String()] {
		return fmt.Errorf("questions.%s.type must be one of: noul, choice, score", name)
	}
	if err := validateSystemoneText(q.Get("instructions"), "questions."+name+".instructions"); err != nil {
		return err
	}

	switch typ.String() {
	case "noul":
		return validateSystemoneNoulCriteria(name, q.Get("criteria"))
	case "choice":
		return validateSystemoneChoiceCriteria(name, q.Get("criteria"))
	default: // score
		return validateSystemoneScoreCriteria(name, q.Get("criteria"))
	}
}

// validateSystemoneNoulCriteria: noul's criteria is OPTIONAL; when present it
// is an object whose keys are "true"/"false" and whose values are descriptions.
func validateSystemoneNoulCriteria(name string, criteria gjson.Result) error {
	if !criteria.Exists() {
		return nil
	}
	if !criteria.IsObject() {
		return fmt.Errorf("questions.%s.criteria must be an object", name)
	}
	for k, v := range criteria.Map() {
		if k != "true" && k != "false" {
			return fmt.Errorf("questions.%s.criteria key %q must be \"true\" or \"false\"", name, k)
		}
		if err := validateSystemoneText(v, fmt.Sprintf("questions.%s.criteria.%s", name, k)); err != nil {
			return err
		}
	}
	return nil
}

// validateSystemoneChoiceCriteria: choice's criteria is REQUIRED — a map of
// option -> description (string | object | array | null), max 255 options.
func validateSystemoneChoiceCriteria(name string, criteria gjson.Result) error {
	if !criteria.Exists() {
		return fmt.Errorf("questions.%s.criteria is required for choice", name)
	}
	if !criteria.IsObject() {
		return fmt.Errorf("questions.%s.criteria must be an object of option -> description", name)
	}
	entries := criteria.Map()
	if len(entries) == 0 {
		return fmt.Errorf("questions.%s.criteria must contain at least one option", name)
	}
	if len(entries) > systemoneMaxChoiceOptions {
		return fmt.Errorf("questions.%s.criteria has %d options, max %d", name, len(entries), systemoneMaxChoiceOptions)
	}
	for opt, v := range entries {
		if v.Type == gjson.Null {
			continue // null = no extra detail for this option
		}
		if err := validateSystemoneText(v, fmt.Sprintf("questions.%s.criteria.%s", name, opt)); err != nil {
			return err
		}
	}
	return nil
}

// validateSystemoneScoreCriteria: score's criteria is REQUIRED — an ordered
// array of 2-10 level descriptions (string | object | array).
func validateSystemoneScoreCriteria(name string, criteria gjson.Result) error {
	if !criteria.Exists() {
		return fmt.Errorf("questions.%s.criteria is required for score", name)
	}
	if !criteria.IsArray() {
		return fmt.Errorf("questions.%s.criteria must be an array of level descriptions", name)
	}
	levels := criteria.Array()
	if len(levels) < systemoneMinScoreLevels || len(levels) > systemoneMaxScoreLevels {
		return fmt.Errorf("questions.%s.criteria must have %d-%d levels, got %d",
			name, systemoneMinScoreLevels, systemoneMaxScoreLevels, len(levels))
	}
	for i, v := range levels {
		if err := validateSystemoneText(v, fmt.Sprintf("questions.%s.criteria[%d]", name, i)); err != nil {
			return err
		}
	}
	return nil
}

// validateSystemoneText checks a Typesafe "text" slot: a non-empty string, or a
// non-empty object/array (structured instructions/data).
func validateSystemoneText(v gjson.Result, path string) error {
	switch v.Type {
	case gjson.String:
		if strings.TrimSpace(v.String()) == "" {
			return fmt.Errorf("%s must be a non-empty string", path)
		}
		return nil
	case gjson.JSON:
		if !v.IsObject() && !v.IsArray() {
			return fmt.Errorf("%s must be a string, object, or array", path)
		}
		if len(v.Raw) <= 2 { // "[]" / "{}"
			return fmt.Errorf("%s must not be empty", path)
		}
		return nil
	default: // Null, Number, True, False — and missing
		return fmt.Errorf("%s must be a string, object, or array", path)
	}
}
