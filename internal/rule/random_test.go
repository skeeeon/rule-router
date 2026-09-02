package rule

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"rule-router/internal/logger"
)

// Assertions here are about range and shape, never about exact values. There is
// deliberately no injectable or seedable random source: adding one would put a
// construction path through every call site to serve nothing but the tests.

// drawCount is high enough that a bound which is off by one, or an argument
// silently ignored, shows up reliably.
const drawCount = 300

func TestRandomInt(t *testing.T) {
	t.Run("stays within an inclusive range", func(t *testing.T) {
		for i := 0; i < drawCount; i++ {
			got, err := randomValue("random.int(1,6)")
			if err != nil {
				t.Fatalf("randomValue: %v", err)
			}
			n, err := strconv.Atoi(got)
			if err != nil {
				t.Fatalf("output %q is not an integer", got)
			}
			if n < 1 || n > 6 {
				t.Fatalf("got %d, want 1..6", n)
			}
		}
	})

	// Both ends inclusive is the documented contract, and the easiest thing to
	// get wrong by one.
	t.Run("can reach both bounds", func(t *testing.T) {
		seenMin, seenMax := false, false
		for i := 0; i < drawCount; i++ {
			got, _ := randomValue("random.int(0,1)")
			switch got {
			case "0":
				seenMin = true
			case "1":
				seenMax = true
			default:
				t.Fatalf("got %q, want 0 or 1", got)
			}
		}
		if !seenMin || !seenMax {
			t.Errorf("range 0..1 never produced both bounds (min=%v max=%v)", seenMin, seenMax)
		}
	})

	t.Run("handles negative and single-value ranges", func(t *testing.T) {
		got, err := randomValue("random.int(-5,-5)")
		if err != nil {
			t.Fatalf("randomValue: %v", err)
		}
		if got != "-5" {
			t.Errorf("got %q, want -5", got)
		}
	})

	t.Run("rejects bad arguments", func(t *testing.T) {
		cases := map[string]string{
			"random.int(1)":      "2 arguments",
			"random.int(1,2,3)":  "2 arguments",
			"random.int(a,100)":  "min",
			"random.int(1,b)":    "max",
			"random.int(10,1)":   "less than min",
			"random.int(1.5,10)": "min",
			"random.int()":       "2 arguments",
			"random.int(-9223372036854775808,9223372036854775807)": "too large",
		}
		for call, wantErr := range cases {
			_, err := randomValue(call)
			if err == nil {
				t.Errorf("%s succeeded, want an error", call)
				continue
			}
			if !strings.Contains(err.Error(), wantErr) {
				t.Errorf("%s error = %q, want it to mention %q", call, err, wantErr)
			}
		}
	})
}

func TestRandomFloat(t *testing.T) {
	// This is the property the whole feature exists for: a bare, unquoted
	// substitution into a JSON payload that parses on every fire.
	t.Run("produces a valid unquoted JSON number every time", func(t *testing.T) {
		for i := 0; i < drawCount; i++ {
			got, err := randomValue("random.float(-19.4,-17.2,1)")
			if err != nil {
				t.Fatalf("randomValue: %v", err)
			}

			var probe struct {
				Celsius float64 `json:"celsius"`
			}
			payload := `{"celsius":` + got + `}`
			if err := json.Unmarshal([]byte(payload), &probe); err != nil {
				t.Fatalf("payload %q is not valid JSON: %v", payload, err)
			}
			if probe.Celsius < -19.4 || probe.Celsius > -17.2 {
				t.Fatalf("got %v, want -19.4..-17.2", probe.Celsius)
			}
		}
	})

	t.Run("honours the decimals argument", func(t *testing.T) {
		cases := []struct {
			call          string
			wantDecimals  int
			wantSeparator bool
		}{
			{"random.float(0,1,0)", 0, false},
			{"random.float(0,1,1)", 1, true},
			{"random.float(0,1,4)", 4, true},
		}
		for _, tc := range cases {
			got, err := randomValue(tc.call)
			if err != nil {
				t.Fatalf("%s: %v", tc.call, err)
			}
			dot := strings.IndexByte(got, '.')
			if !tc.wantSeparator {
				if dot >= 0 {
					t.Errorf("%s = %q, want no decimal point", tc.call, got)
				}
				continue
			}
			if dot < 0 {
				t.Errorf("%s = %q, want a decimal point", tc.call, got)
				continue
			}
			if n := len(got) - dot - 1; n != tc.wantDecimals {
				t.Errorf("%s = %q, got %d decimals, want %d", tc.call, got, n, tc.wantDecimals)
			}
		}
	})

	// 'f' formatting is what keeps a tiny value from arriving as 1e-08.
	t.Run("never emits exponent notation", func(t *testing.T) {
		for i := 0; i < drawCount; i++ {
			got, err := randomValue("random.float(0.00000001,0.00000002,10)")
			if err != nil {
				t.Fatalf("randomValue: %v", err)
			}
			if strings.ContainsAny(got, "eE") {
				t.Fatalf("got %q, want fixed notation", got)
			}
		}
	})

	t.Run("rejects bad arguments", func(t *testing.T) {
		cases := map[string]string{
			"random.float(0,1)":      "3 arguments",
			"random.float(0,1,1,1)":  "3 arguments",
			"random.float(a,1,1)":    "min",
			"random.float(0,b,1)":    "max",
			"random.float(0,1,x)":    "decimals",
			"random.float(1,0,1)":    "less than min",
			"random.float(0,1,-1)":   "between 0 and",
			"random.float(0,1,11)":   "between 0 and",
			"random.float(Inf,1,1)":  "finite",
			"random.float(0,NaN,1)":  "finite",
			"random.float(-Inf,1,1)": "finite",
		}
		for call, wantErr := range cases {
			_, err := randomValue(call)
			if err == nil {
				t.Errorf("%s succeeded, want an error", call)
				continue
			}
			if !strings.Contains(err.Error(), wantErr) {
				t.Errorf("%s error = %q, want it to mention %q", call, err, wantErr)
			}
		}
	})
}

