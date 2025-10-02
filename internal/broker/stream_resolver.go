//file: internal/broker/stream_resolver.go

package broker

import (
	"fmt"
	"strings"

	watermillNats "github.com/nats-io/nats.go"
	"rule-router/internal/logger"
)

// StreamResolver discovers JetStream streams and maps subjects to streams
type StreamResolver struct {
	jsCtx      watermillNats.JetStreamContext
	streams    []StreamInfo
	logger     *logger.Logger
	discovered bool
}

// StreamInfo holds information about a JetStream stream
type StreamInfo struct {
	Name     string   // Stream name (e.g., "SENSORS")
	Subjects []string // Subject filters (e.g., ["sensors.>", "devices.*.temperature"])
}

// NewStreamResolver creates a new stream resolver
func NewStreamResolver(jsCtx watermillNats.JetStreamContext, logger *logger.Logger) *StreamResolver {
	return &StreamResolver{
		jsCtx:      jsCtx,
		streams:    make([]StreamInfo, 0),
		logger:     logger,
		discovered: false,
	}
}

// Discover queries NATS JetStream for all available streams
func (sr *StreamResolver) Discover() error {
	sr.logger.Info("discovering JetStream streams")

	// Get list of all streams
	streamNames := make([]string, 0)
	for streamInfo := range sr.jsCtx.StreamsInfo() {
		if streamInfo == nil {
			break
		}
		streamNames = append(streamNames, streamInfo.Config.Name)
		
		// Store stream information
		sr.streams = append(sr.streams, StreamInfo{
			Name:     streamInfo.Config.Name,
			Subjects: streamInfo.Config.Subjects,
		})

		sr.logger.Debug("discovered stream",
			"name", streamInfo.Config.Name,
			"subjects", streamInfo.Config.Subjects,
			"messages", streamInfo.State.Msgs)
	}

	if len(sr.streams) == 0 {
		sr.logger.Warn("no JetStream streams found - rules will fail to initialize")
		return fmt.Errorf("no JetStream streams found - please create streams before starting rule-router")
	}

	sr.discovered = true
	
	// Log summary with subject filters for debugging
	sr.logger.Info("stream discovery complete",
		"streamCount", len(sr.streams),
		"streams", streamNames)
	
	// Debug: Log all stream filters to help diagnose matching issues
	for _, stream := range sr.streams {
		sr.logger.Debug("stream subject filters",
			"stream", stream.Name,
			"subjects", stream.Subjects,
			"isSystem", strings.HasPrefix(stream.Name, "$") || strings.HasPrefix(stream.Name, "KV_"))
	}

	return nil
}

// FindStreamForSubject finds the stream that handles messages for the given subject
// Supports both exact subjects and wildcard patterns
// Prefers user streams over system streams and more specific matches over broad wildcards
func (sr *StreamResolver) FindStreamForSubject(subject string) (string, error) {
	if !sr.discovered {
		return "", fmt.Errorf("streams not discovered - call Discover() first")
	}

	sr.logger.Debug("finding stream for subject", "subject", subject)

	// Collect all matching streams with their specificity score
	type streamMatch struct {
		streamName string
		filter     string
		specificity int // Higher = more specific
		isSystem   bool // System streams start with $
	}
	
	var matches []streamMatch

	// Check each stream to see if its subject filters match our subject
	for _, stream := range sr.streams {
		for _, filter := range stream.Subjects {
			if sr.subjectMatches(subject, filter) {
				specificity := sr.calculateSpecificity(filter)
				// System streams: those starting with $ or KV_ (auto-generated)
				isSystem := strings.HasPrefix(stream.Name, "$") || strings.HasPrefix(stream.Name, "KV_")
				
				matches = append(matches, streamMatch{
					streamName:  stream.Name,
					filter:      filter,
					specificity: specificity,
					isSystem:    isSystem,
				})
				
				sr.logger.Debug("stream matches subject",
					"subject", subject,
					"stream", stream.Name,
					"filter", filter,
					"specificity", specificity,
					"isSystem", isSystem)
			}
		}
	}

	if len(matches) == 0 {
		// No stream found
		availableFilters := sr.getAllSubjectFilters()
		return "", fmt.Errorf("no stream found for subject '%s' - available stream filters: %v", 
			subject, availableFilters)
	}

	// Select the best match:
	// 1. Prefer non-system streams over system streams
	// 2. Among same type, prefer higher specificity
	var bestMatch *streamMatch
	for i := range matches {
		match := &matches[i]
		
		if bestMatch == nil {
			bestMatch = match
			continue
		}

		// Prefer non-system streams
		if !match.isSystem && bestMatch.isSystem {
			bestMatch = match
			continue
		}
		if match.isSystem && !bestMatch.isSystem {
			continue
		}

		// If both same type (system or non-system), prefer higher specificity
		if match.specificity > bestMatch.specificity {
			bestMatch = match
		}
	}

	sr.logger.Info("selected best matching stream",
		"subject", subject,
		"selectedStream", bestMatch.streamName,
		"filter", bestMatch.filter,
		"specificity", bestMatch.specificity,
		"isSystem", bestMatch.isSystem,
		"totalMatches", len(matches))

	return bestMatch.streamName, nil
}

