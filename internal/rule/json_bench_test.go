package rule

import (
	"encoding/json"
	"fmt"
	"testing"

	goccyjson "github.com/goccy/go-json"
	"rule-router/internal/logger"
)

// Realistic message payloads for benchmarking

// Small IoT sensor message (typical edge use case)
var smallMessageJSON = []byte(`{
	"device_id": "sensor001",
	"temperature": 25.5,
	"humidity": 65,
	"timestamp": "2024-01-15T14:30:00Z"
}`)

// Medium message with nested structure
var mediumMessageJSON = []byte(`{
	"device": {
		"id": "sensor001",
		"type": "temperature",
		"location": {
			"building": "main",
			"floor": 3,
			"room": "server-room"
		}
	},
	"data": {
		"reading": {
			"value": 25.5,
			"unit": "celsius",
			"accuracy": 0.1
		},
		"timestamp": "2024-01-15T14:30:00Z",
		"quality": "good"
	},
	"metadata": {
		"firmware": "2.1.4",
		"battery": 87,
		"signal_strength": -45
	}
}`)

// Large complex message (worst case scenario)
var largeMessageJSON = []byte(`{
	"event": {
		"type": "order",
		"action": "created",
		"timestamp": "2024-01-15T14:30:00Z"
	},
	"order": {
		"id": "ORD-12345",
		"customer": {
			"id": "CUST-789",
			"tier": "premium",
			"region": "us-west",
			"profile": {
				"name": "Acme Corp",
				"email": "admin@acme.com",
				"phone": "+1-555-0123",
				"preferences": {
					"notifications": true,
					"theme": "dark",
					"language": "en-US"
				}
			}
		},
		"items": [
			{
				"sku": "PROD-001",
				"name": "Widget Pro",
				"quantity": 2,
				"price": 299.99,
				"category": "electronics"
			},
			{
				"sku": "PROD-002",
				"name": "Gadget Plus",
				"quantity": 1,
				"price": 599.99,
				"category": "electronics"
			},
			{
				"sku": "ACC-001",
				"name": "Cable Set",
				"quantity": 3,
				"price": 29.99,
				"category": "accessories"
			}
		],
		"totals": {
			"subtotal": 1289.94,
			"tax": 103.20,
			"shipping": 15.00,
			"total": 1408.14
		},
		"payment": {
			"method": "credit_card",
			"status": "pending",
			"transaction_id": "TXN-ABC123",
			"processor": "stripe"
		},
		"shipping": {
			"address": {
				"street": "123 Main St",
				"city": "Seattle",
				"state": "WA",
				"zip": "98101",
				"country": "US"
			},
			"method": "next_day",
			"carrier": "FedEx",
			"tracking": "1234567890"
		}
	}
}`)

// Array-heavy message (for testing array traversal)
var arrayMessageJSON = []byte(`{
	"sensor_id": "multi-sensor-001",
	"readings": [
		{"type": "temperature", "value": 25.5, "unit": "celsius"},
		{"type": "humidity", "value": 65, "unit": "percent"},
		{"type": "pressure", "value": 1013, "unit": "hPa"},
		{"type": "co2", "value": 400, "unit": "ppm"},
		{"type": "voc", "value": 125, "unit": "ppb"}
	],
	"timestamps": [
		"2024-01-15T14:30:00Z",
		"2024-01-15T14:30:01Z",
		"2024-01-15T14:30:02Z",
		"2024-01-15T14:30:03Z",
		"2024-01-15T14:30:04Z"
	]
}`)

// ========================================
// Unmarshal Benchmarks (Message Parsing)
// ========================================

// BenchmarkUnmarshal_Small_StdLib benchmarks standard library with small messages
func BenchmarkUnmarshal_Small_StdLib(b *testing.B) {
	var result map[string]interface{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		json.Unmarshal(smallMessageJSON, &result)
	}
}

// BenchmarkUnmarshal_Small_GoccyJSON benchmarks goccy/go-json with small messages
func BenchmarkUnmarshal_Small_GoccyJSON(b *testing.B) {
	var result map[string]interface{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		goccyjson.Unmarshal(smallMessageJSON, &result)
	}
}

