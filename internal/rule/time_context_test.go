package rule

import (
	"testing"
	"time"
)

// TestTimeContext_AllFields tests that all time fields are correctly populated
func TestTimeContext_AllFields(t *testing.T) {
	// Friday, March 15, 2024 at 14:30:45 UTC
	fixedTime := time.Date(2024, 3, 15, 14, 30, 45, 0, time.UTC)
	provider := NewMockTimeProvider(fixedTime)
	ctx := provider.CurrentContext()

	tests := []struct {
		field string
		want  any
	}{
		// Time fields
		{"@time.hour", 14},
		{"@time.minute", 30},

		// Day fields
		{"@day.name", "friday"},
		{"@day.number", 5}, // Friday is 5 (Monday=1, Sunday=7)

		// Date fields
		{"@date.year", 2024},
		{"@date.month", 3},
		{"@date.day", 15},
		{"@date.iso", "2024-03-15"},

		// Timestamp fields
		{"@timestamp.unix", fixedTime.Unix()}, // Calculated from the time itself
		{"@timestamp.iso", "2024-03-15T14:30:45Z"},
	}

	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			got, exists := ctx.Field(tt.field)
			if !exists {
				t.Fatalf("Field(%s) field not found", tt.field)
			}
			if got != tt.want {
				t.Errorf("Field(%s) = %v (%T), want %v (%T)",
					tt.field, got, got, tt.want, tt.want)
			}
		})
	}
}

// TestTimeContext_BusinessHours tests business hours detection
func TestTimeContext_BusinessHours(t *testing.T) {
	tests := []struct {
		name           string
		time           time.Time
		isBusinessHour bool
		isWeekday      bool
	}{
		{
			name:           "Monday 9am - business hours",
			time:           time.Date(2024, 3, 18, 9, 0, 0, 0, time.UTC), // Monday
			isBusinessHour: true,
			isWeekday:      true,
		},
		{
			name:           "Monday 8:59am - before business hours",
			time:           time.Date(2024, 3, 18, 8, 59, 0, 0, time.UTC),
			isBusinessHour: false,
			isWeekday:      true,
		},
		{
			name:           "Monday 5pm - end of business hours",
			time:           time.Date(2024, 3, 18, 17, 0, 0, 0, time.UTC),
			isBusinessHour: false, // 5pm is typically end of business
			isWeekday:      true,
		},
		{
			name:           "Friday 3pm - business hours",
			time:           time.Date(2024, 3, 22, 15, 0, 0, 0, time.UTC), // Friday
			isBusinessHour: true,
			isWeekday:      true,
		},
		{
			name:           "Saturday 10am - weekend",
			time:           time.Date(2024, 3, 23, 10, 0, 0, 0, time.UTC), // Saturday
			isBusinessHour: false,
			isWeekday:      false,
		},
		{
			name:           "Sunday 2pm - weekend",
			time:           time.Date(2024, 3, 24, 14, 0, 0, 0, time.UTC), // Sunday
			isBusinessHour: false,
			isWeekday:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewMockTimeProvider(tt.time)
			ctx := provider.CurrentContext()

			hour, _ := ctx.Field("@time.hour")
			dayNum, _ := ctx.Field("@day.number")

			// Business hours: Monday-Friday (1-5), 9am-5pm (9-16)
			gotBusinessHour := dayNum.(int) >= 1 && dayNum.(int) <= 5 &&
				hour.(int) >= 9 && hour.(int) < 17

			if gotBusinessHour != tt.isBusinessHour {
				t.Errorf("Business hours check = %v, want %v (hour=%v, day=%v)",
					gotBusinessHour, tt.isBusinessHour, hour, dayNum)
			}

			// Weekday: Monday-Friday (1-5)
			gotWeekday := dayNum.(int) >= 1 && dayNum.(int) <= 5
			if gotWeekday != tt.isWeekday {
				t.Errorf("Weekday check = %v, want %v (day=%v)",
					gotWeekday, tt.isWeekday, dayNum)
			}
		})
	}
}

// TestTimeContext_DayTransitions tests midnight and day boundaries
func TestTimeContext_DayTransitions(t *testing.T) {
	tests := []struct {
		name      string
		time      time.Time
		wantHour  int
		wantDay   string
		wantMonth int
	}{
		{
			name:      "midnight",
			time:      time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC),
			wantHour:  0,
			wantDay:   "friday",
			wantMonth: 3,
		},
		{
			name:      "one minute before midnight",
			time:      time.Date(2024, 3, 15, 23, 59, 0, 0, time.UTC),
			wantHour:  23,
			wantDay:   "friday",
			wantMonth: 3,
		},
		{
			name:      "month transition",
			time:      time.Date(2024, 3, 31, 23, 59, 0, 0, time.UTC),
			wantHour:  23,
			wantDay:   "sunday",
			wantMonth: 3,
		},
		{
			name:      "new month",
			time:      time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC),
			wantHour:  0,
			wantDay:   "monday",
			wantMonth: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewMockTimeProvider(tt.time)
			ctx := provider.CurrentContext()

			hour, _ := ctx.Field("@time.hour")
			if hour != tt.wantHour {
				t.Errorf("hour = %v, want %v", hour, tt.wantHour)
			}

			dayName, _ := ctx.Field("@day.name")
			if dayName != tt.wantDay {
				t.Errorf("day.name = %v, want %v", dayName, tt.wantDay)
			}

			month, _ := ctx.Field("@date.month")
			if month != tt.wantMonth {
				t.Errorf("month = %v, want %v", month, tt.wantMonth)
			}
		})
	}
}

