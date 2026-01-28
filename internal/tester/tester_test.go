// file: internal/tester/tester_test.go

package tester

import (
	"testing"

	"rule-router/internal/rule"
)

func TestExtractKVBucket(t *testing.T) {
	tests := []struct {
		name  string
		field string
		want  string
	}{
		{
			name:  "simple bucket reference",
			field: "@kv.config.key",
			want:  "config",
		},
		{
			name:  "bucket with key path",
			field: "@kv.users.user123:name",
			want:  "users",
		},
		{
			name:  "bucket in template braces",
			field: "{@kv.settings.{id}:value}",
			want:  "settings",
		},
		{
			name:  "no kv reference",
			field: "temperature",
			want:  "",
		},
		{
			name:  "kv in middle of field",
			field: "prefix.@kv.bucket.key.suffix",
			want:  "bucket",
		},
		{
			name:  "empty field",
			field: "",
			want:  "",
		},
		{
			name:  "just @kv prefix",
			field: "@kv.",
			want:  "",
		},
		{
			name:  "bucket with underscore",
			field: "@kv.user_settings.key",
			want:  "user_settings",
		},
		{
			name:  "bucket with hyphen",
			field: "@kv.my-bucket.key",
			want:  "my-bucket",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractKVBucket(tt.field)
			if got != tt.want {
				t.Errorf("extractKVBucket(%q) = %q, want %q", tt.field, got, tt.want)
			}
		})
	}
}

func TestAppendUnique(t *testing.T) {
	tests := []struct {
		name  string
		slice []string
		item  string
		want  []string
	}{
		{
			name:  "add to empty slice",
			slice: []string{},
			item:  "a",
			want:  []string{"a"},
		},
		{
			name:  "add new item",
			slice: []string{"a", "b"},
			item:  "c",
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "add duplicate item",
			slice: []string{"a", "b", "c"},
			item:  "b",
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "add first item again",
			slice: []string{"a", "b"},
			item:  "a",
			want:  []string{"a", "b"},
		},
		{
			name:  "add last item again",
			slice: []string{"a", "b"},
			item:  "b",
			want:  []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := appendUnique(tt.slice, tt.item)
			if len(got) != len(tt.want) {
				t.Errorf("appendUnique() length = %d, want %d", len(got), len(tt.want))
				return
			}
			for i, v := range got {
				if v != tt.want[i] {
					t.Errorf("appendUnique()[%d] = %q, want %q", i, v, tt.want[i])
				}
			}
		})
	}
}

func TestDetectForEach(t *testing.T) {
	tests := []struct {
		name      string
		rule      rule.Rule
		wantFound bool
		wantField string
	}{
		{
			name: "NATS forEach",
			rule: rule.Rule{
				Action: rule.Action{
					NATS: &rule.NATSAction{
						ForEach: "{items}",
					},
				},
			},
			wantFound: true,
			wantField: "{items}",
		},
		{
			name: "HTTP forEach",
			rule: rule.Rule{
				Action: rule.Action{
					HTTP: &rule.HTTPAction{
						ForEach: "{readings}",
					},
				},
			},
			wantFound: true,
			wantField: "{readings}",
		},
		{
			name: "no forEach - NATS action",
			rule: rule.Rule{
				Action: rule.Action{
					NATS: &rule.NATSAction{
						Subject: "output.subject",
					},
				},
			},
			wantFound: false,
			wantField: "",
		},
		{
			name: "no forEach - HTTP action",
			rule: rule.Rule{
				Action: rule.Action{
					HTTP: &rule.HTTPAction{
						URL: "http://example.com",
					},
				},
			},
			wantFound: false,
			wantField: "",
		},
		{
			name:      "no action at all",
			rule:      rule.Rule{},
			wantFound: false,
			wantField: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotFound, gotField := detectForEach(&tt.rule)
			if gotFound != tt.wantFound {
				t.Errorf("detectForEach() found = %v, want %v", gotFound, tt.wantFound)
			}
			if gotField != tt.wantField {
				t.Errorf("detectForEach() field = %q, want %q", gotField, tt.wantField)
			}
		})
	}
}

