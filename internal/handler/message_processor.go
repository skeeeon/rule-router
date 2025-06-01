//file: internal/handler/message_processor.go

package handler

import (
    "fmt"

    "github.com/ThreeDotsLabs/watermill/message"
    "rule-router/internal/logger"
    "rule-router/internal/metrics"
    "rule-router/internal/rule"
)

// MessageHandler processes messages through the rule engine using Watermill
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

// ProcessMessage processes a Watermill message through the rule engine
func (h *MessageHandler) ProcessMessage(msg *message.Message) ([]*message.Message, error) {
    // Extract topic from Watermill message metadata
    topic := msg.Metadata.Get("topic")
    if topic == "" {
        // Fallback to using a default topic extraction method
        topic = "unknown"
        h.logger.Debug("no topic in message metadata, using default", "topic", topic)
    }

    h.logger.Debug("processing message through rule engine",
        "topic", topic,
        "messageUUID", msg.UUID,
        "payloadSize", len(msg.Payload))

    // Process through existing rule engine
    actions, err := h.ruleProcessor.Process(topic, msg.Payload)
    if err != nil {
        h.logger.Error("rule processing failed",
            "error", err,
            "topic", topic,
            "messageUUID", msg.UUID)
        return nil, fmt.Errorf("rule processing failed: %w", err)
    }

    // Convert rule actions to Watermill messages
    var results []*message.Message
    for _, action := range actions {
        resultMsg := message.NewMessage(msg.UUID+"-action", []byte(action.Payload))
        resultMsg.Metadata.Set("topic", action.Topic)
        resultMsg.Metadata.Set("source_topic", topic)
        resultMsg.Metadata.Set("source_message_id", msg.UUID)
        
        // Copy correlation ID if present
        if correlationID := msg.Metadata.Get("correlation_id"); correlationID != "" {
            resultMsg.Metadata.Set("correlation_id", correlationID)
        }

        results = append(results, resultMsg)

        h.logger.Debug("created action message",
            "actionTopic", action.Topic,
            "sourceTopic", topic,
            "actionUUID", resultMsg.UUID)
    }

    h.logger.Debug("message processing complete",
        "topic", topic,
        "actionsGenerated", len(results))

    return results, nil
}

// HandleMessage is a Watermill handler function that processes messages
func (h *MessageHandler) HandleMessage(msg *message.Message) error {
    h.logger.Debug("handling watermill message",
        "uuid", msg.UUID,
        "payloadSize", len(msg.Payload))

    // Extract topic from metadata or use a default
    topic := msg.Metadata.Get("topic")
    if topic == "" {
        topic = "default"
    }

    // Process through rule engine
    actions, err := h.ruleProcessor.Process(topic, msg.Payload)
    if err != nil {
        h.logger.Error("failed to process message through rule engine",
            "error", err,
            "topic", topic,
            "uuid", msg.UUID)
        return fmt.Errorf("rule processing failed: %w", err)
    }

    // Log actions for debugging (in production, these would be published)
    for _, action := range actions {
        h.logger.Info("action generated",
            "sourceTopic", topic,
            "actionTopic", action.Topic,
            "payloadLength", len(action.Payload))
    }

    return nil
}