// TestTimeContext_SundayHandling tests that Sunday is correctly mapped to 7
func TestTimeContext_SundayHandling(t *testing.T) {
	// Sunday in Go's time.Weekday is 0, but we map it to 7
	sundayTime := time.Date(2024, 3, 17, 12, 0, 0, 0, time.UTC) // Sunday
	provider := NewMockTimeProvider(sundayTime)
	ctx := provider.CurrentContext()

	dayNum, exists := ctx.Field("@day.number")
	if !exists {
		t.Fatal("@day.number not found")
	}

	if dayNum != 7 {
		t.Errorf("Sunday day.number = %v, want 7 (Go weekday=%v)",
			dayNum, sundayTime.Weekday())
	}

	dayName, _ := ctx.Field("@day.name")
	if dayName != "sunday" {
		t.Errorf("Sunday day.name = %v, want sunday", dayName)
	}
}

// TestTimeContext_LeapYear tests leap year date handling
func TestTimeContext_LeapYear(t *testing.T) {
	// 2024 is a leap year - Feb 29 exists
	leapDay := time.Date(2024, 2, 29, 12, 0, 0, 0, time.UTC)
	provider := NewMockTimeProvider(leapDay)
	ctx := provider.CurrentContext()

	day, _ := ctx.Field("@date.day")
	if day != 29 {
		t.Errorf("Leap day = %v, want 29", day)
	}

	month, _ := ctx.Field("@date.month")
	if month != 2 {
		t.Errorf("Leap month = %v, want 2 (February)", month)
	}

	iso, _ := ctx.Field("@date.iso")
	if iso != "2024-02-29" {
		t.Errorf("Leap day ISO = %v, want 2024-02-29", iso)
	}
}

// TestTimeContext_InvalidField tests handling of unknown fields
func TestTimeContext_InvalidField(t *testing.T) {
	provider := NewSystemTimeProvider()
	ctx := provider.CurrentContext()

	invalidFields := []string{
		"@time.second", // Not exposed
		"@time.invalid",
		"@day.invalid",
		"@date.invalid",
		"@notafield",
		"",
	}

	for _, field := range invalidFields {
		t.Run(field, func(t *testing.T) {
			_, exists := ctx.Field(field)
			if exists {
				t.Errorf("Field(%s) should not exist", field)
			}
		})
	}
}

// TestTimeContext_GetAllFieldNames tests field enumeration
func TestTimeContext_GetAllFieldNames(t *testing.T) {
	provider := NewSystemTimeProvider()
	ctx := provider.CurrentContext()

	fieldNames := ctx.FieldNames()

	expectedFields := []string{
		"@time.hour",
		"@time.minute",
		"@day.name",
		"@day.number",
		"@date.year",
		"@date.month",
		"@date.day",
		"@date.iso",
		"@timestamp.unix",
		"@timestamp.iso",
	}

	// Check all expected fields are present
	fieldMap := make(map[string]bool)
	for _, name := range fieldNames {
		fieldMap[name] = true
	}

	for _, expected := range expectedFields {
		if !fieldMap[expected] {
			t.Errorf("Expected field %s not found in FieldNames()", expected)
		}
	}

	// Verify we have the right count (no extra fields)
	if len(fieldNames) != len(expectedFields) {
		t.Errorf("FieldNames() returned %d fields, expected %d",
			len(fieldNames), len(expectedFields))
	}
}

// TestTimeContext_ISOFormats tests ISO timestamp formats
func TestTimeContext_ISOFormats(t *testing.T) {
	tests := []struct {
		name     string
		time     time.Time
		wantISO  string
		wantDate string
	}{
		{
			name:     "standard datetime",
			time:     time.Date(2024, 3, 15, 14, 30, 45, 0, time.UTC),
			wantISO:  "2024-03-15T14:30:45Z",
			wantDate: "2024-03-15",
		},
		{
			name:     "midnight",
			time:     time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			wantISO:  "2024-01-01T00:00:00Z",
			wantDate: "2024-01-01",
		},
		{
			name:     "single digit month and day",
			time:     time.Date(2024, 1, 5, 9, 5, 3, 0, time.UTC),
			wantISO:  "2024-01-05T09:05:03Z",
			wantDate: "2024-01-05",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewMockTimeProvider(tt.time)
			ctx := provider.CurrentContext()

			iso, _ := ctx.Field("@timestamp.iso")
			if iso != tt.wantISO {
				t.Errorf("timestamp.iso = %v, want %v", iso, tt.wantISO)
			}

			date, _ := ctx.Field("@date.iso")
			if date != tt.wantDate {
				t.Errorf("date.iso = %v, want %v", date, tt.wantDate)
			}
		})
	}
}

