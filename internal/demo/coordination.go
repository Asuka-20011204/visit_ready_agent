package demo

import (
	"regexp"
	"strings"

	"visitready/internal/domain"
)

func coordinatedSymptomScope(quote string, target symptomRule) string {
	segments := strings.FieldsFunc(quote, func(char rune) bool {
		return strings.ContainsRune("，,、；;", char)
	})
	for index, segment := range segments {
		if !containsPositive(segment, target.tokens...) || !hasDifferentSymptom(segment, target) {
			continue
		}
		scope := []string{sharedCoordinationPrefix(segment)}
		for next := index + 1; next < len(segments); next++ {
			candidate := strings.TrimSpace(segments[next])
			if hasDifferentSymptom(candidate, target) && !isAssociationClause(candidate) {
				break
			}
			scope = append(scope, candidate)
		}
		return strings.Join(scope, "，")
	}
	return ""
}

func sharedCoordinationPrefix(segment string) string {
	firstSymptom := len(segment)
	for _, rule := range symptomRules {
		for _, token := range rule.tokens {
			index := strings.Index(segment, token)
			if index >= 0 && index < firstSymptom && containsPositive(segment, token) {
				firstSymptom = index
			}
		}
	}
	return strings.TrimSpace(segment[:firstSymptom])
}

func applySharedSymptomDetails(profile *domain.SymptomProfile, scope string) {
	if scope == "" {
		return
	}
	if profile.Onset == "" {
		profile.Onset = uniquePatternMatch(timePattern, scope)
	}
	if profile.Duration == "" {
		profile.Duration = uniquePatternMatch(durationPattern, scope)
	}
	if profile.Frequency == "" {
		profile.Frequency = uniquePatternMatch(frequencyPattern, scope)
	}
	if profile.Severity == "" {
		profile.Severity = uniquePatternMatch(severityPattern, scope)
	}
	if profile.Trigger == "" {
		profile.Trigger = uniquePatternMatch(triggerPattern, scope)
	}
	if profile.RelievingFactors == "" {
		profile.RelievingFactors = uniquePatternMatch(reliefPattern, scope)
	}
	if profile.Pattern == "" {
		profile.Pattern = uniqueContained(scope, "晚上睡觉", "夜间", "晚上", "睡觉")
	}
}

func uniquePatternMatch(pattern *regexp.Regexp, value string) string {
	matches := pattern.FindAllStringSubmatch(value, -1)
	unique := ""
	for _, match := range matches {
		if len(match) < 2 || match[1] == unique {
			continue
		}
		if unique != "" {
			return ""
		}
		unique = match[1]
	}
	return unique
}

func uniqueContained(value string, candidates ...string) string {
	match := ""
	for _, candidate := range candidates {
		if !strings.Contains(value, candidate) {
			continue
		}
		if match != "" && !strings.Contains(match, candidate) && !strings.Contains(candidate, match) {
			return ""
		}
		if len([]rune(candidate)) > len([]rune(match)) {
			match = candidate
		}
	}
	return match
}
