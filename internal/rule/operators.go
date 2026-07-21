// file: internal/rule/operators.go

package rule

// validOperators is the single source of truth for valid rule condition
// operators. Both the loader (rule validation) and rule-cli (interactive
// validation via internal/cli) consult it through IsValidOperator, so adding an
// operator here is picked up everywhere at once.
var validOperators = map[string]bool{
	"eq":           true,
	"neq":          true,
	"gt":           true,
	"lt":           true,
	"gte":          true,
	"lte":          true,
	"contains":     true,
	"not_contains": true,
	"in":           true,
	"not_in":       true,
	"exists":       true,
	"recent":       true,
	"any":          true,
	"all":          true,
	"none":         true,
}

// IsValidOperator reports whether op is a recognized rule condition operator.
func IsValidOperator(op string) bool {
	return validOperators[op]
}
