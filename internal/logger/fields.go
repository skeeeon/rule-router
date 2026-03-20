// file: internal/logger/fields.go

package logger

// Standard field key constants for structured logging.
// Use these to ensure consistent field naming across the codebase.
const (
	KeyError     = "error"
	KeyComponent = "component"
	KeySubject   = "subject"
	KeyURL       = "url"
	KeyBucket    = "bucket"
	KeyDuration  = "duration"
)
