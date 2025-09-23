//file: internal/handler/message_processor.go

package handler

import (
    "fmt"
    "strings"

    "github.com/ThreeDotsLabs/watermill/message"
    "github.com/google/uuid"
    "rule-router/internal/logger"
    "rule-router/internal/metrics"
    "rule-router/internal/rule"
)

// MessageHandler processes messages through the rule engine using Watermill handlers
type MessageHandler struct {
    ruleProcessor *rule.Processor
    logger        *logger.Logger
    metrics       *metrics.Metrics
}

// NewMessageHandler creates a new Watermill message handler
func NewMessageHandler(processor *rule.Processor, logger *logger.Logger, metrics *metrics.Metrics) *MessageHandler {
    return &MessageHandler{
        ruleProcessor: processor,
        logger:        logger,
        metrics:       metrics,
    }
}

// CreateHandlerFunc returns a Watermill handler function for the given subject
// This now supports wildcard patterns and extracts actual NATS subjects
func (h *MessageHandler) CreateHandlerFunc(subscriptionSubject string) message.HandlerFunc {
    return func(msg *message.Message) ([]*message.Message, error) {
        // Extract the actual NATS subject from the message
        actualSubject := h.extractActualSubject(msg, subscriptionSubject)
        
        h.logger.Debug("processing message through rule handler",
            "subscriptionSubject", subscriptionSubject,
            "actualSubject", actualSubject,
            "messageUUID", msg.UUID,
            "payloadSize", len(msg.Payload))

        // Update metrics for received messages
        if h.metrics != nil {
            h.metrics.IncMessagesTotal("received")
        }

        // Process through rule engine with actual subject - this enables wildcard support
        actions, err := h.ruleProcessor.ProcessWithSubject(actualSubject, msg.Payload)
        if err != nil {
            if h.metrics != nil {
                h.metrics.IncMessagesTotal("error")
            }
            h.logger.Error("rule processing failed",
                "error", err,
                "subscriptionSubject", subscriptionSubject,
                "actualSubject", actualSubject,
                "messageUUID", msg.UUID)
            return nil, fmt.Errorf("rule processing failed: %w", err)
        }

        // Convert rule actions to Watermill messages for publishing
        results := make([]*message.Message, 0, len(actions))
        for i, action := range actions {
            // Create a new message for each action
            actionID := fmt.Sprintf("%s-action-%d", msg.UUID, i)
            actionMsg := message.NewMessage(actionID, []byte(action.Payload))
            
            // Set metadata for routing and tracking
            actionMsg.Metadata.Set("destination_subject", action.Subject)
            actionMsg.Metadata.Set("source_subscription", subscriptionSubject)
            actionMsg.Metadata.Set("source_actual_subject", actualSubject)
            actionMsg.Metadata.Set("source_message_id", msg.UUID)
            
            // Copy correlation ID if present
            if correlationID := msg.Metadata.Get("correlation_id"); correlationID != "" {
                actionMsg.Metadata.Set("correlation_id", correlationID)
            } else {
                // Create new correlation ID if none exists
                actionMsg.Metadata.Set("correlation_id", uuid.New().String())
            }

            results = append(results, actionMsg)

            h.logger.Debug("created action message",
                "actionSubject", action.Subject,
                "sourceSubscription", subscriptionSubject,
                "sourceActualSubject", actualSubject,
                "actionUUID", actionMsg.UUID,
                "payloadLength", len(action.Payload))
        }

        // Update metrics for successful processing
        if h.metrics != nil {
            h.metrics.IncMessagesTotal("processed")
            for range results {
                h.metrics.IncActionsTotal("success")
            }
        }

        h.logger.Debug("message processing complete",
            "subscriptionSubject", subscriptionSubject,
            "actualSubject", actualSubject,
            "actionsGenerated", len(results))

        return results, nil
    }
}

// extractActualSubject extracts the actual NATS subject from the message
// This enables wildcard pattern matching by knowing what subject actually triggered
func (h *MessageHandler) extractActualSubject(msg *message.Message, subscriptionSubject string) string {
    // Try to get actual subject from NATS-specific metadata first
    if actualSubject := msg.Metadata.Get("nats_subject"); actualSubject != "" {
        h.logger.Debug("extracted actual subject from nats_subject metadata",
            "subscription", subscriptionSubject,
            "actualSubject", actualSubject)
        return actualSubject
    }
    
    // Try standard Watermill subject metadata
    if actualSubject := msg.Metadata.Get("subject"); actualSubject != "" {
        h.logger.Debug("extracted actual subject from subject metadata",
            "subscription", subscriptionSubject,
            "actualSubject", actualSubject)
        return actualSubject
    }
    
    // Try legacy topic metadata (some publishers use this)
    if actualSubject := msg.Metadata.Get("topic"); actualSubject != "" {
        h.logger.Debug("extracted actual subject from topic metadata",
            "subscription", subscriptionSubject,
            "actualSubject", actualSubject)
        return actualSubject
    }
    
    // If no metadata available, check if subscription subject contains wildcards
    if h.isWildcardPattern(subscriptionSubject) {
        // For wildcard subscriptions, we need the actual subject but don't have it
        // This is a limitation - log it but continue with subscription subject
        h.logger.Info("wildcard subscription but no actual subject in metadata - using subscription subject",
            "subscriptionSubject", subscriptionSubject,
            "messageUUID", msg.UUID,
            "availableMetadata", h.getMetadataKeys(msg))
        
        // Return subscription subject as fallback, but this limits wildcard functionality
        return subscriptionSubject
    }
    
    // For exact match subscriptions, the subscription subject IS the actual subject
    h.logger.Debug("using subscription subject as actual subject (exact match)",
        "subscriptionSubject", subscriptionSubject)
    return subscriptionSubject
}

// isWildcardPattern checks if a subject contains NATS wildcard characters
func (h *MessageHandler) isWildcardPattern(subject string) bool {
    return strings.Contains(subject, "*") || strings.Contains(subject, ">")
}

// getMetadataKeys returns all metadata keys for debugging
func (h *MessageHandler) getMetadataKeys(msg *message.Message) []string {
    keys := make([]string, 0, len(msg.Metadata))
    for key := range msg.Metadata {
        keys = append(keys, key)
    }
    return keys
}

// ValidateMessage performs basic message validation
func (h *MessageHandler) ValidateMessage(msg *message.Message) error {
    // Basic validation
    if len(msg.Payload) == 0 {
        h.logger.Debug("empty payload received", "uuid", msg.UUID)
        return fmt.Errorf("empty message payload")
    }

    // Additional validation can be added here
    return nil
}
