// file: internal/broker/stream_resolver.go

package broker

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"rule-router/internal/logger"
)

// Timeout constants for stream resolver operations
const (
	// streamDiscoveryTimeout is the maximum time to wait for JetStream stream discovery
	streamDiscoveryTimeout = 30 * time.Second
)

// StreamResolver discovers JetStream streams and maps subjects to streams
// Now supports mirrors and sourced streams with intelligent storage-aware selection
type StreamResolver struct {
	jetStream  jetstream.JetStream
	streams    []StreamInfo
	logger     *logger.Logger
	discovered bool
}

// StreamInfo holds comprehensive information about a JetStream stream
// including mirror/source configuration and storage type
type StreamInfo struct {
	Name          string                 // Stream name (e.g., "SENSORS")
	Subjects      []string               // Primary subject filters (only for non-mirror/source streams)
	MirrorFilter  string                 // Mirror's FilterSubject (if stream is a mirror)
	SourceFilters []string               // Source FilterSubjects (if stream has sources)
	Storage       jetstream.StorageType  // Memory or File storage
	IsMirror      bool                   // True if this is a mirror stream
	IsSource      bool                   // True if this stream has sources
}

// streamMatch represents a potential stream match with its properties for sorting.
type streamMatch struct {
	streamName  string
	filter      string
	specificity int                   // Subject pattern specificity score
	storage     jetstream.StorageType // Memory or File
	isMirror    bool                  // Is this a mirror stream?
	isSource    bool                  // Is this a sourced stream?
	isSystem    bool                  // Is this a system stream ($*, KV_*)?
}

// NewStreamResolver creates a new stream resolver
func NewStreamResolver(js jetstream.JetStream, logger *logger.Logger) *StreamResolver {
	return &StreamResolver{
		jetStream:  js,
		streams:    make([]StreamInfo, 0),
		logger:     logger,
		discovered: false,
	}
}

// Discover queries NATS JetStream for all available streams including mirrors and sources
// Reads subject filters from primary streams, mirror configurations, and source configurations
func (sr *StreamResolver) Discover(ctx context.Context) error {
	sr.logger.Info("discovering JetStream streams with mirror/source support")

	// Use context with timeout for discovery
	discoverCtx, cancel := context.WithTimeout(ctx, streamDiscoveryTimeout)
	defer cancel()

	// Get stream lister using the new API
	streamLister := sr.jetStream.ListStreams(discoverCtx)

	streamNames := make([]string, 0)

	// Iterate over stream info
	for info := range streamLister.Info() {
		streamNames = append(streamNames, info.Config.Name)

		streamInfo := StreamInfo{
			Name:    info.Config.Name,
			Storage: info.Config.Storage, // Memory or File
		}

		// Primary stream: has Config.Subjects defined
		if len(info.Config.Subjects) > 0 {
			streamInfo.Subjects = info.Config.Subjects
			sr.logger.Debug("discovered primary stream",
				"name", info.Config.Name,
				"storage", info.Config.Storage,
				"subjects", info.Config.Subjects,
				"messages", info.State.Msgs)
		}

		// Mirror stream: read filter from mirror configuration
		if info.Config.Mirror != nil {
			streamInfo.IsMirror = true

			// Mirror filter subject (defaults to ">" if not specified)
			filter := info.Config.Mirror.FilterSubject
			if filter == "" {
				filter = ">" // Default: mirror everything from source
			}
			streamInfo.MirrorFilter = filter

			sr.logger.Debug("discovered mirror stream",
				"name", info.Config.Name,
				"storage", info.Config.Storage,
				"mirrorFilter", filter,
				"sourceName", info.Config.Mirror.Name,
				"messages", info.State.Msgs)
		}

		// Sourced stream: read filters from each source
		if len(info.Config.Sources) > 0 {
			streamInfo.IsSource = true
			streamInfo.SourceFilters = make([]string, 0, len(info.Config.Sources))

			for _, source := range info.Config.Sources {
				filter := source.FilterSubject
				if filter == "" {
					filter = ">" // Default: source everything
				}
				streamInfo.SourceFilters = append(streamInfo.SourceFilters, filter)

				sr.logger.Debug("discovered stream with source",
					"name", info.Config.Name,
					"storage", info.Config.Storage,
					"sourceName", source.Name,
					"sourceFilter", filter)
			}
		}

		// Store the stream info
		sr.streams = append(sr.streams, streamInfo)

		// Summary log for this stream
		sr.logger.Info("stream discovered",
			"name", streamInfo.Name,
			"storage", streamInfo.Storage,
			"type", sr.getStreamType(streamInfo),
			"filterCount", sr.getFilterCount(streamInfo))
	}

	// Check for errors during iteration
	if streamLister.Err() != nil {
		return fmt.Errorf("error during stream discovery: %w", streamLister.Err())
	}

	if len(sr.streams) == 0 {
		sr.logger.Warn("no JetStream streams found - rules will fail to initialize")
		return fmt.Errorf("no JetStream streams found - please create streams before starting rule-router")
	}

	sr.discovered = true

	// Log comprehensive discovery summary
	sr.logger.Info("stream discovery complete",
		"totalStreams", len(sr.streams),
		"streamNames", streamNames,
		"memoryStreams", sr.countByStorage(jetstream.MemoryStorage),
		"fileStreams", sr.countByStorage(jetstream.FileStorage),
		"mirrorStreams", sr.countMirrors(),
		"sourcedStreams", sr.countSources())

	return nil
}

