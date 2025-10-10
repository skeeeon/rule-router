// file: internal/rule/types.go

package rule

import (
    "strconv"
    "strings"
)

type Rule struct {
    Subject    string       `json:"subject" yaml:"subject"`
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
    Subject     string `json:"subject" yaml:"subject"`
    Payload     string `json:"payload" yaml:"payload"`
    Passthrough bool   `json:"passthrough,omitempty" yaml:"passthrough,omitempty"` // NEW

    // RawPayload stores the original unmodified message bytes when passthrough is true.
    // This is populated during processing, not from config files.
    RawPayload  []byte `json:"-" yaml:"-"` // NEW: Not serialized
}

// SubjectContext provides access to NATS subject information for templates and conditions
type SubjectContext struct {
    Full   string   `json:"full"`   // Full subject: "sensors.temperature.room1"
    Tokens []string `json:"tokens"` // Split tokens: ["sensors", "temperature", "room1"]  
    Count  int      `json:"count"`  // Token count: 3
}

// NewSubjectContext creates a SubjectContext from a NATS subject string
func NewSubjectContext(subject string) *SubjectContext {
    if subject == "" {
        return &SubjectContext{
            Full:   "",
            Tokens: []string{},
            Count:  0,
        }
    }

    tokens := strings.Split(subject, ".")
    return &SubjectContext{
        Full:   subject,
        Tokens: tokens,
        Count:  len(tokens),
    }
}

// GetToken safely retrieves a token by index, returns empty string if out of bounds
func (sc *SubjectContext) GetToken(index int) string {
    if index < 0 || index >= len(sc.Tokens) {
        return ""
    }
    return sc.Tokens[index]
}

// GetField retrieves a subject field for template/condition processing
// Supports: "@subject", "@subject.0", "@subject.1", "@subject.count"
func (sc *SubjectContext) GetField(fieldName string) (interface{}, bool) {
    switch fieldName {
    case "@subject":
        return sc.Full, true
    case "@subject.count":
        return sc.Count, true
    default:
        // Check for indexed access: @subject.0, @subject.1, etc.
        if strings.HasPrefix(fieldName, "@subject.") {
            indexStr := fieldName[9:] // Remove "@subject."
            
            // Handle special cases
            switch indexStr {
            case "last":
                if sc.Count > 0 {
                    return sc.Tokens[sc.Count-1], true
                }
                return "", true
            case "first":
                if sc.Count > 0 {
                    return sc.Tokens[0], true
                }
                return "", true
            default:
                // Try to parse as integer index
                if index, err := strconv.Atoi(indexStr); err == nil && index >= 0 {
                    return sc.GetToken(index), true
                }
            }
        }
    }
    
    return nil, false
}

// GetAllFieldNames returns available subject field names for debugging
func (sc *SubjectContext) GetAllFieldNames() []string {
    fields := []string{"@subject", "@subject.count", "@subject.first", "@subject.last"}
    
    // Add indexed fields based on actual token count
    for i := 0; i < sc.Count; i++ {
        fields = append(fields, "@subject."+strconv.Itoa(i))
    }
    
    return fields
}
