package agent

import (
	"strings"

	"visitready/internal/domain"
	"visitready/internal/guard"
)

// validateExtraction applies deterministic trust boundaries to patient-state
// fields. It deliberately does not require generated preparation questions or
// actions to be verbatim user quotes because they are not patient facts.
func validateExtraction(input string, candidate domain.Extraction, turns []domain.ClarificationTurn) (domain.Extraction, int) {
	validated := candidate
	accepted, rejected := guard.PartitionGroundedFacts(input, candidate.Facts)
	validated.Facts = deduplicateFacts(accepted)
	for index := range validated.Facts {
		validated.Facts[index].Content = strings.TrimSpace(validated.Facts[index].SourceQuote)
		validated.Facts[index].TimeLabel = ""
	}
	validated.SymptomProfiles = groundedSymptomProfiles(input, candidate.SymptomProfiles, turns)
	validated.Timeline = groundedTimeline(input, candidate.Timeline)
	validated.RiskSignals = guard.DetectEmergencySignals(input)
	validated.MissingFields = safeMissingFields(candidate.MissingFields)
	validated.VisitGoal = groundedVisitGoal(validated.SymptomProfiles)
	return validated, len(rejected)
}

func groundedVisitGoal(profiles []domain.SymptomProfile) string {
	names := make([]string, 0, min(len(profiles), 3))
	seen := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		name := strings.TrimSpace(profile.Name)
		if name == "" || len([]rune(name)) > 24 || guard.ContainsMedicalOverreach(name) || len(guard.ScanPII(name)) > 0 {
			continue
		}
		key := normalizeFactEvidence(name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		names = append(names, name)
		if len(names) == 3 {
			break
		}
	}
	if len(names) == 0 {
		return "整理本次情况并向医生说明"
	}
	return "整理" + strings.Join(names, "、") + "相关情况并向医生说明"
}

func validateQuestionSet(candidate domain.QuestionSet) domain.QuestionSet {
	validated := domain.QuestionSet{
		Questions:   make([]domain.Question, 0, len(candidate.Questions)),
		ActionItems: make([]domain.ActionItem, 0, len(candidate.ActionItems)),
	}
	for _, question := range candidate.Questions {
		question = sanitizeQuestion(question)
		if isSafeHealthQuestion(question.Text) {
			validated.Questions = append(validated.Questions, question)
		}
	}
	for _, item := range candidate.ActionItems {
		item.Title = strings.TrimSpace(item.Title)
		item.Detail = strings.TrimSpace(item.Detail)
		item.Reason = strings.TrimSpace(item.Reason)
		item.Category = strings.TrimSpace(item.Category)
		item.Priority = normalizePriority(item.Priority)
		combined := strings.Join([]string{item.Title, item.Detail, item.Reason}, " ")
		if item.Title == "" || item.Detail == "" || !isAllowedActionCategory(item.Category) ||
			len([]rune(combined)) > 640 || guard.ContainsMedicalOverreach(combined) || len(guard.ScanPII(combined)) > 0 {
			continue
		}
		validated.ActionItems = append(validated.ActionItems, item)
	}
	return validated
}
