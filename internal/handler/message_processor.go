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

// CreateHandlerFunc returns a Watermill handler function for the given topic
// This now supports wildcard patterns and extracts actual NATS subjects
func (h *MessageHandler) CreateHandlerFunc(subscriptionTopic string) message.HandlerFunc {
    return func(msg *message.Message) ([]*message.Message, error) {
        // Extract the actual NATS subject from the message
        actualSubject := h.extractActualSubject(msg, subscriptionTopic)
        
        h.logger.Debug("processing message through rule handler",
            "subscriptionTopic", subscriptionTopic,
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
                "subscriptionTopic", subscriptionTopic,
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
            actionMsg.Metadata.Set("destination_topic", action.Topic)
            actionMsg.Metadata.Set("source_subscription", subscriptionTopic)
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
                "actionTopic", action.Topic,
                "sourceSubscription", subscriptionTopic,
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
            "subscriptionTopic", subscriptionTopic,
            "actualSubject", actualSubject,
            "actionsGenerated", len(results))

        return results, nil
    }
}

// extractActualSubject extracts the actual NATS subject from the message
// This enables wildcard pattern matching by knowing what subject actually triggered
func (h *MessageHandler) extractActualSubject(msg *message.Message, subscriptionTopic string) string {
    // Try to get actual subject from NATS-specific metadata first
    if actualSubject := msg.Metadata.Get("nats_subject"); actualSubject != "" {
        h.logger.Debug("extracted actual subject from nats_subject metadata",
            "subscription", subscriptionTopic,
            "actualSubject", actualSubject)
        return actualSubject
    }
    
    // Try standard Watermill subject metadata
    if actualSubject := msg.Metadata.Get("subject"); actualSubject != "" {
        h.logger.Debug("extracted actual subject from subject metadata",
            "subscription", subscriptionTopic,
            "actualSubject", actualSubject)
        return actualSubject
    }
    
    // Try topic metadata (some publishers use this)
    if actualSubject := msg.Metadata.Get("topic"); actualSubject != "" {
        h.logger.Debug("extracted actual subject from topic metadata",
            "subscription", subscriptionTopic,
            "actualSubject", actualSubject)
        return actualSubject
    }
    
    // If no metadata available, check if subscription topic contains wildcards
    if h.isWildcardPattern(subscriptionTopic) {
        // For wildcard subscriptions, we need the actual subject but don't have it
        // This is a limitation - log it but continue with subscription topic
        h.logger.Info("wildcard subscription but no actual subject in metadata - using subscription topic",
            "subscriptionTopic", subscriptionTopic,
            "messageUUID", msg.UUID,
            "availableMetadata", h.getMetadataKeys(msg))
        
        // Return subscription topic as fallback, but this limits wildcard functionality
        return subscriptionTopic
    }
    
    // For exact match subscriptions, the subscription topic IS the actual subject
    h.logger.Debug("using subscription topic as actual subject (exact match)",
        "subscriptionTopic", subscriptionTopic)
    return subscriptionTopic
}

// isWildcardPattern checks if a topic contains NATS wildcard characters
func (h *MessageHandler) isWildcardPattern(topic string) bool {
    return strings.Contains(topic, "*") || strings.Contains(topic, ">")
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