func TestDetectVariableComparisons(t *testing.T) {
	tests := []struct {
		name string
		rule rule.Rule
		want bool
	}{
		{
			name: "has variable comparison",
			rule: rule.Rule{
				Conditions: &rule.Conditions{
					Items: []rule.Condition{
						{Field: "temperature", Operator: "gt", Value: "{threshold}"},
					},
				},
			},
			want: true,
		},
		{
			name: "no variable comparison - literal value",
			rule: rule.Rule{
				Conditions: &rule.Conditions{
					Items: []rule.Condition{
						{Field: "temperature", Operator: "gt", Value: float64(100)},
					},
				},
			},
			want: false,
		},
		{
			name: "no variable comparison - string literal",
			rule: rule.Rule{
				Conditions: &rule.Conditions{
					Items: []rule.Condition{
						{Field: "status", Operator: "eq", Value: "active"},
					},
				},
			},
			want: false,
		},
		{
			name: "variable comparison in nested group",
			rule: rule.Rule{
				Conditions: &rule.Conditions{
					Groups: []rule.Conditions{
						{
							Items: []rule.Condition{
								{Field: "value", Operator: "lte", Value: "{max_value}"},
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "nil conditions",
			rule: rule.Rule{
				Conditions: nil,
			},
			want: false,
		},
		{
			name: "empty conditions",
			rule: rule.Rule{
				Conditions: &rule.Conditions{},
			},
			want: false,
		},
		{
			name: "variable in nested condition (array operator)",
			rule: rule.Rule{
				Conditions: &rule.Conditions{
					Items: []rule.Condition{
						{
							Field:    "items",
							Operator: "any",
							Conditions: &rule.Conditions{
								Items: []rule.Condition{
									{Field: "price", Operator: "lt", Value: "{budget}"},
								},
							},
						},
					},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectVariableComparisons(&tt.rule)
			if got != tt.want {
				t.Errorf("detectVariableComparisons() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAnalyzeRuleFeatures(t *testing.T) {
	tests := []struct {
		name     string
		rule     rule.Rule
		validate func(t *testing.T, features RuleFeatures)
	}{
		{
			name: "forEach detected",
			rule: rule.Rule{
				Action: rule.Action{
					NATS: &rule.NATSAction{
						ForEach: "{readings}",
					},
				},
			},
			validate: func(t *testing.T, features RuleFeatures) {
				if !features.UsesForEach {
					t.Error("UsesForEach should be true")
				}
				if features.ForEachField != "{readings}" {
					t.Errorf("ForEachField = %q, want {readings}", features.ForEachField)
				}
			},
		},
		{
			name: "KV lookups detected in field",
			rule: rule.Rule{
				Conditions: &rule.Conditions{
					Items: []rule.Condition{
						{Field: "@kv.config.{id}:max_temp", Operator: "gt", Value: float64(0)},
					},
				},
			},
			validate: func(t *testing.T, features RuleFeatures) {
				if !features.HasKVLookups {
					t.Error("HasKVLookups should be true")
				}
				if len(features.KVBuckets) != 1 || features.KVBuckets[0] != "config" {
					t.Errorf("KVBuckets = %v, want [config]", features.KVBuckets)
				}
			},
		},
		{
			name: "KV lookups detected in value",
			rule: rule.Rule{
				Conditions: &rule.Conditions{
					Items: []rule.Condition{
						{Field: "temperature", Operator: "gt", Value: "{@kv.thresholds.sensor1:max}"},
					},
				},
			},
			validate: func(t *testing.T, features RuleFeatures) {
				if !features.HasKVLookups {
					t.Error("HasKVLookups should be true")
				}
				if !features.HasVariableComparisons {
					t.Error("HasVariableComparisons should be true (KV in value)")
				}
				if len(features.KVBuckets) != 1 || features.KVBuckets[0] != "thresholds" {
					t.Errorf("KVBuckets = %v, want [thresholds]", features.KVBuckets)
				}
			},
		},
		{
			name: "time conditions detected",
			rule: rule.Rule{
				Conditions: &rule.Conditions{
					Items: []rule.Condition{
						{Field: "@time.hour", Operator: "gte", Value: float64(9)},
					},
				},
			},
			validate: func(t *testing.T, features RuleFeatures) {
				if !features.HasTimeConditions {
					t.Error("HasTimeConditions should be true")
				}
			},
		},
		{
			name: "day conditions detected",
			rule: rule.Rule{
				Conditions: &rule.Conditions{
					Items: []rule.Condition{
						{Field: "@day.weekday", Operator: "in", Value: []interface{}{"Monday", "Tuesday"}},
					},
				},
			},
			validate: func(t *testing.T, features RuleFeatures) {
				if !features.HasTimeConditions {
					t.Error("HasTimeConditions should be true for @day")
				}
			},
		},
		{
			name: "date conditions detected",
			rule: rule.Rule{
				Conditions: &rule.Conditions{
					Items: []rule.Condition{
						{Field: "@date.month", Operator: "eq", Value: float64(12)},
					},
				},
			},
			validate: func(t *testing.T, features RuleFeatures) {
				if !features.HasTimeConditions {
					t.Error("HasTimeConditions should be true for @date")
				}
			},
		},
		{
			name: "array operators detected - any",
			rule: rule.Rule{
				Conditions: &rule.Conditions{
					Items: []rule.Condition{
						{
							Field:    "items",
							Operator: "any",
							Conditions: &rule.Conditions{
								Items: []rule.Condition{
									{Field: "status", Operator: "eq", Value: "active"},
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, features RuleFeatures) {
				if !features.HasArrayOperators {
					t.Error("HasArrayOperators should be true for 'any'")
				}
			},
		},
		{
			name: "array operators detected - all",
			rule: rule.Rule{
				Conditions: &rule.Conditions{
					Items: []rule.Condition{
						{
							Field:    "items",
							Operator: "all",
							Conditions: &rule.Conditions{
								Items: []rule.Condition{
									{Field: "valid", Operator: "eq", Value: true},
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, features RuleFeatures) {
				if !features.HasArrayOperators {
					t.Error("HasArrayOperators should be true for 'all'")
				}
			},
		},
		{
			name: "array operators detected - none",
			rule: rule.Rule{
				Conditions: &rule.Conditions{
					Items: []rule.Condition{
						{
							Field:    "errors",
							Operator: "none",
							Conditions: &rule.Conditions{
								Items: []rule.Condition{
									{Field: "severity", Operator: "eq", Value: "critical"},
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, features RuleFeatures) {
				if !features.HasArrayOperators {
					t.Error("HasArrayOperators should be true for 'none'")
				}
			},
		},
		{
			name: "variable comparisons tracked in ComparisonFields",
			rule: rule.Rule{
				Conditions: &rule.Conditions{
					Items: []rule.Condition{
						{Field: "temperature", Operator: "gt", Value: "{threshold}"},
						{Field: "humidity", Operator: "lt", Value: "{max_humidity}"},
					},
				},
			},
			validate: func(t *testing.T, features RuleFeatures) {
				if !features.HasVariableComparisons {
					t.Error("HasVariableComparisons should be true")
				}
				if len(features.ComparisonFields) != 2 {
					t.Errorf("ComparisonFields length = %d, want 2", len(features.ComparisonFields))
				}
			},
		},
		{
			name: "multiple KV buckets detected",
			rule: rule.Rule{
				Conditions: &rule.Conditions{
					Items: []rule.Condition{
						{Field: "@kv.config.key1", Operator: "eq", Value: "a"},
						{Field: "@kv.users.key2", Operator: "eq", Value: "b"},
						{Field: "temp", Operator: "gt", Value: "{@kv.thresholds.key3}"},
					},
				},
			},
			validate: func(t *testing.T, features RuleFeatures) {
				if len(features.KVBuckets) != 3 {
					t.Errorf("KVBuckets length = %d, want 3", len(features.KVBuckets))
				}
			},
		},
		{
			name: "no features",
			rule: rule.Rule{
				Conditions: &rule.Conditions{
					Items: []rule.Condition{
						{Field: "status", Operator: "eq", Value: "active"},
					},
				},
				Action: rule.Action{
					NATS: &rule.NATSAction{
						Subject: "output",
					},
				},
			},
			validate: func(t *testing.T, features RuleFeatures) {
				if features.UsesForEach {
					t.Error("UsesForEach should be false")
				}
				if features.HasVariableComparisons {
					t.Error("HasVariableComparisons should be false")
				}
				if features.HasKVLookups {
					t.Error("HasKVLookups should be false")
				}
				if features.HasTimeConditions {
					t.Error("HasTimeConditions should be false")
				}
				if features.HasArrayOperators {
					t.Error("HasArrayOperators should be false")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			features := analyzeRuleFeatures(&tt.rule)
			tt.validate(t, features)
		})
	}
}

func TestHasVariableComparisonsRecursive(t *testing.T) {
	tests := []struct {
		name  string
		conds *rule.Conditions
		want  bool
	}{
		{
			name: "direct variable comparison",
			conds: &rule.Conditions{
				Items: []rule.Condition{
					{Field: "a", Operator: "eq", Value: "{b}"},
				},
			},
			want: true,
		},
		{
			name: "variable in nested group",
			conds: &rule.Conditions{
				Groups: []rule.Conditions{
					{
						Groups: []rule.Conditions{
							{
								Items: []rule.Condition{
									{Field: "x", Operator: "eq", Value: "{y}"},
								},
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "variable in array operator condition",
			conds: &rule.Conditions{
				Items: []rule.Condition{
					{
						Field:    "items",
						Operator: "any",
						Conditions: &rule.Conditions{
							Items: []rule.Condition{
								{Field: "val", Operator: "gt", Value: "{min}"},
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "no variables anywhere",
			conds: &rule.Conditions{
				Items: []rule.Condition{
					{Field: "status", Operator: "eq", Value: "active"},
				},
				Groups: []rule.Conditions{
					{
						Items: []rule.Condition{
							{Field: "count", Operator: "gt", Value: float64(10)},
						},
					},
				},
			},
			want: false,
		},
		{
			name: "value not a string",
			conds: &rule.Conditions{
				Items: []rule.Condition{
					{Field: "count", Operator: "eq", Value: float64(42)},
					{Field: "active", Operator: "eq", Value: true},
				},
			},
			want: false,
		},
		{
			name: "string value but not a template",
			conds: &rule.Conditions{
				Items: []rule.Condition{
					{Field: "name", Operator: "eq", Value: "not a {template"},
					{Field: "other", Operator: "eq", Value: "template} without start"},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasVariableComparisonsRecursive(tt.conds)
			if got != tt.want {
				t.Errorf("hasVariableComparisonsRecursive() = %v, want %v", got, tt.want)
			}
		})
	}
}

