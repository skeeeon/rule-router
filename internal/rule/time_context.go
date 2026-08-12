//file: internal/rule/time_context.go

package rule

import (
	"strings"
	"time"
)

// TimeContext holds current time information and pre-computed fields
type TimeContext struct {
	timestamp time.Time
	fields    map[string]any
}

// TimeProvider interface for creating time contexts (enables mocking)
type TimeProvider interface {
	CurrentContext() *TimeContext
	ContextAt(time.Time) *TimeContext
}

// SystemTimeProvider uses system time
type SystemTimeProvider struct{}

// NewSystemTimeProvider creates a new system time provider
func NewSystemTimeProvider() *SystemTimeProvider {
	return &SystemTimeProvider{}
}

// CurrentContext returns time context for current moment
func (p *SystemTimeProvider) CurrentContext() *TimeContext {
	return p.ContextAt(time.Now())
}

// ContextAt returns time context for specific time (useful for testing)
func (p *SystemTimeProvider) ContextAt(t time.Time) *TimeContext {
	ctx := &TimeContext{
		timestamp: t,
		fields:    make(map[string]any),
	}

	// Pre-compute all time fields for efficient lookup
	ctx.fields["@time.hour"] = t.Hour()
	ctx.fields["@time.minute"] = t.Minute()
	ctx.fields["@day.name"] = strings.ToLower(t.Weekday().String())
	ctx.fields["@day.number"] = int(t.Weekday())
	if ctx.fields["@day.number"] == 0 { // Sunday is 0 in Go, make it 7
		ctx.fields["@day.number"] = 7
	}
	ctx.fields["@date.year"] = t.Year()
	ctx.fields["@date.month"] = int(t.Month())
	ctx.fields["@date.day"] = t.Day()
	ctx.fields["@date.iso"] = t.Format("2006-01-02")
	ctx.fields["@timestamp.unix"] = t.Unix()
	ctx.fields["@timestamp.iso"] = t.Format(time.RFC3339)

	return ctx
}

// Field safely retrieves a time field value
func (tc *TimeContext) Field(fieldName string) (any, bool) {
	value, exists := tc.fields[fieldName]
	return value, exists
}

// FieldNames returns list of all available time field names
func (tc *TimeContext) FieldNames() []string {
	names := make([]string, 0, len(tc.fields))
	for name := range tc.fields {
		names = append(names, name)
	}
	return names
}

// MockTimeProvider for testing with fixed time
type MockTimeProvider struct {
	fixedTime time.Time
}

// NewMockTimeProvider creates a mock provider with fixed time
func NewMockTimeProvider(fixedTime time.Time) *MockTimeProvider {
	return &MockTimeProvider{fixedTime: fixedTime}
}

// CurrentContext returns context for the fixed time
func (m *MockTimeProvider) CurrentContext() *TimeContext {
	return m.ContextAt(m.fixedTime)
}

// ContextAt returns context for specified time
func (m *MockTimeProvider) ContextAt(t time.Time) *TimeContext {
	return NewSystemTimeProvider().ContextAt(t)
}

// SetTime updates the fixed time
func (m *MockTimeProvider) SetTime(t time.Time) {
	m.fixedTime = t
}
