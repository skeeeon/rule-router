//file: internal/rule/kv_context.go

package rule

import (
	json "github.com/goccy/go-json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	watermillNats "github.com/nats-io/nats.go"
	"rule-router/internal/logger"
)

// Pre-compiled regex for variable substitution in KV fields
var (
	kvVariablePattern = regexp.MustCompile(`\{([^}]+)\}`)
)

// KVContext provides access to NATS Key-Value stores for rule evaluation and templating
type KVContext struct {
	stores map[string]watermillNats.KeyValue
	logger *logger.Logger
}

// NewKVContext creates a new KV context with the provided KV stores
func NewKVContext(stores map[string]watermillNats.KeyValue, logger *logger.Logger) *KVContext {
	if logger == nil {
		// This should never happen in practice, but be defensive
		panic("KVContext requires a logger")
	}

	ctx := &KVContext{
		stores: make(map[string]watermillNats.KeyValue),
		logger: logger,
	}

	// Copy the stores map to avoid external modification
	for bucket, store := range stores {
		ctx.stores[bucket] = store
	}

	logger.Info("KV context initialized with JSON path support", 
		"bucketCount", len(ctx.stores), 
		"buckets", ctx.getBucketNames())
	return ctx
}

// GetField retrieves a value from a KV store based on the field specification
// Supports format: "@kv.bucket_name.key_name[.json.path.to.field]"
// Returns the value and whether it was found successfully
func (kv *KVContext) GetField(field string) (interface{}, bool) {
	// Parse the KV field specification (now with JSON path support)
	bucket, key, jsonPath, err := kv.parseKVFieldWithPath(field)
	if err != nil {
		kv.logger.Debug("invalid KV field format", "field", field, "error", err)
		return nil, false
	}

	kv.logger.Debug("looking up KV field with JSON path", 
		"field", field, 
		"bucket", bucket, 
		"key", key,
		"jsonPath", jsonPath)

	// Check if the bucket exists in our configured stores
	store, exists := kv.stores[bucket]
	if !exists {
		kv.logger.Debug("KV bucket not configured", "bucket", bucket, "availableBuckets", kv.getBucketNames())
		return nil, false
	}

	// Perform the KV lookup
	entry, err := store.Get(key)
	if err != nil {
		// Handle key not found (this is a normal case, not an error)
		if err == watermillNats.ErrKeyNotFound {
			kv.logger.Debug("KV key not found", "bucket", bucket, "key", key)
			return nil, false
		}

		// Handle other errors (network issues, etc.)
		kv.logger.Debug("KV lookup failed", "bucket", bucket, "key", key, "error", err)
		return nil, false
	}

	// Get the raw value
	rawValue := entry.Value()
	
	// If no JSON path, return the converted raw value
	if len(jsonPath) == 0 {
		value := kv.convertValue(rawValue)
		kv.logger.Debug("KV lookup successful (no JSON path)", "bucket", bucket, "key", key, "value", value)
		return value, true
	}

	// Parse as JSON and traverse the path
	value, err := kv.traverseJSONPath(rawValue, jsonPath)
	if err != nil {
		kv.logger.Debug("JSON path traversal failed", 
			"bucket", bucket, 
			"key", key, 
			"jsonPath", jsonPath, 
			"error", err)
		return nil, false
	}

	kv.logger.Debug("KV lookup with JSON path successful", 
		"bucket", bucket, 
		"key", key, 
		"jsonPath", jsonPath, 
		"value", value)
	return value, true
}

// ENHANCED: GetFieldWithContext retrieves a value with variable substitution support
// Now properly handles missing variables by returning empty strings
func (kv *KVContext) GetFieldWithContext(field string, msgData map[string]interface{}, timeCtx *TimeContext, subjectCtx *SubjectContext) (interface{}, bool) {
	// First resolve any variables in the field specification
	resolvedField, hasUnresolvedVars, err := kv.resolveVariablesEnhanced(field, msgData, timeCtx, subjectCtx)
	if err != nil {
		kv.logger.Debug("failed to resolve variables in KV field", "field", field, "error", err)
		return "", false // Return empty string for template processing
	}
	
	// ENHANCED: If variables couldn't be resolved, return empty string
	if hasUnresolvedVars {
		kv.logger.Debug("KV field has unresolved variables, returning empty", "original", field, "resolved", resolvedField)
		return "", false
	}

	kv.logger.Debug("resolved KV field variables", "original", field, "resolved", resolvedField)

	// Now do the actual KV lookup with JSON path support
	value, found := kv.GetField(resolvedField)
	
	// ENHANCED: If KV lookup fails, return empty string (not nil)
	if !found {
		kv.logger.Debug("KV lookup failed after variable resolution", "resolvedField", resolvedField)
		return "", false
	}
	
	return value, true
}

