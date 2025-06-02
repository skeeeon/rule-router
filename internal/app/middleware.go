//file: internal/app/middleware.go

package app

import (
	"fmt"
	"strings"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/message/router/middleware"

	"rule-router/internal/handler"
)

// setupMiddleware configures the complete Watermill router middleware stack
func (a *App) setupMiddleware() {
	messageHandler := handler.NewMessageHandler(a.processor, a.logger, a.metrics)

	a.router.AddMiddleware(
		// Correlation ID propagation (Watermill built-in)
		middleware.CorrelationID,

		// Custom middleware stack
		handler.CorrelationIDMiddleware(),
		handler.LoggingMiddleware(a.logger),
		handler.ValidationMiddleware(a.logger),
		handler.RecoveryMiddleware(a.logger),

		// Conditional metrics middleware
		a.conditionalMetricsMiddleware(),

		// Retry middleware with configuration
		handler.RetryMiddleware(a.config.Watermill.Middleware.RetryMaxAttempts, a.logger),

		// Poison queue handling
		handler.PoisonQueueMiddleware(a.logger, a.metrics),

		// Rule engine integration (this is where the magic happens)
		handler.RuleEngineMiddleware(messageHandler),
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

// setupHandlers configures message handlers for each rule topic
func (a *App) setupHandlers() {
	publisher := a.broker.GetPublisher()
	subscriber := a.broker.GetSubscriber()

	// Extract unique topics from rules and add handlers
	topics := a.processor.GetTopics()
	a.logger.Info("setting up Watermill handlers for rule topics", "topicCount", len(topics))

	for _, topic := range topics {
		handlerName := fmt.Sprintf("processor-%s", a.sanitizeHandlerName(topic))

		// Convert MQTT topic format to NATS subject format if using NATS
		subscribeTopic := topic
		publishTopic := "processed." + topic

		if a.config.BrokerType == "nats" {
			subscribeTopic = a.toNATSSubject(topic)
			publishTopic = a.toNATSSubject(publishTopic)
		}

		a.logger.Debug("adding handler",
			"handlerName", handlerName,
			"subscribeTopic", subscribeTopic,
			"publishTopic", publishTopic)

		a.router.AddHandler(
			handlerName,
			subscribeTopic,
			subscriber,
			publishTopic,
			publisher,
			func(msg *message.Message) ([]*message.Message, error) {
				// Set topic in metadata for rule processing
				msg.Metadata.Set("topic", topic)

				// The actual processing happens in the middleware chain
				// This handler just passes the message through
				return []*message.Message{msg}, nil
			},
		)
	}
}

// sanitizeHandlerName ensures handler names are valid for Watermill
func (a *App) sanitizeHandlerName(topic string) string {
	// Replace problematic characters in topic names for handler names
	sanitized := topic
	sanitized = strings.ReplaceAll(sanitized, "/", "-")
	sanitized = strings.ReplaceAll(sanitized, ".", "-")
	sanitized = strings.ReplaceAll(sanitized, " ", "-")
	sanitized = strings.ReplaceAll(sanitized, "#", "wildcard")
	sanitized = strings.ReplaceAll(sanitized, "+", "single")
	return sanitized
}

// toNATSSubject converts MQTT topic format to NATS subject format
func (a *App) toNATSSubject(mqttTopic string) string {
	// Convert MQTT topic format to NATS subject format
	// MQTT uses / as separators and +/# as wildcards
	// NATS uses . as separators and */> as wildcards
	subject := mqttTopic
	subject = strings.ReplaceAll(subject, "+", "*")
	subject = strings.ReplaceAll(subject, "#", ">")
	subject = strings.ReplaceAll(subject, "/", ".")
	return subject
}
