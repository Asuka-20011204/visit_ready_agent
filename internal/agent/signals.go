package agent

import (
	"regexp"
	"strings"

	"visitready/internal/domain"
)

var (
	durationValuePattern  = regexp.MustCompile(`每次(?:约|大约)?[一二两三四五六七八九十半\d]+(?:秒钟?|分钟?|小时)`)
	frequencyValuePattern = regexp.MustCompile(`(?:每天|每晚|每周|一周|一天)[一二两三四五六七八九十\d]+次`)
	onsetValuePattern     = regexp.MustCompile(`(?:今天|昨天|前天|(?:近|最近)?[一二两三四五六七八九十\d]+(?:天|周|个月|月|年)(?:前|来))`)
)

func deriveConversationSignals(session domain.Session) (domain.ConversationSummary, []domain.Uncertainty, []domain.Contradiction) {
	uncertainties := make([]domain.Uncertainty, 0, len(session.MissingFields)+3)
	for _, field := range session.MissingFields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		uncertainties = append(uncertainties, domain.Uncertainty{
			Topic: field, Detail: "目前没有足够的原文信息", Why: uncertaintyWhy(field),
			Status: "unknown", Priority: uncertaintyPriority(field),
		})
	}
	for _, turn := range session.ClarificationTurns {
		if isUncertainAnswer(turn.Answer) {
			uncertainties = append(uncertainties, domain.Uncertainty{Topic: "补充回答", Detail: "你表示这项回忆不确定或可能存在多种情况", Why: "Agent 不会替你选择其中一种说法，原始回答仍只用于当前临时会话分析", Status: "unconfirmed", Priority: domain.PriorityHigh})
		}
		if !isSkipAnswer(turn.Answer) {
			continue
		}
		for _, question := range turn.Questions {
			if strings.TrimSpace(question.Text) == "" {
				continue
			}
			uncertainties = append(uncertainties, domain.Uncertainty{
				Topic: question.Category, Detail: question.Text, Why: "你选择暂不提供，已保留为就诊时可补充项",
				Status: "skipped", Priority: question.Priority,
			})
		}
	}
	contradictions := findContradictions(session)
	confirmed := make([]string, 0, 5)
	for _, profile := range session.SymptomProfiles {
		for _, detail := range []string{profile.Onset, profile.Duration, profile.Frequency, profile.Severity, profile.Pattern, profile.Trigger, profile.RelievingFactors} {
			if strings.TrimSpace(detail) != "" {
				confirmed = append(confirmed, profile.Name+"："+detail)
			}
			if len(confirmed) == 5 {
				break
			}
		}
	}
	open := make([]string, 0, len(uncertainties)+len(contradictions))
	for _, item := range uncertainties {
		open = append(open, item.Topic)
	}
	for _, item := range contradictions {
		open = append(open, item.Topic+"存在不同说法")
	}
	subject := symptomSubject(session)
	headline := "我这次主要想说明" + subject + "。"
	summary := domain.ConversationSummary{
		Headline:    headline,
		Intent:      session.VisitGoal,
		Confirmed:   confirmed,
		OpenThreads: open,
	}
	return summary, uniqueUncertainties(uncertainties), contradictions
}

func isUncertainAnswer(answer string) bool {
	for _, phrase := range []string{"不确定", "可能", "记不清", "想不起来", "大概"} {
		if strings.Contains(answer, phrase) {
			return true
		}
	}
	return false
}

func deriveInterviewState(session domain.Session, uncertainties []domain.Uncertainty, contradictions []domain.Contradiction) domain.InterviewState {
	confirmed := 0
	for _, profile := range session.SymptomProfiles {
		for _, value := range []string{profile.Onset, profile.Duration, profile.Frequency, profile.Severity, profile.Pattern, profile.Trigger, profile.RelievingFactors} {
			if strings.TrimSpace(value) != "" {
				confirmed++
			}
		}
	}
	state := domain.InterviewState{
		TurnCount: len(session.ClarificationTurns), MaxTurns: maxClarificationRounds,
		ConfirmedCount: confirmed, OpenCount: len(uncertainties) + len(contradictions),
	}
	switch {
	case len(contradictions) > 0:
		state.CompletionReason = "conflict_needs_clarification"
	case len(uncertainties) == 0:
		state.CompletionReason = "sufficient_context"
	case session.ClarificationCount >= maxClarificationRounds:
		state.CompletionReason = "interview_budget_reached"
	}
	return state
}

