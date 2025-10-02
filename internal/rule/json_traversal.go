//file: internal/rule/json_traversal.go

package rule

import (
	"fmt"
	"strconv"
	"strings"
)

// JSONPathTraverser provides unified JSON path traversal for message fields, KV values, and any other JSON data
// Supports both object property access and array index access using dot notation
// Examples:
//   - "user.name" → object property access
//   - "readings.0.value" → array index access
//   - "data.sensors.2.readings.5.timestamp" → mixed nested access
type JSONPathTraverser struct {
	// Optional: could add configuration like supporting negative indices in the future
}

// NewJSONPathTraverser creates a new path traverser instance
func NewJSONPathTraverser() *JSONPathTraverser {
	return &JSONPathTraverser{}
}

// TraversePath navigates through a JSON structure using a dot-separated path
// Supports both map[string]interface{} for objects and []interface{} for arrays
// Returns the value at the path and an error if the path is invalid
func (t *JSONPathTraverser) TraversePath(data interface{}, path []string) (interface{}, error) {
	if len(path) == 0 {
		return data, nil
	}

	current := data
	
	for i, segment := range path {
		if segment == "" {
			return nil, fmt.Errorf("empty path segment at position %d", i)
		}

		// Attempt traversal based on current value type
		next, err := t.traverseSegment(current, segment, i, path)
		if err != nil {
			return nil, err
		}
		current = next
	}

	return current, nil
}

// TraversePathString is a convenience method that takes a dot-separated string path
// Example: "user.profile.email" or "readings.0.value"
func (t *JSONPathTraverser) TraversePathString(data interface{}, pathStr string) (interface{}, error) {
	if pathStr == "" {
		return data, nil
	}
	
	path := strings.Split(pathStr, ".")
	return t.TraversePath(data, path)
}

// traverseSegment handles traversal of a single path segment
func (t *JSONPathTraverser) traverseSegment(current interface{}, segment string, position int, fullPath []string) (interface{}, error) {
	switch v := current.(type) {
	case map[string]interface{}:
		// Object property access
		value, exists := v[segment]
		if !exists {
			// Provide helpful context in error message
			availableKeys := t.getMapKeys(v, 5) // Get first 5 keys for context
			return nil, fmt.Errorf("key '%s' not found at path position %d (available keys: %s)", 
				segment, position, strings.Join(availableKeys, ", "))
		}
		return value, nil

	case map[interface{}]interface{}:
		// Handle maps with interface{} keys (common in unmarshaled YAML)
		value, exists := v[segment]
		if !exists {
			// Try with various type conversions for the key
			for key, val := range v {
				if fmt.Sprintf("%v", key) == segment {
					return val, nil
				}
			}
			return nil, fmt.Errorf("key '%s' not found at path position %d", segment, position)
		}
		return value, nil

	case []interface{}:
		// Array index access
		index, err := t.parseArrayIndex(segment, len(v))
		if err != nil {
			return nil, fmt.Errorf("at path position %d: %w", position, err)
		}
		
		// Bounds check
		if index < 0 || index >= len(v) {
			return nil, fmt.Errorf("array index %d out of bounds at path position %d (array length: %d)", 
				index, position, len(v))
		}
		
		return v[index], nil

	case nil:
		return nil, fmt.Errorf("cannot traverse into nil at path position %d (segment: '%s')", position, segment)

	default:
		// Primitive type or unknown type - cannot traverse further
		typeStr := fmt.Sprintf("%T", current)
		return nil, fmt.Errorf("cannot traverse into %s at path position %d (segment: '%s')", typeStr, position, segment)
	}
}

// parseArrayIndex parses an array index from a string segment
// Supports:
//   - Positive integers: "0", "1", "10"
//   - Negative integers for reverse indexing: "-1" (last), "-2" (second to last)
func (t *JSONPathTraverser) parseArrayIndex(segment string, arrayLen int) (int, error) {
	index, err := strconv.Atoi(segment)
	if err != nil {
		return 0, fmt.Errorf("invalid array index '%s': must be an integer", segment)
	}

	// Handle negative indices (Python-style reverse indexing)
	if index < 0 {
		// -1 means last element, -2 means second to last, etc.
		index = arrayLen + index
		if index < 0 {
			return 0, fmt.Errorf("negative array index '%s' out of bounds (array length: %d)", segment, arrayLen)
		}
	}

	return index, nil
}

// getMapKeys returns up to maxKeys keys from a map for error messages
func (t *JSONPathTraverser) getMapKeys(m map[string]interface{}, maxKeys int) []string {
	keys := make([]string, 0, maxKeys)
	count := 0
	for k := range m {
		if count >= maxKeys {
			keys = append(keys, "...")
			break
		}
		keys = append(keys, k)
		count++
	}
	return keys
}

// ExtractValue is a convenience function for simple path extraction
// Returns nil if the path doesn't exist (no error)
func (t *JSONPathTraverser) ExtractValue(data interface{}, path []string) interface{} {
	value, err := t.TraversePath(data, path)
	if err != nil {
		return nil
	}
	return value
}

// HasPath checks if a path exists in the data structure
func (t *JSONPathTraverser) HasPath(data interface{}, path []string) bool {
	_, err := t.TraversePath(data, path)
	return err == nil
}

// Global singleton instance for convenience
var defaultTraverser = NewJSONPathTraverser()

// TraverseJSONPath is a package-level convenience function using the default traverser
// This is what most code will use
func TraverseJSONPath(data interface{}, path []string) (interface{}, error) {
	return defaultTraverser.TraversePath(data, path)
}

// TraverseJSONPathString is a package-level convenience function for string paths
func TraverseJSONPathString(data interface{}, pathStr string) (interface{}, error) {
	return defaultTraverser.TraversePathString(data, pathStr)
}

// ExtractJSONValue is a package-level convenience function that returns nil on error
func ExtractJSONValue(data interface{}, path []string) interface{} {
	return defaultTraverser.ExtractValue(data, path)
}

// HasJSONPath is a package-level convenience function to check path existence
func HasJSONPath(data interface{}, path []string) bool {
	return defaultTraverser.HasPath(data, path)
}
