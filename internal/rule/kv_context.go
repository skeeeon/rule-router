// file: internal/rule/kv_context.go

package rule

import (
	json "github.com/goccy/go-json"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"rule-router/internal/logger"
)

// Pre-compiled regex for variable substitution in KV fields
var (
	kvVariablePattern = regexp.MustCompile(`\{([^}]+)\}`)
)

// KVContext provides access to NATS Key-Value stores for rule evaluation and templating
// Now includes local cache support for improved performance
type KVContext struct {
	stores     map[string]jetstream.KeyValue
	logger     *logger.Logger
	localCache *LocalKVCache  // Local cache for performance optimization
	traverser  *JSONPathTraverser // Shared JSON path traversal
}

// NewKVContext creates a new KV context with the provided KV stores and optional local cache
func NewKVContext(stores map[string]jetstream.KeyValue, logger *logger.Logger, localCache *LocalKVCache) *KVContext {
	if logger == nil {
		// This should never happen in practice, but be defensive
		panic("KVContext requires a logger")
	}

	ctx := &KVContext{
		stores:     make(map[string]jetstream.KeyValue),
		logger:     logger,
		localCache: localCache,
		traverser:  NewJSONPathTraverser(), // Use shared traverser
	}

	// Copy the stores map to avoid external modification
	for bucket, store := range stores {
		ctx.stores[bucket] = store
	}

	cacheStatus := "disabled"
	if localCache != nil && localCache.IsEnabled() {
		cacheStatus = "enabled"
	}

	logger.Info("KV context initialized with colon delimiter syntax", 
		"bucketCount", len(ctx.stores), 
		"buckets", ctx.getBucketNames(),
		"localCache", cacheStatus,
		"syntax", "@kv.bucket.key:json.path")
	return ctx
}

// GetField retrieves a value from KV store (cache first, then NATS KV fallback)
// Supports format: "@kv.bucket_name.key_name:json.path.to.field"
// The colon (:) delimiter separates key from JSON path
// Returns the value and whether it was found successfully
func (kv *KVContext) GetField(field string) (interface{}, bool) {
	// Parse the KV field specification with colon delimiter
	bucket, key, jsonPath, err := kv.parseKVFieldWithPath(field)
	if err != nil {
		kv.logger.Debug("invalid KV field format", "field", field, "error", err)
		return nil, false
	}

	kv.logger.Debug("looking up KV field with colon syntax", 
		"field", field, 
		"bucket", bucket, 
		"key", key,
		"jsonPath", jsonPath)

	// Try local cache first for maximum performance
	if kv.localCache != nil && kv.localCache.IsEnabled() {
		if cachedValue, found := kv.localCache.Get(bucket, key); found {
			kv.logger.Debug("KV cache hit", "bucket", bucket, "key", key)
			
			// Process JSON path using shared traverser
			finalValue, err := kv.traverser.TraversePath(cachedValue, jsonPath)
			if err != nil {
				kv.logger.Debug("JSON path traversal failed on cached value", 
					"bucket", bucket, "key", key, "jsonPath", jsonPath, "error", err)
				return nil, false
			}
			return finalValue, true
		}
		
		kv.logger.Debug("KV cache miss", "bucket", bucket, "key", key)
	}

	// Cache miss or cache disabled - fallback to NATS KV
	kv.logger.Debug("falling back to NATS KV lookup", "bucket", bucket, "key", key)
	return kv.getFromNATSKV(bucket, key, jsonPath)
}

// GetFieldWithContext retrieves a value with variable substitution support
// Now properly handles missing variables by returning empty strings
func (kv *KVContext) GetFieldWithContext(field string, msgData map[string]interface{}, timeCtx *TimeContext, subjectCtx *SubjectContext) (interface{}, bool) {
	// First resolve any variables in the field specification
	resolvedField, hasUnresolvedVars, err := kv.resolveVariablesEnhanced(field, msgData, timeCtx, subjectCtx)
	if err != nil {
		kv.logger.Debug("failed to resolve variables in KV field", "field", field, "error", err)
		return "", false // Return empty string for template processing
	}
	
	// If variables couldn't be resolved, return empty string
	if hasUnresolvedVars {
		kv.logger.Warn("KV field has unresolved variables, returning empty", 
			"original", field, 
			"resolved", resolvedField,
			"impact", "Template will use empty value")
		return "", false
	}

	kv.logger.Debug("resolved KV field variables", "original", field, "resolved", resolvedField)

	// Now do the actual KV lookup (cache first, then NATS KV fallback)
	value, found := kv.GetField(resolvedField)
	
	// If KV lookup fails, return empty string (not nil)
	if !found {
		kv.logger.Warn("KV lookup failed after variable resolution",
			"resolvedField", resolvedField,
			"originalField", field,
			"impact", "Template will use empty value")
		return "", false
	}
	
	return value, true
}