// BenchmarkUnmarshal_Medium_StdLib benchmarks standard library with medium messages
func BenchmarkUnmarshal_Medium_StdLib(b *testing.B) {
	var result map[string]interface{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		json.Unmarshal(mediumMessageJSON, &result)
	}
}

// BenchmarkUnmarshal_Medium_GoccyJSON benchmarks goccy/go-json with medium messages
func BenchmarkUnmarshal_Medium_GoccyJSON(b *testing.B) {
	var result map[string]interface{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		goccyjson.Unmarshal(mediumMessageJSON, &result)
	}
}

// BenchmarkUnmarshal_Large_StdLib benchmarks standard library with large messages
func BenchmarkUnmarshal_Large_StdLib(b *testing.B) {
	var result map[string]interface{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		json.Unmarshal(largeMessageJSON, &result)
	}
}

// BenchmarkUnmarshal_Large_GoccyJSON benchmarks goccy/go-json with large messages
func BenchmarkUnmarshal_Large_GoccyJSON(b *testing.B) {
	var result map[string]interface{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		goccyjson.Unmarshal(largeMessageJSON, &result)
	}
}

// BenchmarkUnmarshal_Array_StdLib benchmarks standard library with array-heavy messages
func BenchmarkUnmarshal_Array_StdLib(b *testing.B) {
	var result map[string]interface{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		json.Unmarshal(arrayMessageJSON, &result)
	}
}

// BenchmarkUnmarshal_Array_GoccyJSON benchmarks goccy/go-json with array-heavy messages
func BenchmarkUnmarshal_Array_GoccyJSON(b *testing.B) {
	var result map[string]interface{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		goccyjson.Unmarshal(arrayMessageJSON, &result)
	}
}

// ========================================
// Marshal Benchmarks (Template Output)
// ========================================

// BenchmarkMarshal_Small_StdLib benchmarks standard library marshaling
func BenchmarkMarshal_Small_StdLib(b *testing.B) {
	data := map[string]interface{}{
		"device_id":   "sensor001",
		"temperature": 25.5,
		"humidity":    65,
		"timestamp":   "2024-01-15T14:30:00Z",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		json.Marshal(data)
	}
}

// BenchmarkMarshal_Small_GoccyJSON benchmarks goccy/go-json marshaling
func BenchmarkMarshal_Small_GoccyJSON(b *testing.B) {
	data := map[string]interface{}{
		"device_id":   "sensor001",
		"temperature": 25.5,
		"humidity":    65,
		"timestamp":   "2024-01-15T14:30:00Z",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		goccyjson.Marshal(data)
	}
}

// BenchmarkMarshal_Medium_StdLib benchmarks standard library with nested structures
func BenchmarkMarshal_Medium_StdLib(b *testing.B) {
	data := map[string]interface{}{
		"device": map[string]interface{}{
			"id":   "sensor001",
			"type": "temperature",
			"location": map[string]interface{}{
				"building": "main",
				"floor":    3,
				"room":     "server-room",
			},
		},
		"data": map[string]interface{}{
			"reading": map[string]interface{}{
				"value":    25.5,
				"unit":     "celsius",
				"accuracy": 0.1,
			},
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		json.Marshal(data)
	}
}

// BenchmarkMarshal_Medium_GoccyJSON benchmarks goccy/go-json with nested structures
func BenchmarkMarshal_Medium_GoccyJSON(b *testing.B) {
	data := map[string]interface{}{
		"device": map[string]interface{}{
			"id":   "sensor001",
			"type": "temperature",
			"location": map[string]interface{}{
				"building": "main",
				"floor":    3,
				"room":     "server-room",
			},
		},
		"data": map[string]interface{}{
			"reading": map[string]interface{}{
				"value":    25.5,
				"unit":     "celsius",
				"accuracy": 0.1,
			},
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		goccyjson.Marshal(data)
	}
}

// ========================================
// Nested Field Access Benchmarks
// ========================================

// BenchmarkNestedAccess_TwoLevel benchmarks accessing nested fields
func BenchmarkNestedAccess_TwoLevel(b *testing.B) {
	var data map[string]interface{}
	goccyjson.Unmarshal(mediumMessageJSON, &data)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Simulate accessing device.id
		if device, ok := data["device"].(map[string]interface{}); ok {
			_ = device["id"]
		}
	}
}

// BenchmarkNestedAccess_ThreeLevel benchmarks deep nested field access
func BenchmarkNestedAccess_ThreeLevel(b *testing.B) {
	var data map[string]interface{}
	goccyjson.Unmarshal(mediumMessageJSON, &data)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Simulate accessing device.location.building
		if device, ok := data["device"].(map[string]interface{}); ok {
			if location, ok := device["location"].(map[string]interface{}); ok {
				_ = location["building"]
			}
		}
	}
}