// TestTimeContext_DayNames tests all day name mappings
func TestTimeContext_DayNames(t *testing.T) {
	// Start with Monday, March 18, 2024
	tests := []struct {
		dayOffset int
		wantName  string
		wantNum   int
	}{
		{0, "monday", 1},
		{1, "tuesday", 2},
		{2, "wednesday", 3},
		{3, "thursday", 4},
		{4, "friday", 5},
		{5, "saturday", 6},
		{6, "sunday", 7},
	}

	baseTime := time.Date(2024, 3, 18, 12, 0, 0, 0, time.UTC) // Monday

	for _, tt := range tests {
		t.Run(tt.wantName, func(t *testing.T) {
			testTime := baseTime.AddDate(0, 0, tt.dayOffset)
			provider := NewMockTimeProvider(testTime)
			ctx := provider.CurrentContext()

			dayName, _ := ctx.Field("@day.name")
			if dayName != tt.wantName {
				t.Errorf("day.name = %v, want %v", dayName, tt.wantName)
			}

			dayNum, _ := ctx.Field("@day.number")
			if dayNum != tt.wantNum {
				t.Errorf("day.number = %v, want %v", dayNum, tt.wantNum)
			}
		})
	}
}

// TestSystemTimeProvider tests the real system time provider
func TestSystemTimeProvider(t *testing.T) {
	provider := NewSystemTimeProvider()

	// Get context twice - they should both work
	ctx1 := provider.CurrentContext()
	ctx2 := provider.CurrentContext()

	// Verify all expected fields exist in both contexts
	expectedFields := []string{
		"@time.hour",
		"@day.name",
		"@date.year",
		"@timestamp.iso",
	}

	for _, field := range expectedFields {
		if _, exists := ctx1.Field(field); !exists {
			t.Errorf("ctx1 missing field: %s", field)
		}
		if _, exists := ctx2.Field(field); !exists {
			t.Errorf("ctx2 missing field: %s", field)
		}
	}

	// Verify timestamps are reasonable (not zero, not far in future)
	ts1, _ := ctx1.Field("@timestamp.unix")
	unix1 := ts1.(int64)

	// Timestamp should be after 2024-01-01 (1704067200) and before 2030-01-01 (1893456000)
	if unix1 < 1704067200 || unix1 > 1893456000 {
		t.Errorf("Timestamp out of reasonable range: %v", unix1)
	}

	// Verify ISO format is parseable
	iso1, _ := ctx1.Field("@timestamp.iso")
	_, err := time.Parse(time.RFC3339, iso1.(string))
	if err != nil {
		t.Errorf("Invalid ISO timestamp format: %v, error: %v", iso1, err)
	}
}

// TestMockTimeProvider tests the mock provider behavior
func TestMockTimeProvider(t *testing.T) {
	fixedTime := time.Date(2024, 3, 15, 14, 30, 0, 0, time.UTC)
	mock := NewMockTimeProvider(fixedTime)

	// Multiple calls should return same time
	ctx1 := mock.CurrentContext()
	ctx2 := mock.CurrentContext()

	ts1, _ := ctx1.Field("@timestamp.unix")
	ts2, _ := ctx2.Field("@timestamp.unix")

	if ts1 != ts2 {
		t.Errorf("Mock time changing: ts1=%v, ts2=%v", ts1, ts2)
	}

	// SetTime should update the time
	newTime := time.Date(2024, 3, 16, 10, 0, 0, 0, time.UTC)
	mock.SetTime(newTime)
	ctx3 := mock.CurrentContext()

	hour3, _ := ctx3.Field("@time.hour")
	if hour3 != 10 {
		t.Errorf("After SetTime, hour = %v, want 10", hour3)
	}

	day3, _ := ctx3.Field("@date.day")
	if day3 != 16 {
		t.Errorf("After SetTime, day = %v, want 16", day3)
	}
}

// BenchmarkTimeContext_GetField benchmarks field lookup performance
func BenchmarkTimeContext_GetField(b *testing.B) {
	provider := NewSystemTimeProvider()
	ctx := provider.CurrentContext()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx.Field("@time.hour")
	}
}

// BenchmarkTimeContext_MultipleFields benchmarks multiple field lookups
func BenchmarkTimeContext_MultipleFields(b *testing.B) {
	provider := NewSystemTimeProvider()
	ctx := provider.CurrentContext()

	fields := []string{
		"@time.hour",
		"@time.minute",
		"@day.name",
		"@date.iso",
		"@timestamp.unix",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, field := range fields {
			ctx.Field(field)
		}
	}
}

// BenchmarkTimeContext_Creation benchmarks context creation
func BenchmarkTimeContext_Creation(b *testing.B) {
	provider := NewSystemTimeProvider()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		provider.CurrentContext()
	}
}

// BenchmarkTimeContext_GetAllFieldNames benchmarks field enumeration
func BenchmarkTimeContext_GetAllFieldNames(b *testing.B) {
	provider := NewSystemTimeProvider()
	ctx := provider.CurrentContext()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx.FieldNames()
	}
}