func TestRandomChoice(t *testing.T) {
	t.Run("always returns a member of the set", func(t *testing.T) {
		allowed := map[string]bool{"open": true, "closed": true, "ajar": true}
		seen := map[string]bool{}

		for i := 0; i < drawCount; i++ {
			got, err := randomValue("random.choice(open,closed,ajar)")
			if err != nil {
				t.Fatalf("randomValue: %v", err)
			}
			if !allowed[got] {
				t.Fatalf("got %q, which is not in the set", got)
			}
			seen[got] = true
		}

		if len(seen) != len(allowed) {
			t.Errorf("only saw %d of %d values across %d draws", len(seen), len(allowed), drawCount)
		}
	})

	t.Run("a single value is returned verbatim", func(t *testing.T) {
		got, err := randomValue("random.choice(only)")
		if err != nil {
			t.Fatalf("randomValue: %v", err)
		}
		if got != "only" {
			t.Errorf("got %q, want \"only\"", got)
		}
	})

	t.Run("rejects an empty set", func(t *testing.T) {
		if _, err := randomValue("random.choice()"); err == nil {
			t.Error("random.choice() succeeded, want an error")
		}
	})
}

func TestRandomValue_MalformedCallShape(t *testing.T) {
	// Note: the split is on the FIRST '(' and the trailing ')', so a stray paren
	// inside the arguments becomes part of a value rather than an error —
	// random.choice(a)) yields "a)". That is garbage in, garbage out, and adding
	// a rule against it would buy nothing.
	cases := map[string]string{
		"random.int":       "malformed call",
		"random.int(1,2":   "malformed call",
		"random.nope(1,2)": "unknown function",
		"random.":          "malformed call",
	}
	for call, wantErr := range cases {
		_, err := randomValue(call)
		if err == nil {
			t.Errorf("%q succeeded, want an error", call)
			continue
		}
		if !strings.Contains(err.Error(), wantErr) {
			t.Errorf("%q error = %q, want it to mention %q", call, err, wantErr)
		}
	}
}

// --- Template engine integration ---

func templateContext(t *testing.T, payload string) *EvaluationContext {
	t.Helper()
	ctx, err := NewEvaluationContext(
		[]byte(payload),
		nil, // headers
		nil, // subjectCtx
		nil, // httpCtx
		nil, // timeCtx
		nil, // kvCtx
		nil, // sigVerification
		logger.NewNop(),
	)
	if err != nil {
		t.Fatalf("NewEvaluationContext: %v", err)
	}
	return ctx
}