// calculateSpecificity returns a score indicating how specific a subject filter is
// Higher score = more specific
// Examples:
//   "sensors.temperature" = 1000 (exact match)
//   "sensors.*" = 100 (single wildcard)
//   "sensors.>" = 50 (greedy wildcard)
//   ">" = 1 (catch-all)
func (sr *StreamResolver) calculateSpecificity(filter string) int {
	// Exact match (no wildcards) - most specific
	if !strings.Contains(filter, "*") && !strings.Contains(filter, ">") {
		tokens := strings.Split(filter, ".")
		return 1000 + len(tokens)*10 // Longer exact matches are more specific
	}

	// Count tokens and wildcard types
	tokens := strings.Split(filter, ".")
	score := 0

	for _, token := range tokens {
		if token == ">" {
			// Greedy wildcard - least specific
			score += 1
		} else if token == "*" {
			// Single wildcard - somewhat specific
			score += 10
		} else {
			// Exact token - most specific
			score += 100
		}
	}

	return score
}

// subjectMatches checks if a subject matches a stream's subject filter
// Handles both exact matches and wildcard patterns
func (sr *StreamResolver) subjectMatches(subject, filter string) bool {
	// Exact match
	if subject == filter {
		return true
	}

	// Filter with wildcards - check if subject matches the filter pattern
	if strings.Contains(filter, "*") || strings.Contains(filter, ">") {
		return sr.matchPattern(subject, filter)
	}

	// If subject is a pattern, check if filter covers it
	// E.g., subject="sensors.*" should match filter="sensors.>"
	if strings.Contains(subject, "*") || strings.Contains(subject, ">") {
		return sr.patternCoveredBy(subject, filter)
	}

	return false
}

// matchPattern checks if a subject matches a NATS pattern
// subject: "sensors.temperature" filter: "sensors.>" → true
// subject: "sensors.temperature" filter: "sensors.*" → true
// subject: "sensors.temp.room1" filter: "sensors.*" → false
// subject: "sensors.temperature" filter: "$KV.bucket.>" → false
func (sr *StreamResolver) matchPattern(subject, pattern string) bool {
	subjectTokens := strings.Split(subject, ".")
	patternTokens := strings.Split(pattern, ".")

	// Check for greedy wildcard (>)
	for i, token := range patternTokens {
		if token == ">" {
			// Greedy wildcard matches everything from this point onwards
			// BUT we must verify all tokens BEFORE the > matched correctly
			
			// Need at least as many subject tokens as pattern tokens before the >
			if len(subjectTokens) < i {
				return false
			}
			
			// Verify all tokens before the > match exactly
			for j := 0; j < i; j++ {
				patternToken := patternTokens[j]
				subjectToken := subjectTokens[j]
				
				if patternToken == "*" {
					// Single wildcard matches any token
					continue
				}
				if patternToken != subjectToken {
					// Exact token must match
					return false
				}
			}
			
			// All tokens before > matched, greedy wildcard matches rest
			return true
		}
	}

	// No greedy wildcard - must match token by token
	if len(subjectTokens) != len(patternTokens) {
		return false
	}

	for i := 0; i < len(patternTokens); i++ {
		patternToken := patternTokens[i]
		subjectToken := subjectTokens[i]

		if patternToken == "*" {
			// Single wildcard matches any single token
			continue
		}
		if patternToken != subjectToken {
			// Exact token must match
			return false
		}
	}

	return true
}

