package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// The `calculate` tool: a real expression parser, never an evaluator.
//
// Exists because local models get four-digit arithmetic wrong with total
// confidence, and shopping runs on exactly that arithmetic — price per litre,
// €/GB, three-year cost with the subscription, is the 12-pack actually cheaper.
//
// SECURITY: the expression is model text. This is a hand-written recursive
// descent parser over a fixed grammar (numbers, + - * / ^, parens, a whitelist
// of functions) — there is no eval, no variables, no assignment, and no way to
// reach anything outside this file. Keep it that way: the moment this grows a
// general evaluator it becomes remote code execution on the user's box.

const (
	// maxCalcs caps calls per turn. A comparison of five products with two
	// derived figures each is well inside this; a model looping on the same sum
	// is a bug.
	maxCalcs = 12
	// Recursion cap: bounds a pathological "((((((…" from the model.
	calcMaxDepth = 32
)

// parseCalcArgs pulls the expression off the tool call. `expression` is the
// documented field; the aliases are what models reach for instead.
func parseCalcArgs(raw string) string {
	var a struct {
		Expression string `json:"expression"`
		Expr       string `json:"expr"`
		Input      string `json:"input"`
		Query      string `json:"query"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return ""
	}
	for _, s := range []string{a.Expression, a.Expr, a.Input, a.Query} {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

type calcParser struct {
	s     []rune
	i     int
	depth int
}

// evalExpr parses and evaluates one arithmetic expression.
func evalExpr(expr string) (float64, error) {
	if len(expr) > 400 {
		return 0, errors.New("expression too long")
	}
	s := strings.NewReplacer(
		"×", "*", "·", "*", "÷", "/", "−", "-", "–", "-", ",", ",",
	).Replace(expr)
	// Digit-grouping commas are only stripped when the expression carries no
	// function call: `max(100,250)` and `1,299.50` cannot both be honoured, and
	// silently reading the first as 100250 would be a wrong answer, not an error.
	if !strings.ContainsAny(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		s = strings.ReplaceAll(s, ",", "")
	}
	s = strings.ReplaceAll(s, "_", "")
	p := &calcParser{s: []rune(s)}
	v, err := p.expr()
	if err != nil {
		return 0, err
	}
	p.ws()
	if p.i < len(p.s) {
		return 0, fmt.Errorf("unexpected %q at position %d", string(p.s[p.i]), p.i+1)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, errors.New("result is not a finite number (division by zero?)")
	}
	return v, nil
}

func (p *calcParser) ws() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t' || p.s[p.i] == '\n') {
		p.i++
	}
}

func (p *calcParser) peek() rune {
	p.ws()
	if p.i >= len(p.s) {
		return 0
	}
	return p.s[p.i]
}

func (p *calcParser) expr() (float64, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > calcMaxDepth {
		return 0, errors.New("expression nested too deeply")
	}
	v, err := p.term()
	if err != nil {
		return 0, err
	}
	for {
		switch p.peek() {
		case '+':
			p.i++
			r, err := p.term()
			if err != nil {
				return 0, err
			}
			v += r
		case '-':
			p.i++
			r, err := p.term()
			if err != nil {
				return 0, err
			}
			v -= r
		default:
			return v, nil
		}
	}
}

func (p *calcParser) term() (float64, error) {
	v, err := p.unary()
	if err != nil {
		return 0, err
	}
	for {
		switch p.peek() {
		case '*':
			p.i++
			r, err := p.unary()
			if err != nil {
				return 0, err
			}
			v *= r
		case '/':
			p.i++
			r, err := p.unary()
			if err != nil {
				return 0, err
			}
			if r == 0 {
				return 0, errors.New("division by zero")
			}
			v /= r
		default:
			return v, nil
		}
	}
}

func (p *calcParser) unary() (float64, error) {
	switch p.peek() {
	case '-':
		p.i++
		v, err := p.unary()
		return -v, err
	case '+':
		p.i++
		return p.unary()
	}
	return p.power()
}

func (p *calcParser) power() (float64, error) {
	base, err := p.postfix()
	if err != nil {
		return 0, err
	}
	if p.peek() == '^' {
		p.i++
		// Right-associative, and the exponent may be signed: 2^-3.
		exp, err := p.unary()
		if err != nil {
			return 0, err
		}
		return math.Pow(base, exp), nil
	}
	return base, nil
}

// postfix handles the trailing percent sign: 20% is 0.2. Chosen over modulo
// because this tool's job is prices, where "%" always means percent.
func (p *calcParser) postfix() (float64, error) {
	v, err := p.primary()
	if err != nil {
		return 0, err
	}
	for p.peek() == '%' {
		p.i++
		v /= 100
	}
	return v, nil
}

// calcFuncs is the whole function surface — a closed whitelist, by design.
var calcFuncs = map[string]struct {
	min, max int
	fn       func([]float64) (float64, error)
}{
	"sqrt":  {1, 1, func(a []float64) (float64, error) { return math.Sqrt(a[0]), nil }},
	"abs":   {1, 1, func(a []float64) (float64, error) { return math.Abs(a[0]), nil }},
	"floor": {1, 1, func(a []float64) (float64, error) { return math.Floor(a[0]), nil }},
	"ceil":  {1, 1, func(a []float64) (float64, error) { return math.Ceil(a[0]), nil }},
	"ln":    {1, 1, func(a []float64) (float64, error) { return math.Log(a[0]), nil }},
	"log":   {1, 1, func(a []float64) (float64, error) { return math.Log10(a[0]), nil }},
	"pow":   {2, 2, func(a []float64) (float64, error) { return math.Pow(a[0], a[1]), nil }},
	"round": {1, 2, func(a []float64) (float64, error) {
		p := 0.0
		if len(a) == 2 {
			p = a[1]
		}
		m := math.Pow(10, p)
		return math.Round(a[0]*m) / m, nil
	}},
	"min": {1, 16, func(a []float64) (float64, error) {
		v := a[0]
		for _, x := range a[1:] {
			v = math.Min(v, x)
		}
		return v, nil
	}},
	"max": {1, 16, func(a []float64) (float64, error) {
		v := a[0]
		for _, x := range a[1:] {
			v = math.Max(v, x)
		}
		return v, nil
	}},
	"sum": {1, 32, func(a []float64) (float64, error) {
		v := 0.0
		for _, x := range a {
			v += x
		}
		return v, nil
	}},
	"avg": {1, 32, func(a []float64) (float64, error) {
		v := 0.0
		for _, x := range a {
			v += x
		}
		return v / float64(len(a)), nil
	}},
}

func (p *calcParser) primary() (float64, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > calcMaxDepth {
		return 0, errors.New("expression nested too deeply")
	}
	c := p.peek()
	switch {
	case c == 0:
		return 0, errors.New("expression ends early")
	case c == '(':
		p.i++
		v, err := p.expr()
		if err != nil {
			return 0, err
		}
		if p.peek() != ')' {
			return 0, errors.New("missing closing parenthesis")
		}
		p.i++
		return v, nil
	case c >= '0' && c <= '9', c == '.':
		start := p.i
		for p.i < len(p.s) && ((p.s[p.i] >= '0' && p.s[p.i] <= '9') || p.s[p.i] == '.') {
			p.i++
		}
		// Exponent form (1.5e6) — only when a digit or sign actually follows, so
		// "2e" stays an error rather than silently becoming 2.
		if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
			j := p.i + 1
			if j < len(p.s) && (p.s[j] == '+' || p.s[j] == '-') {
				j++
			}
			if j < len(p.s) && p.s[j] >= '0' && p.s[j] <= '9' {
				for j < len(p.s) && p.s[j] >= '0' && p.s[j] <= '9' {
					j++
				}
				p.i = j
			}
		}
		f, err := strconv.ParseFloat(string(p.s[start:p.i]), 64)
		if err != nil {
			return 0, fmt.Errorf("bad number %q", string(p.s[start:p.i]))
		}
		return f, nil
	case isLetter(c):
		start := p.i
		for p.i < len(p.s) && (isLetter(p.s[p.i]) || (p.s[p.i] >= '0' && p.s[p.i] <= '9')) {
			p.i++
		}
		name := strings.ToLower(string(p.s[start:p.i]))
		switch name {
		case "pi":
			return math.Pi, nil
		case "e":
			return math.E, nil
		}
		f, ok := calcFuncs[name]
		if !ok {
			return 0, fmt.Errorf("unknown name %q - this tool does arithmetic only, with no variables", name)
		}
		if p.peek() != '(' {
			return 0, fmt.Errorf("%s needs parentheses", name)
		}
		p.i++
		var args []float64
		if p.peek() == ')' {
			p.i++
		} else {
			for {
				v, err := p.expr()
				if err != nil {
					return 0, err
				}
				args = append(args, v)
				if p.peek() == ',' {
					p.i++
					continue
				}
				if p.peek() != ')' {
					return 0, fmt.Errorf("missing closing parenthesis after %s(", name)
				}
				p.i++
				break
			}
		}
		if len(args) < f.min || len(args) > f.max {
			return 0, fmt.Errorf("%s takes %d to %d argument(s), got %d", name, f.min, f.max, len(args))
		}
		return f.fn(args)
	}
	return 0, fmt.Errorf("unexpected %q at position %d", string(c), p.i+1)
}

func isLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// formatCalc renders the answer. The expression is echoed back because the
// model's own arithmetic is what this replaces — seeing the input it actually
// sent is how a wrong transcription gets caught.
func formatCalc(expr string, v float64) string {
	return fmt.Sprintf("%s = %s", expr, fmtCalcNum(v))
}

// fmtCalcNum prints a result without float noise (0.30000000000000004) and
// without dropping precision that matters.
func fmtCalcNum(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	s := strconv.FormatFloat(v, 'g', 12, 64)
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return s
}
