package rule

import (
	"testing"

	"rule-router/internal/logger"
)

// queryContext builds an HTTP evaluation context carrying query parameters.
func queryContext(t *testing.T, query QueryParams) *EvaluationContext {
	t.Helper()
	ctx, err := NewEvaluationContext(
		[]byte(`{"user_id":"body-value"}`),
		map[string]string{"X-Tenant": "header-value"},
		nil, // subjectCtx
		NewHTTPRequestContext("/webhooks/acme", "POST", query),
		nil, // timeCtx
		nil, // kvCtx
		nil, // sigVerification
		logger.NewNop(),
	)
	if err != nil {
		t.Fatalf("NewEvaluationContext() error = %v", err)
	}
	return ctx
}

func TestResolveValue_Query(t *testing.T) {
	query := QueryParams{
		"tenant":  "acme",
		"Tenant":  "AcmeCorp",
		"debug":   "1",
		"empty":   "",
		"user_id": "query-value",
	}
	ctx := queryContext(t, query)

	tests := []struct {
		name      string
		path      string
		want      any
		wantFound bool
	}{
		{"resolves a parameter", "@query.tenant", "acme", true},
		{"names are case-sensitive", "@query.Tenant", "AcmeCorp", true},
		{"wrong case is not found", "@query.TENANT", nil, false},
		{"empty value is found, not absent", "@query.empty", "", true},
		{"absent parameter is not found", "@query.missing", nil, false},
		{"bare @query is not a variable", "@query", nil, false},
		{"no @query.count", "@query.count", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := ctx.ResolveValue(tt.path)
			if found != tt.wantFound {
				t.Fatalf("ResolveValue(%q) found = %v, want %v", tt.path, found, tt.wantFound)
			}
			if found && got != tt.want {
				t.Errorf("ResolveValue(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// TestResolveValue_QueryDoesNotShadowMessage is the security-relevant property:
// query data is attacker-supplied in a way an HMAC-gated body is not, so it
// lives in its own namespace and can never override a body field.
func TestResolveValue_QueryDoesNotShadowMessage(t *testing.T) {
	ctx := queryContext(t, QueryParams{"user_id": "query-value"})

	if got, _ := ctx.ResolveValue("user_id"); got != "body-value" {
		t.Errorf("body field user_id = %v, want body-value (query must not shadow it)", got)
	}
	if got, _ := ctx.ResolveValue("@query.user_id"); got != "query-value" {
		t.Errorf("@query.user_id = %v, want query-value", got)
	}
}

// TestResolveValue_QueryAbsentContexts checks the nil paths: a NATS trigger has
// no HTTP context at all, and an HTTP request may carry no query string.
func TestResolveValue_QueryAbsentContexts(t *testing.T) {
	t.Run("nil query on an HTTP context", func(t *testing.T) {
		ctx := queryContext(t, nil)
		if _, found := ctx.ResolveValue("@query.tenant"); found {
			t.Error("expected @query.tenant to be absent when no query string was sent")
		}
	})

	t.Run("NATS trigger has no HTTP context", func(t *testing.T) {
		ctx, err := NewEvaluationContext(
			[]byte(`{}`),
			nil,
			NewSubjectContext("sensors.temp"),
			nil, // httpCtx
			nil, nil, nil,
			logger.NewNop(),
		)
		if err != nil {
			t.Fatalf("NewEvaluationContext() error = %v", err)
		}
		if _, found := ctx.ResolveValue("@query.tenant"); found {
			t.Error("expected @query.tenant to be absent for a NATS trigger")
		}
	})
}

// TestQueryValuesInConditions confirms query values behave like header values
// in conditions: strings that coerce against YAML numbers and booleans.
func TestQueryValuesInConditions(t *testing.T) {
	ctx := queryContext(t, QueryParams{
		"tenant":  "acme",
		"version": "3",
		"debug":   "true",
	})
	evaluator := NewEvaluator(logger.NewNop())

	tests := []struct {
		name string
		cond Condition
		want bool
	}{
		{"string eq", Condition{Field: "{@query.tenant}", Operator: "eq", Value: "acme"}, true},
		{"string mismatch", Condition{Field: "{@query.tenant}", Operator: "eq", Value: "other"}, false},
		{"gte against a YAML number", Condition{Field: "{@query.version}", Operator: "gte", Value: 2}, true},
		{"eq against a YAML bool", Condition{Field: "{@query.debug}", Operator: "eq", Value: true}, true},
		{"exists on a present param", Condition{Field: "{@query.tenant}", Operator: "exists"}, true},
		{"exists on an absent param", Condition{Field: "{@query.nope}", Operator: "exists"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond := tt.cond
			prepareCondition(&cond)
			if got := evaluator.evaluateCondition(&cond, ctx); got != tt.want {
				t.Errorf("evaluateCondition(%s %s %v) = %v, want %v",
					cond.Field, cond.Operator, cond.Value, got, tt.want)
			}
		})
	}
}

// TestQueryInTemplates checks the template surface, including the empty render
// for an absent parameter that the engine uses for every unresolved variable.
func TestQueryInTemplates(t *testing.T) {
	ctx := queryContext(t, QueryParams{"tenant": "acme"})
	engine := NewTemplateEngine(logger.NewNop())

	tests := []struct {
		template string
		want     string
	}{
		{"{@query.tenant}", "acme"},
		{"tenant/{@query.tenant}/events", "tenant/acme/events"},
		{"{@query.missing}", ""},
	}

	for _, tt := range tests {
		t.Run(tt.template, func(t *testing.T) {
			got, err := engine.Execute(tt.template, ctx)
			if err != nil {
				t.Fatalf("Execute(%q) error = %v", tt.template, err)
			}
			if got != tt.want {
				t.Errorf("Execute(%q) = %q, want %q", tt.template, got, tt.want)
			}
		})
	}
}