// FindStreamForSubject finds the optimal stream for the given subject by collecting
// all potential matches and sorting them by a series of explicit priority rules.
func (sr *StreamResolver) FindStreamForSubject(subject string) (string, error) {
	if !sr.discovered {
		return "", fmt.Errorf("streams not discovered - call Discover() first")
	}

	sr.logger.Debug("finding optimal stream for subject", "subject", subject)

	// Collect all matching streams with their filters
	var matches []streamMatch

	// Check each stream and its subject filters
	for _, stream := range sr.streams {
		// Check primary stream subjects
		for _, filter := range stream.Subjects {
			if sr.subjectMatches(subject, filter) {
				matches = append(matches, streamMatch{
					streamName:  stream.Name,
					filter:      filter,
					specificity: sr.calculateSpecificity(filter),
					storage:     stream.Storage,
					isMirror:    false,
					isSource:    false,
					isSystem:    sr.isSystemStream(stream.Name),
				})
			}
		}

		// Check mirror filter
		if stream.IsMirror && stream.MirrorFilter != "" {
			if sr.subjectMatches(subject, stream.MirrorFilter) {
				matches = append(matches, streamMatch{
					streamName:  stream.Name,
					filter:      stream.MirrorFilter,
					specificity: sr.calculateSpecificity(stream.MirrorFilter),
					storage:     stream.Storage,
					isMirror:    true,
					isSource:    false,
					isSystem:    sr.isSystemStream(stream.Name),
				})
			}
		}

		// Check source filters
		if stream.IsSource {
			for _, filter := range stream.SourceFilters {
				if sr.subjectMatches(subject, filter) {
					matches = append(matches, streamMatch{
						streamName:  stream.Name,
						filter:      filter,
						specificity: sr.calculateSpecificity(filter),
						storage:     stream.Storage,
						isMirror:    false,
						isSource:    true,
						isSystem:    sr.isSystemStream(stream.Name),
					})
				}
			}
		}
	}

	if len(matches) == 0 {
		// No stream found
		availableFilters := sr.getAllSubjectFilters()
		return "", fmt.Errorf("no stream found for subject '%s' - available stream filters: %v",
			subject, availableFilters)
	}

	// If only one stream matches, no need to sort.
	if len(matches) == 1 {
		sr.logger.Info("selected optimal stream for subject (only one match)",
			"subject", subject,
			"selectedStream", matches[0].streamName,
			"filter", matches[0].filter,
			"storage", matches[0].storage)
		return matches[0].streamName, nil
	}

	// Sort the matches based on explicit priority rules.
	// The 'less' function returns true if item 'i' is better than item 'j'.
	sort.Slice(matches, func(i, j int) bool {
		a := matches[i]
		b := matches[j]

		// Rule 1: More specific subject filter is better (higher score is better).
		if a.specificity != b.specificity {
			return a.specificity > b.specificity
		}

		// Rule 2: Memory storage is better than File storage.
		if a.storage != b.storage {
			return a.storage == jetstream.MemoryStorage
		}

		// Rule 3: Primary streams are better than Mirrors or Sourced streams.
		// A primary stream has both isMirror and isSource as false.
		aIsPrimary := !a.isMirror && !a.isSource
		bIsPrimary := !b.isMirror && !b.isSource
		if aIsPrimary != bIsPrimary {
			return aIsPrimary
		}

		// Rule 4: Non-system streams are better than system streams.
		if a.isSystem != b.isSystem {
			return !a.isSystem
		}

		// If all rules are equal, the order doesn't matter.
		return false
	})

	// The best match is now the first element in the sorted slice.
	bestMatch := matches[0]

	// Log selection decision with reasoning
	sr.logger.Info("selected optimal stream for subject",
		"subject", subject,
		"selectedStream", bestMatch.streamName,
		"filter", bestMatch.filter,
		"storage", bestMatch.storage,
		"isMirror", bestMatch.isMirror,
		"isSource", bestMatch.isSource,
		"specificity", bestMatch.specificity,
		"totalMatches", len(matches),
		"reason", sr.explainSelection(&bestMatch))

	// Debug: log all alternatives considered
	alternatives := make([]string, 0, len(matches)-1)
	for i := 1; i < len(matches); i++ {
		alt := matches[i]
		alternatives = append(alternatives, fmt.Sprintf("%s(spec:%d,storage:%s)",
			alt.streamName, alt.specificity, alt.storage))
	}
	sr.logger.Debug("alternative streams considered",
		"subject", subject,
		"alternatives", alternatives)

	return bestMatch.streamName, nil
}

