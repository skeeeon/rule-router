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
// This is where the actual rule processing happens now
func (h *MessageHandler) CreateHandlerFunc(subscribeTopic string) message.HandlerFunc {
    return func(msg *message.Message) ([]*message.Message, error) {
        // Extract the actual topic from the subscription
        // For NATS subjects like "sensors.temperature", use that directly
        topic := subscribeTopic
        
        // If the subscription had wildcards, try to get the actual subject
        if actualSubject := msg.Metadata.Get("nats_subject"); actualSubject != "" {
            topic = actualSubject
        }

        h.logger.Debug("processing message through rule handler",
            "subscribeTopic", subscribeTopic,
            "actualTopic", topic,
            "messageUUID", msg.UUID,
            "payloadSize", len(msg.Payload))

        // Update metrics for received messages
        if h.metrics != nil {
            h.metrics.IncMessagesTotal("received")
        }

        // Process through rule engine - this is where the real work happens
        actions, err := h.ruleProcessor.Process(topic, msg.Payload)
        if err != nil {
            if h.metrics != nil {
                h.metrics.IncMessagesTotal("error")
            }
            h.logger.Error("rule processing failed",
                "error", err,
                "topic", topic,
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
            actionMsg.Metadata.Set("source_topic", topic)
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
                "sourceTopic", topic,
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
            "topic", topic,
            "actionsGenerated", len(results))

        return results, nil
    }
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

// extractTopicFromSubject extracts the actual topic from NATS subject patterns
func (h *MessageHandler) extractTopicFromSubject(subscription, subject string) string {
    // If no wildcards in subscription, use as-is
    if !strings.Contains(subscription, "*") && !strings.Contains(subscription, ">") {
        return subscription
    }
    
    // If we have the actual subject, use that
    if subject != "" {
        return subject
    }
    
    // Fallback to subscription pattern
    return subscription
}
