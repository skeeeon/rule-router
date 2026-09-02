package rule

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
)

// Random value functions for templates. These generate SYNTHETIC / FIXTURE data
// — demo telemetry, simulated readings, placeholder payloads. They are not for
// nonces (use uuid4()), correlation ids (uuid7()), A/B bucketing (which needs a
// hash of a stable field, or a retry re-buckets the message), or sampling.
//
// The grammar is deliberately tiny and is expected to stay that way:
//
//   - Arguments are literals. No arithmetic, no comparisons, no calls nested
//     inside calls. The moment any of those appear, this stops being a template
//     engine and becomes an expression language.
//   - Fixed arity per function. No optional arguments.
//   - strings.Split on "," is the entire argument grammar. No quoting, no
//     escaping, no whitespace handling — a value containing a comma or a space
//     cannot be expressed, and that is a documented limit rather than a gap to
//     be filled in later.
//
// A request that cannot be met inside those constraints is a different feature,
// not a reason to relax them.

// randomPrefix is the namespace that marks a template call as belonging to this
// file. Dispatch keys off it (rather than "any name ending in ')'") so an
// unrelated variable that happens to end in a paren stays a variable.
const randomPrefix = "random."

// maxRandomDecimals bounds the third argument of random.float. Past ten places a
// float64 is inventing digits it does not have.
const maxRandomDecimals = 10

// randomValue evaluates a call such as "random.int(1,100)" — the text between
// "{@" and "}", with the leading '@' already stripped.
//
// This is the single definition of what a valid call is. The template engine
// calls it to render, and the loader calls it at load time and keeps only the
// error, so a rule that loads is a rule that renders. Two implementations of
// "is this call well-formed" would eventually disagree; one cannot. The cost is
// that the loader generates and discards a value per call per load, which is
// far cheaper than that class of bug.
func randomValue(call string) (string, error) {
	open := strings.IndexByte(call, '(')
	if open < 0 || !strings.HasSuffix(call, ")") {
		return "", fmt.Errorf("malformed call, expected name(arguments)")
	}

	name := call[:open]
	args := strings.Split(call[open+1:len(call)-1], ",")

	switch name {
	case "random.int":
		return randomInt(args)
	case "random.float":
		return randomFloat(args)
	case "random.choice":
		return randomChoice(args)
	default:
		return "", fmt.Errorf("unknown function %q", name)
	}
}

// randomInt returns an integer in [min,max]. Both ends are inclusive, which is
// what reads naturally in a rule: random.int(1,6) is a die.
func randomInt(args []string) (string, error) {
	if len(args) != 2 {
		return "", fmt.Errorf("random.int takes 2 arguments (min,max), got %d", len(args))
	}

	minVal, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return "", fmt.Errorf("min %q is not an integer", args[0])
	}
	maxVal, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return "", fmt.Errorf("max %q is not an integer", args[1])
	}
	if maxVal < minVal {
		return "", fmt.Errorf("max (%d) is less than min (%d)", maxVal, minVal)
	}

	// Inclusive of both ends, so the span is max-min+1. Kept in int64 with an
	// explicit overflow check rather than promoted to a wider type: this is
	// fixture data, and a range that wide is a mistake worth reporting.
	span := maxVal - minVal
	if span < 0 || span == math.MaxInt64 {
		return "", fmt.Errorf("range %d..%d is too large", minVal, maxVal)
	}

	return strconv.FormatInt(minVal+rand.Int64N(span+1), 10), nil
}

// randomFloat returns a value in [min,max) formatted to a fixed number of
// decimal places. The places argument is required rather than optional: fixed
// arity is simpler to validate and document, and an unrounded -18.347293847 in a
// temperature payload is not what anyone wanted.
func randomFloat(args []string) (string, error) {
	if len(args) != 3 {
		return "", fmt.Errorf("random.float takes 3 arguments (min,max,decimals), got %d", len(args))
	}

	minVal, err := strconv.ParseFloat(args[0], 64)
	if err != nil {
		return "", fmt.Errorf("min %q is not a number", args[0])
	}
	maxVal, err := strconv.ParseFloat(args[1], 64)
	if err != nil {
		return "", fmt.Errorf("max %q is not a number", args[1])
	}
	decimals, err := strconv.Atoi(args[2])
	if err != nil {
		return "", fmt.Errorf("decimals %q is not an integer", args[2])
	}

	// ParseFloat accepts "Inf" and "NaN". Both format as tokens that are not
	// JSON numbers, and the whole point of this function is to drop a bare value
	// into a JSON payload, so they are rejected at the boundary.
	if math.IsInf(minVal, 0) || math.IsNaN(minVal) || math.IsInf(maxVal, 0) || math.IsNaN(maxVal) {
		return "", errors.New("min and max must be finite numbers")
	}
	if maxVal < minVal {
		return "", fmt.Errorf("max (%v) is less than min (%v)", maxVal, minVal)
	}
	if decimals < 0 || decimals > maxRandomDecimals {
		return "", fmt.Errorf("decimals must be between 0 and %d, got %d", maxRandomDecimals, decimals)
	}

	value := minVal + rand.Float64()*(maxVal-minVal)

	// 'f' never emits exponent notation. JSON permits exponents, but a payload
	// reading "celsius":1.8e+01 helps nobody, and fixed notation is what the
	// decimals argument was asked for.
	return strconv.FormatFloat(value, 'f', decimals, 64), nil
}

// randomChoice returns one of its arguments verbatim.
func randomChoice(args []string) (string, error) {
	// strings.Split of an empty string yields one empty element, so
	// "random.choice()" arrives here as [""] rather than an empty slice.
	if len(args) == 0 || (len(args) == 1 && args[0] == "") {
		return "", errors.New("random.choice requires at least one value")
	}

	return args[rand.IntN(len(args))], nil
}