func findContradictions(session domain.Session) []domain.Contradiction {
	texts := []string{session.RawInput}
	for _, turn := range session.ClarificationTurns {
		texts = append(texts, turn.Answer)
	}
	result := make([]domain.Contradiction, 0, 3)
	for _, profile := range session.SymptomProfiles {
		for _, field := range []struct {
			name    string
			label   string
			pattern *regexp.Regexp
		}{
			{name: "duration", label: "每次持续时间", pattern: durationValuePattern},
			{name: "frequency", label: "发作频率", pattern: frequencyValuePattern},
			{name: "onset", label: "开始时间", pattern: onsetValuePattern},
		} {
			values := make([]string, 0, len(texts))
			for _, text := range texts {
				for _, clause := range relevantClauses(text, profile.Name) {
					for _, value := range field.pattern.FindAllString(clause, -1) {
						if !containsString(values, value) {
							values = append(values, value)
						}
					}
				}
			}
			if len(values) > 1 {
				if contradictionWasResolved(session.ClarificationTurns, values) {
					continue
				}
				result = append(result, domain.Contradiction{
					Topic: profile.Name + "·" + field.label, FirstEvidence: values[0], SecondEvidence: values[1],
					ClarifyingQuestion: "关于" + profile.Name + "的" + field.label + "，哪种说法更接近实际；如果两种情况都会发生，需要如何分别记录？",
					Priority:           domain.PriorityHigh,
				})
			}
			if len(result) == 3 {
				return result
			}
		}
	}
	return result
}

func contradictionWasResolved(turns []domain.ClarificationTurn, values []string) bool {
	for index := len(turns) - 1; index >= 0; index-- {
		answer := turns[index].Answer
		if !containsAnyPhrase(answer, "为准", "说错了", "记错了", "更正为") {
			continue
		}
		for _, value := range values {
			if strings.Contains(answer, value) {
				return true
			}
		}
	}
	return false
}

func containsAnyPhrase(value string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(value, phrase) {
			return true
		}
	}
	return false
}

func relevantClauses(text, profileName string) []string {
	clauses := strings.FieldsFunc(text, func(char rune) bool { return strings.ContainsRune("。！？!?；;，,\n", char) })
	result := make([]string, 0, len(clauses))
	for _, clause := range clauses {
		if strings.Contains(clause, profileName) || !mentionsKnownSymptom(clause) {
			result = append(result, clause)
		}
	}
	return result
}

func mentionsKnownSymptom(text string) bool {
	for _, term := range []string{"心悸", "心慌", "头痛", "头疼", "发热", "发烧", "咳嗽", "腹痛", "胃痛", "皮疹", "头晕"} {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

func uncertaintyWhy(field string) string {
	if strings.Contains(field, "胸痛") || strings.Contains(field, "晕厥") || strings.Contains(field, "呼吸") {
		return "这项信息会影响与医护人员沟通时的优先级"
	}
	if strings.Contains(field, "开始") || strings.Contains(field, "持续") || strings.Contains(field, "频率") {
		return "这项信息能让医生更快了解症状的时间变化"
	}
	return "补上这项信息可以减少就诊时的遗漏"
}

func uncertaintyPriority(field string) domain.Priority {
	if strings.Contains(field, "胸痛") || strings.Contains(field, "晕厥") || strings.Contains(field, "呼吸") {
		return domain.PriorityUrgent
	}
	if strings.Contains(field, "开始") || strings.Contains(field, "持续") || strings.Contains(field, "频率") || strings.Contains(field, "体温") {
		return domain.PriorityHigh
	}
	return domain.PriorityNormal
}

func isSkipAnswer(answer string) bool {
	normalized := strings.TrimSpace(strings.Trim(answer, "。！？!?；;，,"))
	if strings.Contains(normalized, "跳过剩余") || strings.Contains(normalized, "不再追问") || strings.Contains(normalized, "先跳过") || strings.Contains(normalized, "跳过这个") || strings.Contains(normalized, "跳过此项") {
		return true
	}
	for _, phrase := range []string{"跳过", "不清楚", "不知道", "无法提供", "记不清", "暂不回答"} {
		if normalized == phrase || normalized == "全部"+phrase || normalized == "这些都"+phrase {
			return true
		}
	}
	return false
}

func containsString(values []string, value string) bool {
	for _, existing := range values {
		if existing == value {
			return true
		}
	}
	return false
}

func uniqueUncertainties(values []domain.Uncertainty) []domain.Uncertainty {
	result := make([]domain.Uncertainty, 0, len(values))
	seen := make(map[string]struct{})
	for _, value := range values {
		key := value.Status + "\x1f" + value.Topic + "\x1f" + value.Detail
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}
