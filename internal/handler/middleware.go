//file: internal/handler/middleware.go

package handler

import (
    "fmt"
    "math"
    "math/rand"
    "strings"
    "time"

    "github.com/ThreeDotsLabs/watermill/message"
    "rule-router/internal/logger"
    "rule-router/internal/metrics"
)

// LoggingMiddleware creates middleware for comprehensive request logging
func LoggingMiddleware(logger *logger.Logger) message.HandlerMiddleware {
    return func(h message.HandlerFunc) message.HandlerFunc {
        return func(msg *message.Message) ([]*message.Message, error) {
            start := time.Now()
            topic := msg.Metadata.Get("topic")
            
            logger.Debug("processing message",
                "uuid", msg.UUID,
                "topic", topic,
                "payloadSize", len(msg.Payload))

            results, err := h(msg)
            
            duration := time.Since(start)
            if err != nil {
                logger.Error("message processing failed",
                    "error", err,
                    "uuid", msg.UUID,
                    "topic", topic,
                    "duration", duration)
            } else {
                logger.Debug("message processing complete",
                    "uuid", msg.UUID,
                    "topic", topic,
                    "duration", duration,
                    "actionsGenerated", len(results))
            }

            return results, err
        }
    }
}

// MetricsMiddleware creates middleware for metrics collection
func MetricsMiddleware(metrics *metrics.Metrics) message.HandlerMiddleware {
    return func(h message.HandlerFunc) message.HandlerFunc {
        return func(msg *message.Message) ([]*message.Message, error) {
            // Metrics are now handled in the handlers themselves
            // This middleware just passes through
            results, err := h(msg)
            
            // Optional: Add middleware-level metrics here if needed
            if metrics != nil {
                // Update processing metrics
                metrics.SetMessageQueueDepth(0) // Watermill handles queuing
            }

            return results, err
        }
    }
}

// RecoveryMiddleware creates middleware for panic recovery
func RecoveryMiddleware(logger *logger.Logger) message.HandlerMiddleware {
    return func(h message.HandlerFunc) message.HandlerFunc {
        return func(msg *message.Message) (results []*message.Message, err error) {
            defer func() {
                if r := recover(); r != nil {
                    logger.Error("panic recovered in message handler",
                        "panic", r,
                        "uuid", msg.UUID,
                        "topic", msg.Metadata.Get("topic"))
                    err = fmt.Errorf("panic in message handler: %v", r)
                }
            }()

            return h(msg)
        }
    }
}

// CorrelationIDMiddleware creates middleware to ensure correlation IDs are present
func CorrelationIDMiddleware() message.HandlerMiddleware {
    return func(h message.HandlerFunc) message.HandlerFunc {
        return func(msg *message.Message) ([]*message.Message, error) {
            // Ensure correlation ID is present
            if msg.Metadata.Get("correlation_id") == "" {
                msg.Metadata.Set("correlation_id", msg.UUID)
            }

            results, err := h(msg)
            
            // Propagate correlation ID to result messages
            correlationID := msg.Metadata.Get("correlation_id")
            for _, result := range results {
                if result.Metadata.Get("correlation_id") == "" {
                    result.Metadata.Set("correlation_id", correlationID)
                }
            }

            return results, err
        }
    }
}

// RetryMiddleware creates middleware for retrying failed message processing with exponential backoff
func RetryMiddleware(maxRetries int, logger *logger.Logger) message.HandlerMiddleware {
    return func(h message.HandlerFunc) message.HandlerFunc {
        return func(msg *message.Message) ([]*message.Message, error) {
            var lastErr error
            
            for attempt := 0; attempt <= maxRetries; attempt++ {
                if attempt > 0 {
                    logger.Debug("retrying message processing",
                        "uuid", msg.UUID,
                        "attempt", attempt,
                        "maxRetries", maxRetries,
                        "lastError", lastErr)
                    
                    // Exponential backoff with jitter
                    backoff := calculateBackoffWithJitter(attempt)
                    time.Sleep(backoff)
                }

                results, err := h(msg)
                if err == nil {
                    if attempt > 0 {
                        logger.Info("message processing succeeded after retry",
                            "uuid", msg.UUID,
                            "attempts", attempt+1)
                    }
                    return results, nil
                }
                
                lastErr = err
            }

            logger.Error("message processing failed after all retries",
                "uuid", msg.UUID,
                "attempts", maxRetries+1,
                "error", lastErr)
            
            return nil, fmt.Errorf("processing failed after %d attempts: %w", maxRetries+1, lastErr)
        }
    }
}

// calculateBackoffWithJitter implements exponential backoff with jitter to prevent thundering herd
func calculateBackoffWithJitter(attempt int) time.Duration {
    // Base backoff: 100ms * 2^attempt
    baseBackoff := 100 * time.Millisecond * time.Duration(math.Pow(2, float64(attempt)))
    
    // Cap at 30 seconds
    if baseBackoff > 30*time.Second {
        baseBackoff = 30 * time.Second
    }
    
    // Add jitter (±25% of base backoff)
    jitter := time.Duration(rand.Int63n(int64(baseBackoff/2))) // 0 to 50% of base
    if rand.Intn(2) == 0 {
        return baseBackoff + jitter // Add jitter
    }
    return baseBackoff - jitter // Subtract jitter
}

// PoisonQueueMiddleware creates middleware for handling poison messages
func PoisonQueueMiddleware(logger *logger.Logger, metrics *metrics.Metrics) message.HandlerMiddleware {
    return func(h message.HandlerFunc) message.HandlerFunc {
        return func(msg *message.Message) ([]*message.Message, error) {
            results, err := h(msg)
            
            if err != nil {
                // Check if this is a poison message (non-retryable error)
                if isPoisonMessage(err) {
                    logger.Error("poison message detected",
                        "uuid", msg.UUID,
                        "topic", msg.Metadata.Get("topic"),
                        "error", err)
                    
                    if metrics != nil {
                        metrics.IncMessagesTotal("poison")
                    }
                    
                    // For now, just log poison messages
                    // In production, you might want to send to a dead letter queue
                    return nil, nil // Don't propagate poison message errors
                }
            }
            
            return results, err
        }
    }
}

// isPoisonMessage determines if an error indicates a poison message
func isPoisonMessage(err error) bool {
    if err == nil {
        return false
    }
    
    // Consider messages with JSON parsing errors as poison
    errorStr := err.Error()
    return strings.Contains(errorStr, "unmarshal") || 
           strings.Contains(errorStr, "invalid character") ||
           strings.Contains(errorStr, "unexpected end of JSON")
}
