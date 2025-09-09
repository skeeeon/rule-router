//file: internal/app/middleware.go

package app

import (
	"fmt"
	"strings"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/message/router/middleware"

	"rule-router/internal/handler"
	"rule-router/internal/logger"
)

// setupMiddleware configures the Watermill router middleware stack
// Simplified to focus on operational concerns, not business logic
func (a *App) setupMiddleware() {
	a.router.AddMiddleware(
		// Correlation ID propagation (Watermill built-in)
		middleware.CorrelationID,

		// Custom operational middleware stack
		handler.CorrelationIDMiddleware(),
		handler.LoggingMiddleware(a.logger),
		handler.RecoveryMiddleware(a.logger),

		// Conditional metrics middleware
		a.conditionalMetricsMiddleware(),

		// Retry middleware with configuration
		handler.RetryMiddleware(a.config.Watermill.Middleware.RetryMaxAttempts, a.logger),

		// Poison queue handling
		handler.PoisonQueueMiddleware(a.logger, a.metrics),

		// NOTE: RuleEngineMiddleware REMOVED - processing now happens in handlers
	)

	a.logger.Info("middleware stack configured",
		"retryAttempts", a.config.Watermill.Middleware.RetryMaxAttempts,
		"metricsEnabled", a.config.Watermill.Middleware.MetricsEnabled)
}

// conditionalMetricsMiddleware returns metrics middleware if enabled, otherwise pass-through
func (a *App) conditionalMetricsMiddleware() message.HandlerMiddleware {
	if a.config.Watermill.Middleware.MetricsEnabled && a.metrics != nil {
		return handler.MetricsMiddleware(a.metrics)
	}
	return func(h message.HandlerFunc) message.HandlerFunc {
		return h // Pass-through if metrics disabled
	}
}

// setupHandlers configures message handlers for each rule topic using NATS subjects
// NOW handlers do the actual processing work
func (a *App) setupHandlers() {
	publisher := a.broker.GetPublisher()
	subscriber := a.broker.GetSubscriber()
	messageHandler := handler.NewMessageHandler(a.processor, a.logger, a.metrics)

	// Extract unique topics from rules and add handlers
	topics := a.processor.GetTopics()
	a.logger.Info("setting up Watermill handlers for rule topics", "topicCount", len(topics))

	for _, topic := range topics {
		handlerName := fmt.Sprintf("processor-%s", a.sanitizeHandlerName(topic))

		// Use topic directly - it's already in NATS subject format
		subscribeTopic := topic

		a.logger.Debug("adding handler",
			"handlerName", handlerName,
			"subscribeTopic", subscribeTopic)

		// Create a custom publisher that routes to action topics
		customPublisher := &ActionTopicPublisher{
			publisher: publisher,
			logger:    a.logger,
		}

		// Handler now does the real work via CreateHandlerFunc
		a.router.AddHandler(
			handlerName,
			subscribeTopic,
			subscriber,
			"", // No fixed publish topic - determined by action
			customPublisher,
			messageHandler.CreateHandlerFunc(subscribeTopic),
		)
	}
}

// ActionTopicPublisher is a custom publisher that routes messages based on their metadata
type ActionTopicPublisher struct {
	publisher message.Publisher
	logger    *logger.Logger
}

// Publish publishes a message to the topic specified in its metadata
func (p *ActionTopicPublisher) Publish(topic string, messages ...*message.Message) error {
	for _, msg := range messages {
		// Get the actual destination topic from metadata
		destinationTopic := msg.Metadata.Get("destination_topic")
		if destinationTopic == "" {
			p.logger.Error("no destination_topic in message metadata",
				"messageUUID", msg.UUID,
				"fallbackTopic", topic)
			destinationTopic = topic // Fallback
		}

		// Publish to the action-specified topic
		err := p.publisher.Publish(destinationTopic, msg)
		if err != nil {
			return fmt.Errorf("failed to publish to topic %s: %w", destinationTopic, err)
		}

		p.logger.Debug("published action message",
			"destinationTopic", destinationTopic,
			"messageUUID", msg.UUID)
	}
	return nil
}

// Close implements message.Publisher interface
func (p *ActionTopicPublisher) Close() error {
	return p.publisher.Close()
}

// sanitizeHandlerName ensures handler names are valid for Watermill with NATS subjects
func (a *App) sanitizeHandlerName(topic string) string {
	// Replace NATS-specific characters in subject names for handler names
	sanitized := topic
	sanitized = strings.ReplaceAll(sanitized, ".", "-")
	sanitized = strings.ReplaceAll(sanitized, "*", "wildcard")
	sanitized = strings.ReplaceAll(sanitized, ">", "multi-wildcard")
	sanitized = strings.ReplaceAll(sanitized, " ", "-")
	return sanitized
}