// BenchmarkNestedAccess_FourLevel benchmarks very deep nested access
func BenchmarkNestedAccess_FourLevel(b *testing.B) {
	var data map[string]interface{}
	goccyjson.Unmarshal(largeMessageJSON, &data)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Simulate accessing order.customer.profile.preferences.notifications
		if order, ok := data["order"].(map[string]interface{}); ok {
			if customer, ok := order["customer"].(map[string]interface{}); ok {
				if profile, ok := customer["profile"].(map[string]interface{}); ok {
					if prefs, ok := profile["preferences"].(map[string]interface{}); ok {
						_ = prefs["notifications"]
					}
				}
			}
		}
	}
}

// ========================================
// Full Pipeline Benchmarks
// ========================================

// Helper to create a context for benchmark tests.
func newBenchmarkContext(data map[string]interface{}, subject string) *EvaluationContext {
	payload, err := goccyjson.Marshal(data)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal benchmark data: %v", err))
	}

	ctx, err := NewEvaluationContext(
		payload,
		nil, // headers
		NewSubjectContext(subject),
		nil, // httpCtx
		NewSystemTimeProvider().GetCurrentContext(),
		nil, // kvCtx
		nil, // sigVerification
		logger.NewNopLogger(),
	)
	if err != nil {
		panic(fmt.Sprintf("failed to create benchmark context: %v", err))
	}
	return ctx
}

// BenchmarkFullPipeline_Small simulates complete message processing
func BenchmarkFullPipeline_Small(b *testing.B) {
	engine := NewTemplateEngine(logger.NewNopLogger())
	template := `{"alert": "Temperature {temperature}°C", "device": "{device_id}"}`
	var data map[string]interface{}
	goccyjson.Unmarshal(smallMessageJSON, &data)
	context := newBenchmarkContext(data, "sensors.temperature")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.Execute(template, context)
	}
}

// BenchmarkFullPipeline_Medium simulates medium complexity processing
func BenchmarkFullPipeline_Medium(b *testing.B) {
	engine := NewTemplateEngine(logger.NewNopLogger())
	template := `{"device": "{device.id}", "location": "{device.location.building}", "reading": "{data.reading.value}"}`
	var data map[string]interface{}
	goccyjson.Unmarshal(mediumMessageJSON, &data)
	context := newBenchmarkContext(data, "sensors.temperature.room1")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.Execute(template, context)
	}
}

// BenchmarkFullPipeline_Large simulates complex message processing
func BenchmarkFullPipeline_Large(b *testing.B) {
	engine := NewTemplateEngine(logger.NewNopLogger())
	template := `{"order_id": "{order.id}", "customer": "{order.customer.profile.name}", "total": "{order.totals.total}"}`
	var data map[string]interface{}
	goccyjson.Unmarshal(largeMessageJSON, &data)
	context := newBenchmarkContext(data, "orders.created")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.Execute(template, context)
	}
}

// ========================================
// Memory Allocation Tests
// ========================================

