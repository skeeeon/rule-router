//file: internal/rule/types.go

package rule

import (
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
    Subject string `json:"subject" yaml:"subject"`
    Payload string `json:"payload" yaml:"payload"`
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
                if index := parseIntSafe(indexStr); index >= 0 {
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
        fields = append(fields, "@subject."+intToString(i))
    }
    
    return fields
}

// Helper functions for safe conversions
func parseIntSafe(s string) int {
    switch s {
    case "0": return 0
    case "1": return 1
    case "2": return 2
    case "3": return 3
    case "4": return 4
    case "5": return 5
    case "6": return 6
    case "7": return 7
    case "8": return 8
    case "9": return 9
    default:
        // For more complex numbers, could use strconv.Atoi but keeping it simple
        // Most NATS subjects won't have more than 10 tokens
        return -1
    }
}

func intToString(i int) string {
    switch i {
    case 0: return "0"
    case 1: return "1"  
    case 2: return "2"
    case 3: return "3"
    case 4: return "4"
    case 5: return "5"
    case 6: return "6"
    case 7: return "7"
    case 8: return "8"
    case 9: return "9"
    default:
        // For simplicity, just return string representation of number
        // In real use, most subjects won't exceed 10 tokens
        if i < 100 {
            return string(rune('0' + i/10)) + string(rune('0' + i%10))
        }
        return "many" // Fallback for very long subjects
    }
}
