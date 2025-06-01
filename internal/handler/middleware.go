//file: internal/handler/middleware.go

package handler

import (
    "fmt"
    "time"

    "github.com/ThreeDotsLabs/watermill/message"
    "rule-router/internal/logger"
    "rule-router/internal/metrics"
)

// RuleEngineMiddleware creates middleware that integrates with the rule engine
func RuleEngineMiddleware(handler *MessageHandler) message.HandlerMiddleware {
    return func(h message.HandlerFunc) message.HandlerFunc {
        return func(msg *message.Message) ([]*message.Message, error) {
            // Process message through rule engine
            return handler.ProcessMessage(msg)
        }
    }
}

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
            if metrics != nil {
                metrics.IncMessagesTotal("received")
            }

            results, err := h(msg)
            
            if metrics != nil {
                if err != nil {
                    metrics.IncMessagesTotal("error")
                } else {
                    metrics.IncMessagesTotal("processed")
                    for range results {
                        metrics.IncActionsTotal("success")
                    }
                }
                
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

// ValidationMiddleware creates middleware for message validation
func ValidationMiddleware(logger *logger.Logger) message.HandlerMiddleware {
    return func(h message.HandlerFunc) message.HandlerFunc {
        return func(msg *message.Message) ([]*message.Message, error) {
            // Basic validation
            if len(msg.Payload) == 0 {
                logger.Debug("empty payload received", "uuid", msg.UUID)
                return nil, fmt.Errorf("empty message payload")
            }

            topic := msg.Metadata.Get("topic")
            if topic == "" {
                logger.Debug("no topic in message metadata", "uuid", msg.UUID)
                // Don't fail, just log - some messages might not have topics
            }

            return h(msg)
        }
    }
}

// RetryMiddleware creates middleware for retrying failed message processing
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
                    
                    // Exponential backoff
                    backoff := time.Duration(attempt) * 100 * time.Millisecond
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
    return contains(errorStr, "unmarshal") || 
           contains(errorStr, "invalid character") ||
           contains(errorStr, "unexpected end of JSON")
}

// contains checks if a string contains a substring (case-insensitive helper)
func contains(s, substr string) bool {
    return len(s) >= len(substr) && 
           (s == substr || len(substr) == 0 || 
            (len(s) > len(substr) && (s[:len(substr)] == substr || 
             s[len(s)-len(substr):] == substr || 
             indexOf(s, substr) >= 0)))
}

// indexOf finds the index of substring in string
func indexOf(s, substr string) int {
    for i := 0; i <= len(s)-len(substr); i++ {
        if s[i:i+len(substr)] == substr {
            return i
        }
    }
    return -1
}