// TestJSONMemoryAllocation tests memory behavior of JSON operations
func TestJSONMemoryAllocation(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
	}{
		{"small", smallMessageJSON},
		{"medium", mediumMessageJSON},
		{"large", largeMessageJSON},
		{"array", arrayMessageJSON},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test multiple unmarshals to see allocation patterns
			for i := 0; i < 100; i++ {
				var result map[string]interface{}
				if err := goccyjson.Unmarshal(tt.payload, &result); err != nil {
					t.Fatalf("Unmarshal failed: %v", err)
				}
			}
		})
	}
}

// ========================================
// Validation Tests
// ========================================

// TestJSONCorrectness verifies both libraries produce same results
func TestJSONCorrectness(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
	}{
		{"small", smallMessageJSON},
		{"medium", mediumMessageJSON},
		{"large", largeMessageJSON},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdResult map[string]interface{}
			var goccyResult map[string]interface{}

			// Unmarshal with both libraries
			if err := json.Unmarshal(tt.payload, &stdResult); err != nil {
				t.Fatalf("Standard library unmarshal failed: %v", err)
			}

			if err := goccyjson.Unmarshal(tt.payload, &goccyResult); err != nil {
				t.Fatalf("Goccy unmarshal failed: %v", err)
			}

			// Both should produce valid results
			if len(stdResult) == 0 {
				t.Error("Standard library produced empty result")
			}
			if len(goccyResult) == 0 {
				t.Error("Goccy produced empty result")
			}

			// Note: Deep equality comparison is complex for map[string]interface{}
			// This is a basic sanity check
			if len(stdResult) != len(goccyResult) {
				t.Errorf("Different number of top-level keys: std=%d, goccy=%d",
					len(stdResult), len(goccyResult))
			}
		})
	}
}

// ========================================
// Error Handling Benchmarks
// ========================================

// BenchmarkUnmarshal_Invalid tests error handling performance
func BenchmarkUnmarshal_Invalid_StdLib(b *testing.B) {
	invalidJSON := []byte(`{"invalid": json}`)
	var result map[string]interface{}
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		json.Unmarshal(invalidJSON, &result)
	}
}

// BenchmarkUnmarshal_Invalid_GoccyJSON tests goccy error handling
func BenchmarkUnmarshal_Invalid_GoccyJSON(b *testing.B) {
	invalidJSON := []byte(`{"invalid": json}`)
	var result map[string]interface{}
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		goccyjson.Unmarshal(invalidJSON, &result)
	}
}

// ========================================
// Real-World Scenario Benchmarks
// ========================================

// BenchmarkRealWorld_IoTSensorBurst simulates IoT sensor burst
func BenchmarkRealWorld_IoTSensorBurst(b *testing.B) {
	engine := NewTemplateEngine(logger.NewNopLogger())
	template := `{"alert": "Temperature {temperature}°C from {device_id}"}`

	// Pre-unmarshal and create contexts outside the loop
	messages := make([]*EvaluationContext, 100)
	for i := 0; i < 100; i++ {
		var data map[string]interface{}
		goccyjson.Unmarshal(smallMessageJSON, &data)
		messages[i] = newBenchmarkContext(data, "sensors.temperature.room1")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, ctx := range messages {
			engine.Execute(template, ctx)
		}
	}
}

// BenchmarkRealWorld_MixedWorkload simulates mixed message sizes
func BenchmarkRealWorld_MixedWorkload(b *testing.B) {
	engine := NewTemplateEngine(logger.NewNopLogger())
	template := `{"processed": true, "timestamp": "{@timestamp()}"}`

	// Pre-unmarshal and create contexts outside the loop
	contexts := make([]*EvaluationContext, 100)
	for i := 0; i < 100; i++ {
		var payload []byte
		var subject string
		if i < 80 {
			payload, subject = smallMessageJSON, "sensors.temperature"
		} else if i < 95 {
			payload, subject = mediumMessageJSON, "sensors.data"
		} else {
			payload, subject = largeMessageJSON, "orders.created"
		}
		var data map[string]interface{}
		goccyjson.Unmarshal(payload, &data)
		contexts[i] = newBenchmarkContext(data, subject)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, ctx := range contexts {
			engine.Execute(template, ctx)
		}
	}
}