func TestTemplate_RandomRendersIntoJSON(t *testing.T) {
	te := NewTemplateEngine(logger.NewNop())
	ctx := templateContext(t, `{"device":"probe-1"}`)

	tmpl := `{"device":"{device}","celsius":{@random.float(-19.4,-17.2,1)},"count":{@random.int(1,10)},"state":"{@random.choice(open,closed)}"}`

	for i := 0; i < 50; i++ {
		out, err := te.Execute(tmpl, ctx)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}

		var got struct {
			Device  string  `json:"device"`
			Celsius float64 `json:"celsius"`
			Count   int     `json:"count"`
			State   string  `json:"state"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("rendered payload is not valid JSON: %v\n%s", err, out)
		}

		if got.Device != "probe-1" {
			t.Errorf("device = %q, want probe-1", got.Device)
		}
		if got.Celsius < -19.4 || got.Celsius > -17.2 {
			t.Errorf("celsius = %v, out of range", got.Celsius)
		}
		if got.Count < 1 || got.Count > 10 {
			t.Errorf("count = %d, out of range", got.Count)
		}
		if got.State != "open" && got.State != "closed" {
			t.Errorf("state = %q, not in the set", got.State)
		}
	}
}

// Arguments are resolved by the engine's existing nested-template recursion
// before the function ever sees them, so this works without any code of its own.
func TestTemplate_RandomAcceptsNestedTemplateArgument(t *testing.T) {
	te := NewTemplateEngine(logger.NewNop())
	ctx := templateContext(t, `{"max":5}`)

	for i := 0; i < 50; i++ {
		out, err := te.Execute(`{@random.int(5,{max})}`, ctx)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if out != "5" {
			t.Fatalf("got %q, want 5 (min and the resolved max are both 5)", out)
		}
	}
}

// A malformed call cannot normally reach the engine — the loader rejects it —
// but one assembled from a runtime-resolved argument can. It must render empty
// rather than propagate an error that would kill an unrelated message.
func TestTemplate_MalformedRandomRendersEmpty(t *testing.T) {
	te := NewTemplateEngine(logger.NewNop())
	ctx := templateContext(t, `{"max":"abc"}`)

	out, err := te.Execute(`[{@random.int(1,{max})}]`, ctx)
	if err != nil {
		t.Fatalf("Execute returned an error, want a lenient empty render: %v", err)
	}
	if out != "[]" {
		t.Errorf("got %q, want []", out)
	}
}

// TestTemplate_CommaMakesBracesAVariable pins the accepted cost of adding ','
// to isValidVariableChar: a brace-delimited token containing a comma used to
// render literally and now renders as an empty variable lookup. Documented in
// isValidVariableChar; asserted here so the change is never a surprise.
func TestTemplate_CommaMakesBracesAVariable(t *testing.T) {
	te := NewTemplateEngine(logger.NewNop())
	ctx := templateContext(t, `{"a":1}`)

	out, err := te.Execute(`x{foo,bar}y`, ctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "xy" {
		t.Errorf("got %q, want \"xy\" — {foo,bar} is now a variable that misses", out)
	}

	// A token with a space is still not a variable, so genuine prose in braces
	// is unaffected.
	out, err = te.Execute(`x{foo, bar}y`, ctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "x{foo, bar}y" {
		t.Errorf("got %q, want the literal preserved", out)
	}
}

// An unrecognized function name must fall through to ordinary variable
// resolution rather than being swallowed by the function dispatch. This is what
// the (string, bool) return on processSystemFunction buys.
func TestTemplate_UnknownFunctionFallsThroughToResolution(t *testing.T) {
	te := NewTemplateEngine(logger.NewNop())
	ctx := templateContext(t, `{"a":1}`)

	for _, tmpl := range []string{`{@notafunction()}`, `{@notafunction(1,2)}`, `{@kv.bucket.key(weird)}`} {
		out, err := te.Execute(tmpl, ctx)
		if err != nil {
			t.Fatalf("Execute(%q): %v", tmpl, err)
		}
		if out != "" {
			t.Errorf("Execute(%q) = %q, want an empty resolution", tmpl, out)
		}
	}
}

// The three zero-argument functions must be untouched by the dispatch change.
func TestTemplate_ExistingFunctionsUnchanged(t *testing.T) {
	te := NewTemplateEngine(logger.NewNop())
	ctx := templateContext(t, `{"a":1}`)

	for _, tc := range []struct{ tmpl, wantPrefix string }{
		{`{@uuid4()}`, ""},
		{`{@uuid7()}`, ""},
		{`{@timestamp()}`, "20"},
	} {
		out, err := te.Execute(tc.tmpl, ctx)
		if err != nil {
			t.Fatalf("Execute(%q): %v", tc.tmpl, err)
		}
		if out == "" {
			t.Errorf("Execute(%q) rendered empty", tc.tmpl)
		}
		if !strings.HasPrefix(out, tc.wantPrefix) {
			t.Errorf("Execute(%q) = %q, want prefix %q", tc.tmpl, out, tc.wantPrefix)
		}
	}
}
