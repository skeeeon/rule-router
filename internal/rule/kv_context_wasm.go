// file: internal/rule/kv_context_wasm.go
// WASM-compatible KV context — local cache only, no NATS/jetstream dependency.

//go:build js && wasm

package rule

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	json "github.com/goccy/go-json"
	"rule-router/internal/logger"
)

var (
	kvVariablePattern = regexp.MustCompile(`\{([^}]+)\}`)
)

type parsedKVField struct {
	bucket   string
	key      string
	jsonPath []string
}

// KVContext provides access to KV data for rule evaluation.
// In WASM builds, only local cache is supported (no NATS KV stores).
type KVContext struct {
	logger     *logger.Logger
	localCache *LocalKVCache
	traverser  *JSONPathTraverser
	parseCache map[string]*parsedKVField
}

// NewKVContext creates a KV context backed by local cache only.
// The stores parameter is accepted for API compatibility but ignored in WASM.
func NewKVContext(stores interface{}, logger *logger.Logger, localCache *LocalKVCache) *KVContext {
	if logger == nil {
		panic("KVContext requires a logger")
	}

	kvCtx := &KVContext{
		logger:     logger.With("component", "kv"),
		localCache: localCache,
		traverser:  defaultTraverser,
		parseCache: make(map[string]*parsedKVField),
	}

	cacheStatus := "disabled"
	if localCache != nil && localCache.IsEnabled() {
		cacheStatus = "enabled"
	}

	logger.Info("KV context initialized (WASM, cache-only)",
		"localCache", cacheStatus,
		"syntax", "@kv.bucket.key[:json.path]")
	return kvCtx
}

func (kv *KVContext) GetField(field string) (interface{}, bool) {
	bucket, key, jsonPath, err := kv.parseKVField(field)
	if err != nil {
		kv.logger.Debug("invalid KV field format", "field", field, "error", err)
		return nil, false
	}

	if kv.localCache != nil && kv.localCache.IsEnabled() {
		if cachedValue, found := kv.localCache.Get(bucket, key); found {
			if len(jsonPath) == 0 {
				return cachedValue, true
			}
			finalValue, err := kv.traverser.TraversePath(cachedValue, jsonPath)
			if err != nil {
				return nil, false
			}
			return finalValue, true
		}
	}

	return nil, false
}

func (kv *KVContext) GetFieldWithContext(field string, msgData map[string]interface{}, timeCtx *TimeContext, subjectCtx *SubjectContext) (interface{}, bool) {
	resolvedField, hasUnresolvedVars, err := kv.resolveVariablesEnhanced(field, msgData, timeCtx, subjectCtx)
	if err != nil {
		return "", false
	}
	if hasUnresolvedVars {
		return "", false
	}

	value, found := kv.GetField(resolvedField)
	if !found {
		return "", false
	}
	return value, true
}