// getFromNATSKV performs direct NATS KV lookup (fallback when cache misses)
func (kv *KVContext) getFromNATSKV(bucket, key string, jsonPath []string) (interface{}, bool) {
	// Check if the bucket exists in our configured stores
	store, exists := kv.stores[bucket]
	if !exists {
		kv.logger.Warn("KV bucket not configured", 
			"bucket", bucket, 
			"key", key,
			"availableBuckets", kv.getBucketNames(),
			"impact", "KV lookup will fail")
		return nil, false
	}

	// Create context with timeout for KV operations
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Perform the KV lookup
	entry, err := store.Get(ctx, key)
	if err != nil {
		if err == jetstream.ErrKeyNotFound {
			kv.logger.Warn("KV key does not exist in bucket",
				"bucket", bucket,
				"key", key,
				"jsonPath", jsonPath,
				"impact", "Rule will use empty value")
			return nil, false
		}

		// All other errors are infrastructure problems
		kv.logger.Error("NATS KV lookup failed - infrastructure issue",
			"bucket", bucket,
			"key", key,
			"error", err,
			"errorType", fmt.Sprintf("%T", err),
			"impact", "Rule will use empty value - investigate NATS connectivity/permissions")
		return nil, false
	}

	// Get the raw value
	rawValue := entry.Value()

	// Parse as JSON and traverse the path using shared traverser
	var jsonObj interface{}
	if err := json.Unmarshal(rawValue, &jsonObj); err != nil {
		kv.logger.Warn("failed to parse JSON from KV value", 
			"bucket", bucket, 
			"key", key, 
			"error", err,
			"impact", "KV lookup will fail")
		return nil, false
	}

	value, err := kv.traverser.TraversePath(jsonObj, jsonPath)
	if err != nil {
		kv.logger.Warn("JSON path traversal failed on NATS value", 
			"bucket", bucket, 
			"key", key, 
			"jsonPath", jsonPath, 
			"error", err,
			"impact", "KV lookup will fail")
		return nil, false
	}

	kv.logger.Debug("NATS KV lookup with JSON path successful", 
		"bucket", bucket, 
		"key", key, 
		"jsonPath", jsonPath, 
		"value", value)
	return value, true
}

// parseKVFieldWithPath parses a KV field specification with mandatory colon delimiter
// Format: "@kv.bucket.key:json.path.to.field"
// The colon (:) separates the key from the JSON path
// This eliminates ambiguity since NATS allows dots in key names
// 
// FIXED: Now uses SplitPathRespectingBraces for JSON path to handle variables like {sensor.choice}
//
// Examples:
//   @kv.customer_data.cust-123:tier
//   @kv.device_config.sensor.temp.001:thresholds.max  (key contains dots!)
//   @kv.device_config.{sensor.id}:{sensor.choice}      (variables with dots!)
//
// Returns bucket, key, jsonPath (as slice), and error
func (kv *KVContext) parseKVFieldWithPath(field string) (bucket, key string, jsonPath []string, err error) {
	// Field must start with "@kv."
	if !strings.HasPrefix(field, "@kv.") {
		return "", "", nil, fmt.Errorf("KV field must start with '@kv.', got: %s", field)
	}

	// Remove "@kv." prefix
	remainder := field[4:]
	
	// Check for colon delimiter (REQUIRED)
	if !strings.Contains(remainder, ":") {
		return "", "", nil, fmt.Errorf("KV field must use ':' to separate key from JSON path (format: @kv.bucket.key:path), got: %s", field)
	}
	
	// Check for multiple colons (invalid)
	if strings.Count(remainder, ":") > 1 {
		return "", "", nil, fmt.Errorf("KV field must contain exactly one ':' delimiter, got: %s", field)
	}
	
	// Split on first colon
	colonIndex := strings.Index(remainder, ":")
	
	// Left side of colon: bucket.key
	bucketKeyPart := remainder[:colonIndex]
	
	// Right side of colon: json.path (REQUIRED - must not be empty)
	jsonPathPart := remainder[colonIndex+1:]
	if jsonPathPart == "" {
		return "", "", nil, fmt.Errorf("JSON path after ':' cannot be empty in field: %s (format: @kv.bucket.key:path)", field)
	}
	
	// Parse bucket.key (must have exactly one dot between them)
	bucketKeyParts := strings.SplitN(bucketKeyPart, ".", 2)
	if len(bucketKeyParts) != 2 {
		return "", "", nil, fmt.Errorf("KV field must have 'bucket.key' before ':', got: %s", bucketKeyPart)
	}
	
	bucket = bucketKeyParts[0]
	key = bucketKeyParts[1]
	
	// Validate bucket and key are not empty
	if bucket == "" {
		return "", "", nil, fmt.Errorf("KV bucket name cannot be empty in field: %s", field)
	}
	if key == "" {
		return "", "", nil, fmt.Errorf("KV key name cannot be empty in field: %s", field)
	}
	
	// FIXED: Parse JSON path using brace-aware splitter
	// This allows variables like {sensor.choice} to be treated as single tokens
	jsonPath, err = SplitPathRespectingBraces(jsonPathPart)
	if err != nil {
		return "", "", nil, fmt.Errorf("invalid JSON path syntax in field %s: %w", field, err)
	}
	
	kv.logger.Debug("parsed KV field with colon delimiter",
		"field", field,
		"bucket", bucket,
		"key", key,
		"jsonPath", jsonPath)

	return bucket, key, jsonPath, nil
}

// resolveVariablesEnhanced replaces {variable} placeholders with better error handling
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

		// Mark as unresolved instead of returning original
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

	// Regular message field - use shared traverser for consistent behavior
	path := strings.Split(varName, ".")
	value, err := kv.traverser.TraversePath(msgData, path)
	if err != nil {
		return nil, false
	}
	return value, true
}

// convertToString with better handling of edge cases
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
		return "" // Return empty string for nil, not "null"
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
	stats := map[string]interface{}{
		"bucket_count":         len(kv.stores),
		"bucket_names":         kv.getBucketNames(),
		"initialized":          true,
		"syntax":               "colon_delimiter",
		"json_path_support":    true,
		"array_access_support": true,
		"variable_resolution":  "enhanced",
	}

	// Add local cache stats if available
	if kv.localCache != nil {
		stats["local_cache"] = kv.localCache.GetStats()
	} else {
		stats["local_cache"] = "disabled"
	}

	return stats
}