// patternCoveredBy checks if a subject pattern is covered by a filter pattern
// subjectPattern: "sensors.*" filterPattern: "sensors.>" → true
// subjectPattern: "sensors.>" filterPattern: "sensors.*" → false
// subjectPattern: "sensors.*" filterPattern: ">" → true
func (sr *StreamResolver) patternCoveredBy(subjectPattern, filterPattern string) bool {
	// Root wildcard covers everything
	if filterPattern == ">" {
		return true
	}

	subjectTokens := strings.Split(subjectPattern, ".")
	filterTokens := strings.Split(filterPattern, ".")

	// Check if filter has greedy wildcard
	hasFilterGreedy := len(filterTokens) > 0 && filterTokens[len(filterTokens)-1] == ">"
	hasSubjectGreedy := len(subjectTokens) > 0 && subjectTokens[len(subjectTokens)-1] == ">"

	if hasFilterGreedy {
		// Filter with > covers if prefix matches
		filterPrefix := filterTokens[:len(filterTokens)-1]
		subjectPrefix := subjectTokens
		if hasSubjectGreedy {
			subjectPrefix = subjectTokens[:len(subjectTokens)-1]
		}

		// Check if subject prefix is covered by filter prefix
		if len(subjectPrefix) < len(filterPrefix) {
			return false
		}

		for i := 0; i < len(filterPrefix); i++ {
			if filterPrefix[i] != "*" && filterPrefix[i] != subjectPrefix[i] {
				return false
			}
		}
		return true
	}

	// Without filter greedy wildcard, subject greedy can't be covered
	if hasSubjectGreedy {
		return false
	}

	// Both are single-level patterns - must match exactly
	if len(subjectTokens) != len(filterTokens) {
		return false
	}

	for i := 0; i < len(filterTokens); i++ {
		if filterTokens[i] == "*" {
			continue // Filter * covers subject * or exact
		}
		if subjectTokens[i] == "*" {
			return false // Subject * not covered by exact filter token
		}
		if filterTokens[i] != subjectTokens[i] {
			return false
		}
	}

	return true
}

// getAllSubjectFilters returns all subject filters from all streams for error messages
func (sr *StreamResolver) getAllSubjectFilters() []string {
	filters := make([]string, 0)
	for _, stream := range sr.streams {
		for _, subject := range stream.Subjects {
			filters = append(filters, fmt.Sprintf("%s (stream: %s)", subject, stream.Name))
		}
	}
	return filters
}

// GetStreams returns all discovered streams (useful for debugging/logging)
func (sr *StreamResolver) GetStreams() []StreamInfo {
	return sr.streams
}

// GetStreamCount returns the number of discovered streams
func (sr *StreamResolver) GetStreamCount() int {
	return len(sr.streams)
}

// ValidateSubjects checks if all given subjects can be mapped to streams
// Returns an error with detailed information about unmapped subjects
func (sr *StreamResolver) ValidateSubjects(subjects []string) error {
	if !sr.discovered {
		return fmt.Errorf("streams not discovered - call Discover() first")
	}

	var unmappedSubjects []string
	for _, subject := range subjects {
		_, err := sr.FindStreamForSubject(subject)
		if err != nil {
			unmappedSubjects = append(unmappedSubjects, subject)
		}
	}

	if len(unmappedSubjects) > 0 {
		availableFilters := sr.getAllSubjectFilters()
		return fmt.Errorf(
			"cannot map %d subject(s) to streams: %v\n\nAvailable stream filters:\n  %s\n\nCreate streams with: nats stream add <NAME> --subjects \"<PATTERN>\"",
			len(unmappedSubjects),
			unmappedSubjects,
			strings.Join(availableFilters, "\n  "),
		)
	}

	sr.logger.Info("all subjects successfully mapped to streams", "subjectCount", len(subjects))
	return nil
}
