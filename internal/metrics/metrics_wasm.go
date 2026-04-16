// file: internal/metrics/metrics_wasm.go
// Stub metrics for WASM builds — no prometheus dependency.

//go:build js && wasm

package metrics

// Metrics is a no-op stub for WASM builds.
type Metrics struct{}

func (m *Metrics) IncMessagesTotal(status string)                         {}
func (m *Metrics) SetMessageProcessingBacklog(count float64)              {}
func (m *Metrics) IncRuleMatches()                                        {}
func (m *Metrics) SetRulesActive(count float64)                           {}
func (m *Metrics) IncActionsTotal(status string)                          {}
func (m *Metrics) IncActionsByType(actionType string)                     {}
func (m *Metrics) IncActionPublishFailures()                              {}
func (m *Metrics) IncTemplateOpsTotal(status string)                      {}
func (m *Metrics) SetNATSConnectionStatus(connected bool)                 {}
func (m *Metrics) IncNATSReconnects()                                     {}
func (m *Metrics) IncSignatureVerifications(result string)                {}
func (m *Metrics) ObserveSignatureVerificationDuration(seconds float64)   {}
func (m *Metrics) IncKVCacheHits()                                        {}
func (m *Metrics) IncKVCacheMisses()                                      {}
func (m *Metrics) SetKVCacheSize(size float64)                            {}
func (m *Metrics) IncForEachIterations(ruleFile string, count int)        {}
func (m *Metrics) IncForEachFiltered(ruleFile string, count int)          {}
func (m *Metrics) IncForEachActionsGenerated(ruleFile string, count int)  {}
func (m *Metrics) IncForEachElementErrors(ruleFile, errorType string)     {}
func (m *Metrics) ObserveForEachDuration(ruleFile string, seconds float64) {}
func (m *Metrics) IncThrottleSuppressed(phase string)                     {}
func (m *Metrics) IncArrayOperatorEvaluations(operator string, result bool) {}
func (m *Metrics) UpdateSystemMetrics()                                   {}
func (m *Metrics) IncHTTPInboundRequestsTotal(path, method, status string) {}
func (m *Metrics) ObserveHTTPRequestDuration(path, method string, seconds float64) {}
func (m *Metrics) IncHTTPOutboundRequestsTotal(url, statusCode string)    {}
func (m *Metrics) ObserveHTTPOutboundDuration(url string, seconds float64) {}
func (m *Metrics) GetStats() (received, errors uint64)                    { return 0, 0 }