// parseKVFieldWithPath parses a KV field specification with optional JSON path
// Format: "@kv.bucket.key[.json.path.to.field]"
// Returns bucket, key, jsonPath (as slice), and error
func (kv *KVContext) parseKVFieldWithPath(field string) (bucket, key string, jsonPath []string, err error) {
	// Field must start with "@kv."
	if !strings.HasPrefix(field, "@kv.") {
		return "", "", nil, fmt.Errorf("KV field must start with '@kv.', got: %s", field)
	}

	// Remove "@kv." prefix
	remainder := field[4:]
	
	// Split by dots
	parts := strings.Split(remainder, ".")
	if len(parts) < 2 {
		return "", "", nil, fmt.Errorf("KV field must have at least bucket.key format, got: %s", field)
	}

	bucket = parts[0]
	key = parts[1]
	
	// Everything after bucket.key is the JSON path
	if len(parts) > 2 {
		jsonPath = parts[2:]
	}

	// Validate bucket and key are not empty
	if bucket == "" {
		return "", "", nil, fmt.Errorf("KV bucket name cannot be empty in field: %s", field)
	}
	if key == "" {
		return "", "", nil, fmt.Errorf("KV key name cannot be empty in field: %s", field)
	}

	return bucket, key, jsonPath, nil
}

// traverseJSONPath parses JSON and traverses the specified path
func (kv *KVContext) traverseJSONPath(jsonData []byte, path []string) (interface{}, error) {
	// Parse the JSON data
	var jsonObj interface{}
	if err := json.Unmarshal(jsonData, &jsonObj); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Traverse the path
	current := jsonObj
	for i, segment := range path {
		kv.logger.Debug("traversing JSON path segment", 
			"segment", segment, 
			"position", i, 
			"totalSegments", len(path))

		switch v := current.(type) {
		case map[string]interface{}:
			var ok bool
			current, ok = v[segment]
			if !ok {
				return nil, fmt.Errorf("JSON path segment not found: %s", segment)
			}
		case []interface{}:
			// Handle array access if the segment is a number
			index, err := strconv.Atoi(segment)
			if err != nil {
				return nil, fmt.Errorf("invalid array index '%s': %w", segment, err)
			}
			if index < 0 || index >= len(v) {
				return nil, fmt.Errorf("array index %d out of bounds (length: %d)", index, len(v))
			}
			current = v[index]
		default:
			return nil, fmt.Errorf("cannot traverse into non-object/non-array at path segment: %s", segment)
		}
	}

	return current, nil
}

// ENHANCED: resolveVariablesEnhanced replaces {variable} placeholders with better error handling
func (kv *KVContext) resolveVariablesEnhanced(field string, msgData map[string]interface{}, timeCtx *TimeContext, subjectCtx *SubjectContext) (string, bool, error) {
	if !strings.Contains(field, "{") {
		// No variables to resolve
		return field, false, nil
	}

	result := field
	hasUnresolvedVars := false

	// Replace all {variable} patterns
	result = kvVariablePattern.ReplaceAllStringFunc(result, func(match string) string {
		varName := match[1 : len(match)-1] // Remove { and }
		
		kv.logger.Debug("resolving KV variable", "variable", varName, "inField", field)

		// Try to resolve the variable from different contexts
		if value, found := kv.resolveVariable(varName, msgData, timeCtx, subjectCtx); found {
			strValue := kv.convertToString(value)
			kv.logger.Debug("resolved KV variable", "variable", varName, "value", strValue)
			return strValue
		}

		// ENHANCED: Mark as unresolved instead of returning original
		kv.logger.Debug("KV variable not found", "variable", varName)
		hasUnresolvedVars = true
		return "" // Return empty string for missing variables
	})

	return result, hasUnresolvedVars, nil
}