func (kv *KVContext) parseKVField(field string) (bucket, key string, jsonPath []string, err error) {
	if cached, ok := kv.parseCache[field]; ok {
		return cached.bucket, cached.key, cached.jsonPath, nil
	}

	if !strings.HasPrefix(field, "@kv.") {
		return "", "", nil, fmt.Errorf("KV field must start with '@kv.', got: %s", field)
	}

	remainder := field[4:]

	if !strings.Contains(remainder, ":") {
		bucketKeyParts := strings.SplitN(remainder, ".", 2)
		if len(bucketKeyParts) != 2 {
			return "", "", nil, fmt.Errorf("KV field without path must have 'bucket.key' format, got: %s", remainder)
		}
		bucket = bucketKeyParts[0]
		key = bucketKeyParts[1]
		if bucket == "" || key == "" {
			return "", "", nil, fmt.Errorf("KV bucket and key cannot be empty in field: %s", field)
		}
		kv.parseCache[field] = &parsedKVField{bucket: bucket, key: key, jsonPath: []string{}}
		return bucket, key, []string{}, nil
	}

	if strings.Count(remainder, ":") > 1 {
		return "", "", nil, fmt.Errorf("KV field must contain at most one ':' delimiter, got: %s", field)
	}

	colonIndex := strings.Index(remainder, ":")
	bucketKeyPart := remainder[:colonIndex]
	jsonPathPart := remainder[colonIndex+1:]

	bucketKeyParts := strings.SplitN(bucketKeyPart, ".", 2)
	if len(bucketKeyParts) != 2 {
		return "", "", nil, fmt.Errorf("KV field must have 'bucket.key' before ':', got: %s", bucketKeyPart)
	}

	bucket = bucketKeyParts[0]
	key = bucketKeyParts[1]
	if bucket == "" || key == "" {
		return "", "", nil, fmt.Errorf("KV bucket and key cannot be empty in field: %s", field)
	}

	if jsonPathPart == "" {
		kv.parseCache[field] = &parsedKVField{bucket: bucket, key: key, jsonPath: []string{}}
		return bucket, key, []string{}, nil
	}

	jsonPath, err = SplitPathRespectingBraces(jsonPathPart)
	if err != nil {
		return "", "", nil, fmt.Errorf("invalid JSON path syntax in field %s: %w", field, err)
	}

	kv.parseCache[field] = &parsedKVField{bucket: bucket, key: key, jsonPath: jsonPath}
	return bucket, key, jsonPath, nil
}

func (kv *KVContext) resolveVariablesEnhanced(field string, msgData map[string]interface{}, timeCtx *TimeContext, subjectCtx *SubjectContext) (string, bool, error) {
	if !strings.Contains(field, "{") {
		return field, false, nil
	}

	result := field
	hasUnresolvedVars := false

	result = kvVariablePattern.ReplaceAllStringFunc(result, func(match string) string {
		varName := match[1 : len(match)-1]
		if value, found := kv.resolveVariable(varName, msgData, timeCtx, subjectCtx); found {
			return kv.convertToString(value)
		}
		hasUnresolvedVars = true
		return ""
	})

	return result, hasUnresolvedVars, nil
}

func (kv *KVContext) resolveVariable(varName string, msgData map[string]interface{}, timeCtx *TimeContext, subjectCtx *SubjectContext) (interface{}, bool) {
	if strings.HasPrefix(varName, "@") {
		if strings.HasPrefix(varName, "@subject") {
			if subjectCtx == nil {
				return nil, false
			}
			return subjectCtx.GetField(varName)
		}
		if strings.HasPrefix(varName, "@time") || strings.HasPrefix(varName, "@date") || strings.HasPrefix(varName, "@timestamp") {
			if timeCtx == nil {
				return nil, false
			}
			return timeCtx.GetField(varName)
		}
		return nil, false
	}

	path := strings.Split(varName, ".")
	value, err := kv.traverser.TraversePath(msgData, path)
	if err != nil {
		return nil, false
	}
	return value, true
}

func (kv *KVContext) convertToString(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		return strconv.FormatBool(v)
	case nil:
		return ""
	default:
		if jsonBytes, err := json.Marshal(v); err == nil {
			return string(jsonBytes)
		}
		return fmt.Sprintf("%v", v)
	}
}

func (kv *KVContext) GetAllBuckets() []string {
	return []string{}
}

func (kv *KVContext) HasBucket(bucketName string) bool {
	if kv.localCache != nil && kv.localCache.IsEnabled() {
		_, found := kv.localCache.Get(bucketName, "")
		return found
	}
	return false
}

func (kv *KVContext) GetStats() map[string]interface{} {
	stats := map[string]interface{}{
		"bucket_count": 0,
		"initialized":  true,
		"mode":         "wasm-cache-only",
	}
	if kv.localCache != nil {
		stats["local_cache"] = kv.localCache.GetStats()
	}
	return stats
}