// explainSelection provides human-readable reasoning for stream selection
func (sr *StreamResolver) explainSelection(match *streamMatch) string {
	reasons := make([]string, 0, 4)

	// Storage explanation
	if match.storage == jetstream.MemoryStorage {
		reasons = append(reasons, "memory-storage")
	} else {
		reasons = append(reasons, "file-storage")
	}

	// Specificity explanation
	reasons = append(reasons, fmt.Sprintf("specificity(%d)", match.specificity))

	// Stream type explanation
	if match.isMirror {
		reasons = append(reasons, "mirror")
	} else if match.isSource {
		reasons = append(reasons, "sourced")
	} else {
		reasons = append(reasons, "primary")
	}

	// System stream penalty
	if match.isSystem {
		reasons = append(reasons, "system-stream-deprioritized")
	}

	return strings.Join(reasons, ", ")
}

// calculateSpecificity returns a score indicating how specific a subject filter is
// Higher score = more specific
// Examples:
//   "sensors.temperature.room1" = 1030 (exact match, 3 tokens)
//   "sensors.temperature.*" = 310 (exact + single wildcard)
//   "sensors.>" = 101 (exact + greedy wildcard)
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

// isSystemStream checks if a stream is a system stream (should be deprioritized)
func (sr *StreamResolver) isSystemStream(name string) bool {
	return strings.HasPrefix(name, "$") || strings.HasPrefix(name, "KV_")
}

// getAllSubjectFilters returns all subject filters from all streams for error messages
func (sr *StreamResolver) getAllSubjectFilters() []string {
	filters := make([]string, 0)
	for _, stream := range sr.streams {
		// Primary subjects
		for _, subject := range stream.Subjects {
			filters = append(filters, fmt.Sprintf("%s (stream: %s, storage: %s)",
				subject, stream.Name, stream.Storage))
		}
		// Mirror filter
		if stream.MirrorFilter != "" {
			filters = append(filters, fmt.Sprintf("%s (mirror: %s, storage: %s)",
				stream.MirrorFilter, stream.Name, stream.Storage))
		}
		// Source filters
		for _, filter := range stream.SourceFilters {
			filters = append(filters, fmt.Sprintf("%s (sourced: %s, storage: %s)",
				filter, stream.Name, stream.Storage))
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
			"cannot map %d subject(s) to streams: %v\n\nAvailable stream filters:\n  %s\n\nCreate streams with: nats stream add <NAME> --subjects \"<PATTERN>\"\nOr create mirrors with: nats stream add <NAME> --mirror <SOURCE> --mirror-filter \"<PATTERN>\"",
			len(unmappedSubjects),
			unmappedSubjects,
			strings.Join(availableFilters, "\n  "),
		)
	}

	sr.logger.Info("all subjects successfully mapped to streams", "subjectCount", len(subjects))
	return nil
}

// Helper methods for logging and statistics

func (sr *StreamResolver) getStreamType(stream StreamInfo) string {
	if stream.IsMirror {
		return "mirror"
	}
	if stream.IsSource {
		return "sourced"
	}
	if len(stream.Subjects) > 0 {
		return "primary"
	}
	return "unknown"
}

func (sr *StreamResolver) getFilterCount(stream StreamInfo) int {
	count := len(stream.Subjects)
	if stream.MirrorFilter != "" {
		count++
	}
	count += len(stream.SourceFilters)
	return count
}

func (sr *StreamResolver) countByStorage(storage jetstream.StorageType) int {
	count := 0
	for _, stream := range sr.streams {
		if stream.Storage == storage {
			count++
		}
	}
	return count
}

func (sr *StreamResolver) countMirrors() int {
	count := 0
	for _, stream := range sr.streams {
		if stream.IsMirror {
			count++
		}
	}
	return count
}

func (sr *StreamResolver) countSources() int {
	count := 0
	for _, stream := range sr.streams {
		if stream.IsSource {
			count++
		}
	}
	return count
}

