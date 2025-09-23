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

// setupHandlers configures message handlers for each rule subject using NATS subjects
// NOW handlers do the actual processing work
func (a *App) setupHandlers() {
	publisher := a.broker.GetPublisher()
	subscriber := a.broker.GetSubscriber()
	messageHandler := handler.NewMessageHandler(a.processor, a.logger, a.metrics)

	// Extract unique subjects from rules and add handlers
	subjects := a.processor.GetSubjects()
	a.logger.Info("setting up Watermill handlers for rule subjects", "subjectCount", len(subjects))

	for _, subject := range subjects {
		handlerName := fmt.Sprintf("processor-%s", a.sanitizeHandlerName(subject))

		// Use subject directly - it's already in NATS subject format
		subscribeSubject := subject

		a.logger.Debug("adding handler",
			"handlerName", handlerName,
			"subscribeSubject", subscribeSubject)

		// Create a custom publisher that routes to action subjects
		customPublisher := &ActionSubjectPublisher{
			publisher: publisher,
			logger:    a.logger,
		}

		// Handler now does the real work via CreateHandlerFunc
		a.router.AddHandler(
			handlerName,
			subscribeSubject,
			subscriber,
			"", // No fixed publish subject - determined by action
			customPublisher,
			messageHandler.CreateHandlerFunc(subscribeSubject),
		)
	}
}

// ActionSubjectPublisher is a custom publisher that routes messages based on their metadata
type ActionSubjectPublisher struct {
	publisher message.Publisher
	logger    *logger.Logger
}

// Publish publishes a message to the subject specified in its metadata
func (p *ActionSubjectPublisher) Publish(subject string, messages ...*message.Message) error {
	for _, msg := range messages {
		// Get the actual destination subject from metadata
		destinationSubject := msg.Metadata.Get("destination_subject")
		if destinationSubject == "" {
			p.logger.Error("no destination_subject in message metadata",
				"messageUUID", msg.UUID,
				"fallbackSubject", subject)
			destinationSubject = subject // Fallback
		}

		// Publish to the action-specified subject
		err := p.publisher.Publish(destinationSubject, msg)
		if err != nil {
			return fmt.Errorf("failed to publish to subject %s: %w", destinationSubject, err)
		}

		p.logger.Debug("published action message",
			"destinationSubject", destinationSubject,
			"messageUUID", msg.UUID)
	}
	return nil
}

// Close implements message.Publisher interface
func (p *ActionSubjectPublisher) Close() error {
	return p.publisher.Close()
}

// sanitizeHandlerName ensures handler names are valid for Watermill with NATS subjects
func (a *App) sanitizeHandlerName(subject string) string {
	// Replace NATS-specific characters in subject names for handler names
	sanitized := subject
	sanitized = strings.ReplaceAll(sanitized, ".", "-")
	sanitized = strings.ReplaceAll(sanitized, "*", "wildcard")
	sanitized = strings.ReplaceAll(sanitized, ">", "multi-wildcard")
	sanitized = strings.ReplaceAll(sanitized, " ", "-")
	return sanitized
}