// resolveVariable resolves a single variable from available contexts
func (kv *KVContext) resolveVariable(varName string, msgData map[string]interface{}, timeCtx *TimeContext, subjectCtx *SubjectContext) (interface{}, bool) {
	// Check if it's a system field (time, subject)
	if strings.HasPrefix(varName, "@") {
		// System field resolution
		if strings.HasPrefix(varName, "@subject") {
			return subjectCtx.GetField(varName)
		}
		if strings.HasPrefix(varName, "@time") || strings.HasPrefix(varName, "@date") || strings.HasPrefix(varName, "@timestamp") {
			return timeCtx.GetField(varName)
		}
		return nil, false
	}

	// Regular message field (supports nested paths)
	if strings.Contains(varName, ".") {
		// Nested field access
		path := strings.Split(varName, ".")
		return kv.getValueFromPath(msgData, path)
	} else {
		// Direct field access
		value, exists := msgData[varName]
		return value, exists
	}
}

// getValueFromPath traverses nested map structure
func (kv *KVContext) getValueFromPath(data map[string]interface{}, path []string) (interface{}, bool) {
	var current interface{} = data

	for _, key := range path {
		switch v := current.(type) {
		case map[string]interface{}:
			var ok bool
			current, ok = v[key]
			if !ok {
				return nil, false
			}
		case map[interface{}]interface{}:
			var ok bool
			current, ok = v[key]
			if !ok {
				return nil, false
			}
		default:
			return nil, false
		}
	}

	return current, true
}

// convertValue converts raw KV store bytes to appropriate Go types
// Only used when there's no JSON path (backwards compatibility)
func (kv *KVContext) convertValue(data []byte) interface{} {
	if len(data) == 0 {
		return ""
	}

	str := string(data)

	// Try JSON parsing first for structured data
	var jsonValue interface{}
	if err := json.Unmarshal(data, &jsonValue); err == nil {
		// Successfully parsed as JSON, return the structured data
		return jsonValue
	}

	// Fall back to primitive type parsing
	// Try boolean conversion first (true/false)
	if lowerStr := strings.ToLower(str); lowerStr == "true" || lowerStr == "false" {
		if val, err := strconv.ParseBool(lowerStr); err == nil {
			return val
		}
	}

	// Try integer conversion
	if val, err := strconv.ParseInt(str, 10, 64); err == nil {
		// Check if it fits in a regular int
		if val >= -2147483648 && val <= 2147483647 {
			return int(val)
		}
		return val
	}

	// Try float conversion
	if val, err := strconv.ParseFloat(str, 64); err == nil {
		return val
	}

	// Default to string
	return str
}

// ENHANCED: convertToString with better handling of edge cases
func (kv *KVContext) convertToString(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		return strconv.FormatBool(v)
	case nil:
		return "" // ENHANCED: Return empty string for nil, not "null"
	default:
		// For complex types, marshal to JSON
		if jsonBytes, err := json.Marshal(v); err == nil {
			return string(jsonBytes)
		}
		// Final fallback
		return fmt.Sprintf("%v", v)
	}
}

// getBucketNames returns a list of configured bucket names for logging
func (kv *KVContext) getBucketNames() []string {
	names := make([]string, 0, len(kv.stores))
	for bucket := range kv.stores {
		names = append(names, bucket)
	}
	return names
}

// GetAllBuckets returns the names of all configured KV buckets
func (kv *KVContext) GetAllBuckets() []string {
	return kv.getBucketNames()
}

// HasBucket checks if a specific bucket is configured
func (kv *KVContext) HasBucket(bucketName string) bool {
	_, exists := kv.stores[bucketName]
	return exists
}

// GetStats returns basic statistics about the KV context
func (kv *KVContext) GetStats() map[string]interface{} {
	return map[string]interface{}{
		"bucket_count":      len(kv.stores),
		"bucket_names":      kv.getBucketNames(),
		"initialized":       true,
		"json_path_support": true,
		"variable_resolution": "enhanced", // Indicates new error handling
	}
}
