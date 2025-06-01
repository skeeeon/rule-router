//file: internal/rule/types.go
package rule

type Rule struct {
	Topic      string       `json:"topic" yaml:"topic"`
	Conditions *Conditions  `json:"conditions" yaml:"conditions"`
	Action     *Action      `json:"action" yaml:"action"`
}

// Conditions represents a group of conditions with a logical operator
type Conditions struct {
	Operator string      `json:"operator" yaml:"operator"` // "and" or "or"
	Items    []Condition `json:"items" yaml:"items"`
	Groups   []Conditions `json:"groups,omitempty" yaml:"groups,omitempty"` // For nested condition groups
}

type Condition struct {
	Field    string      `json:"field" yaml:"field"`
	Operator string      `json:"operator" yaml:"operator"`
	Value    interface{} `json:"value" yaml:"value"`
}

type Action struct {
	Topic   string `json:"topic" yaml:"topic"`
	Payload string `json:"payload" yaml:"payload"`
}
