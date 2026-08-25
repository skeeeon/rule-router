package rule

import (
	"reflect"
	"testing"

	"rule-router/internal/logger"
)

// formContext builds an evaluation context the way the HTTP gateway does, with
// the caller's headers deciding which decoder runs.
func formContext(t *testing.T, payload string, headers map[string]string) (*EvaluationContext, error) {
	t.Helper()
	return NewEvaluationContext(
		[]byte(payload),
		headers,
		nil, // subjectCtx
		NewHTTPRequestContext("/new_user_identified.fcgi", "POST"),
		nil, // timeCtx
		nil, // kvCtx
		nil, // sigVerification
		logger.NewNop(),
	)
}

func formHeaders(contentType string) map[string]string {
	return map[string]string{"Content-Type": contentType}
}

func TestNewEvaluationContext_FormDecoding(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		headers map[string]string
		want    map[string]any
	}{
		{
			name:    "simple pairs stay strings",
			payload: "device_id=1234&user_id=5&event=7",
			headers: formHeaders(contentTypeForm),
			want: map[string]any{
				"device_id": "1234",
				"user_id":   "5",
				"event":     "7",
			},
		},
		{
			name:    "repeated key becomes an array",
			payload: "tag=a&tag=b&tag=c",
			headers: formHeaders(contentTypeForm),
			want: map[string]any{
				"tag": []any{"a", "b", "c"},
			},
		},
		{
			name:    "empty value yields empty string",
			payload: "qrcode_value=&user_id=5",
			headers: formHeaders(contentTypeForm),
			want: map[string]any{
				"qrcode_value": "",
				"user_id":      "5",
			},
		},
		{
			name:    "plus is a space and percent-encoding is decoded",
			payload: "user_name=John+Doe&note=a%26b",
			headers: formHeaders(contentTypeForm),
			want: map[string]any{
				"user_name": "John Doe",
				"note":      "a&b",
			},
		},
		{
			name:    "media type parameters are ignored",
			payload: "user_id=5",
			headers: formHeaders("application/x-www-form-urlencoded; charset=UTF-8"),
			want:    map[string]any{"user_id": "5"},
		},
		{
			name:    "content type match is case-insensitive",
			payload: "user_id=5",
			headers: formHeaders("Application/X-WWW-Form-Urlencoded"),
			want:    map[string]any{"user_id": "5"},
		},
		{
			name:    "header name spelling does not matter",
			payload: "user_id=5",
			headers: map[string]string{"content-type": contentTypeForm},
			want:    map[string]any{"user_id": "5"},
		},
		{
			name:    "leading zeros are preserved, not coerced to numbers",
			payload: "pin_value=007&card_value=9007199254740993",
			headers: formHeaders(contentTypeForm),
			want: map[string]any{
				"pin_value":  "007",
				"card_value": "9007199254740993",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, err := formContext(t, tt.payload, tt.headers)
			if err != nil {
				t.Fatalf("NewEvaluationContext() error = %v", err)
			}
			if !reflect.DeepEqual(ctx.Msg, tt.want) {
				t.Errorf("Msg = %#v, want %#v", ctx.Msg, tt.want)
			}
		})
	}
}

// TestNewEvaluationContext_FormMalformedFailsClosed pins the deliberate
// divergence from the lenient JSON path: url.ParseQuery returns the pairs it
// could read alongside its error, and evaluating against a truncated field set
// can flip a condition rather than raise one.
func TestNewEvaluationContext_FormMalformedFailsClosed(t *testing.T) {
	ctx, err := formContext(t, "user_id=5&bad=%zz", formHeaders(contentTypeForm))
	if err == nil {
		t.Fatalf("expected an error for a malformed form body, got context %#v", ctx.Msg)
	}
	if ctx != nil {
		t.Errorf("expected a nil context alongside the error, got %#v", ctx)
	}
}

// TestNewEvaluationContext_FormRequiresContentType documents that the decoder
// is opt-in by the sender: the same bytes without the header keep the existing
// raw-string behavior.
func TestNewEvaluationContext_FormRequiresContentType(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
	}{
		{name: "no headers at all", headers: nil},
		{name: "different content type", headers: formHeaders("text/plain")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, err := formContext(t, "device_id=1234&user_id=5", tt.headers)
			if err != nil {
				t.Fatalf("NewEvaluationContext() error = %v", err)
			}
			want := map[string]any{"@value": "device_id=1234&user_id=5"}
			if !reflect.DeepEqual(ctx.Msg, want) {
				t.Errorf("Msg = %#v, want %#v", ctx.Msg, want)
			}
		})
	}
}

// TestNewEvaluationContext_FormEmptyBody checks the empty-payload path still
// produces the wrapped-nil message rather than an empty object.
func TestNewEvaluationContext_FormEmptyBody(t *testing.T) {
	ctx, err := formContext(t, "", formHeaders(contentTypeForm))
	if err != nil {
		t.Fatalf("NewEvaluationContext() error = %v", err)
	}
	want := map[string]any{"@value": nil}
	if !reflect.DeepEqual(ctx.Msg, want) {
		t.Errorf("Msg = %#v, want %#v", ctx.Msg, want)
	}
}

// TestNewEvaluationContext_FormRawPayloadPreserved guards the HMAC gate and
// passthrough responses, both of which read the original bytes.
func TestNewEvaluationContext_FormRawPayloadPreserved(t *testing.T) {
	const payload = "device_id=1234&user_id=5"
	ctx, err := formContext(t, payload, formHeaders(contentTypeForm))
	if err != nil {
		t.Fatalf("NewEvaluationContext() error = %v", err)
	}
	if string(ctx.RawPayload) != payload {
		t.Errorf("RawPayload = %q, want %q", ctx.RawPayload, payload)
	}
}

// TestFormValuesCoerceInConditions is the claim the decoder rests on: string
// values from a form compare correctly against numbers and booleans written in
// YAML, so no type inference is needed at decode time.
func TestFormValuesCoerceInConditions(t *testing.T) {
	ctx, err := formContext(t,
		"event=3&confidence=87&face_mask=true&duress=0",
		formHeaders(contentTypeForm))
	if err != nil {
		t.Fatalf("NewEvaluationContext() error = %v", err)
	}

	evaluator := NewEvaluator(logger.NewNop())

	tests := []struct {
		name string
		cond Condition
		want bool
	}{
		{"eq against a YAML number", Condition{Field: "{event}", Operator: "eq", Value: 3}, true},
		{"eq mismatch", Condition{Field: "{event}", Operator: "eq", Value: 4}, false},
		{"gte against a YAML number", Condition{Field: "{confidence}", Operator: "gte", Value: 80}, true},
		{"lt against a YAML number", Condition{Field: "{confidence}", Operator: "lt", Value: 80}, false},
		{"eq against a YAML bool", Condition{Field: "{face_mask}", Operator: "eq", Value: true}, true},
		{"zero value is not absent", Condition{Field: "{duress}", Operator: "eq", Value: 0}, true},
		{"exists is false for an absent field", Condition{Field: "{user_name}", Operator: "exists"}, false},
		{"exists is true for a decoded field", Condition{Field: "{event}", Operator: "exists"}, true},
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
