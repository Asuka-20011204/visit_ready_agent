package exporter

import (
	"errors"
	"fmt"
	"html"
	"strings"

	"visitready/internal/domain"
	"visitready/internal/guard"
)

var markdownEscaper = strings.NewReplacer(
	"\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "{", "\\{", "}", "\\}",
	"[", "\\[", "]", "\\]", "(", "\\(", ")", "\\)", "#", "\\#", "+", "\\+",
	"-", "\\-", ".", "\\.", "!", "\\!", "|", "\\|", ">", "\\>",
)

func Markdown(session domain.Session) ([]byte, error) {
	if session.Status == domain.StatusEmergency {
		return emergencyMarkdown(session), nil
	}
	if session.Status != domain.StatusCompleted {
		return nil, errors.New("session must be completed before export")
	}

	var output strings.Builder
	output.WriteString("# 我的诊前沟通清单\n\n")
	output.WriteString("> 仅用于整理就诊信息，不能替代医生诊断或治疗建议。紧急情况请立即联系当地急救服务。\n\n")
	writeSection(&output, "本次就诊目标", []string{session.VisitGoal})
	writeConversationSummary(&output, session.ConversationSummary)
	writeRiskSignals(&output, session.RiskSignals)
	writeSymptomProfiles(&output, session.SymptomProfiles)
	writeTimeline(&output, session.Timeline)
	writeGlobalHistory(&output, session)
	writeActionItems(&output, session.ActionItems)
	writeOpenInformation(&output, session.Uncertainties, session.Contradictions)

	output.WriteString("## 已核对的事实\n\n")
	for _, fact := range uniqueFacts(session.Facts) {
		fmt.Fprintf(&output, "- **%s**：%s", labelForCategory(fact.Category), cleanText(fact.Content))
		if fact.TimeLabel != "" {
			fmt.Fprintf(&output, "（%s）", cleanText(fact.TimeLabel))
		}
		output.WriteByte('\n')
		if strings.TrimSpace(fact.Content) != strings.TrimSpace(fact.SourceQuote) && strings.TrimSpace(fact.SourceQuote) != "" {
			fmt.Fprintf(&output, "  - 原文依据：%s\n", cleanText(fact.SourceQuote))
		}
	}
	output.WriteString("\n")

	output.WriteString("## 准备向医生询问\n\n")
	for _, question := range session.Questions {
		fmt.Fprintf(&output, "- [ ] **%s** %s\n", priorityLabel(question.Priority), cleanText(question.Text))
		if question.Reason != "" {
			fmt.Fprintf(&output, "  - 为什么问：%s\n", cleanText(question.Reason))
		}
	}
	output.WriteString("\n")

	if len(session.Sources) > 0 {
		output.WriteString("## 参考资料（不属于个人事实）\n\n")
		for _, source := range session.Sources {
			fmt.Fprintf(&output, "- %s（%s）：%s\n", cleanText(source.Title), cleanText(source.Domain), source.URL)
		}
		output.WriteString("\n")
	}
	return []byte(output.String()), nil
}

func emergencyMarkdown(session domain.Session) []byte {
	var output strings.Builder
	output.WriteString("# 紧急安全摘要\n\n")
	output.WriteString("> **")
	output.WriteString(cleanText(guard.EmergencyEscalationMessage))
	output.WriteString("**\n\n")
	output.WriteString("这不是诊断，也不应为了继续使用本工具而延迟寻求紧急帮助。\n\n")
	if len(session.RiskSignals) > 0 {
		output.WriteString("## 触发提醒的原文表现\n\n")
		for _, signal := range session.RiskSignals {
			fmt.Fprintf(&output, "- %s\n", cleanText(signal.SourceQuote))
		}
		output.WriteString("\n")
	}
	return []byte(output.String())
}

func writeConversationSummary(output *strings.Builder, summary domain.ConversationSummary) {
	if summary.Headline == "" && len(summary.Confirmed) == 0 {
		return
	}
	output.WriteString("## 我可以先这样告诉医生\n\n")
	if summary.Headline != "" {
		writeSentence(output, summary.Headline)
	}
	if len(summary.Confirmed) > 0 {
		output.WriteString("目前已明确：")
		writeSentence(output, strings.Join(summary.Confirmed, "；"))
	}
	output.WriteString("\n\n")
}

func writeSentence(output *strings.Builder, value string) {
	trimmed := strings.TrimSpace(value)
	output.WriteString(cleanText(trimmed))
	for _, punctuation := range []string{"。", "！", "？", ".", "!", "?"} {
		if strings.HasSuffix(trimmed, punctuation) {
			return
		}
	}
	output.WriteString("。")
}

func writeOpenInformation(output *strings.Builder, uncertainties []domain.Uncertainty, contradictions []domain.Contradiction) {
	if len(uncertainties) == 0 && len(contradictions) == 0 {
		return
	}
	output.WriteString("## 仍需确认的信息\n\n")
	for _, item := range uncertainties {
		fmt.Fprintf(output, "- **%s**：%s\n", cleanText(item.Topic), cleanText(item.Detail))
		if item.Why != "" {
			fmt.Fprintf(output, "  - 为什么重要：%s\n", cleanText(item.Why))
		}
	}
	for _, item := range contradictions {
		fmt.Fprintf(output, "- **%s存在不同说法**：%s / %s\n", cleanText(item.Topic), cleanText(item.FirstEvidence), cleanText(item.SecondEvidence))
		if item.Resolution != "" {
			fmt.Fprintf(output, "  - 你的选择：%s\n", cleanText(item.Resolution))
		} else {
			fmt.Fprintf(output, "  - 待确认：%s\n", cleanText(item.ClarifyingQuestion))
		}
	}
	output.WriteString("\n")
}

func writeRiskSignals(output *strings.Builder, signals []domain.RiskSignal) {
	if len(signals) == 0 {
		return
	}
	output.WriteString("## 优先安全提醒\n\n")
	output.WriteString("> 这是基于你明确提供的信息生成的就医优先级提醒，不是诊断。\n\n")
	for _, signal := range signals {
		fmt.Fprintf(output, "- **%s · %s**：%s\n", priorityLabel(signal.Priority), cleanText(signal.Title), cleanText(signal.Guidance))
		fmt.Fprintf(output, "  - 已报告依据：%s（原文：%s）\n", cleanText(signal.Evidence), cleanText(signal.SourceQuote))
	}
	output.WriteString("\n")
}

func writeSymptomProfiles(output *strings.Builder, profiles []domain.SymptomProfile) {
	if len(profiles) == 0 {
		return
	}
	output.WriteString("## 症状画像\n\n")
	for _, profile := range profiles {
		details := make([]string, 0, 7)
		appendDetail := func(label, value string) {
			if value != "" {
				details = append(details, label+"："+cleanText(value))
			}
		}
		appendDetail("开始", profile.Onset)
		appendDetail("每次持续", profile.Duration)
		appendDetail("频率", profile.Frequency)
		appendDetail("程度", profile.Severity)
		appendDetail("规律", profile.Pattern)
		appendDetail("诱因", profile.Trigger)
		appendDetail("缓解因素", profile.RelievingFactors)
		fmt.Fprintf(output, "- **%s**", cleanText(profile.Name))
		if len(details) > 0 {
			fmt.Fprintf(output, "：%s", strings.Join(details, "；"))
		}
		output.WriteString("\n")
		if len(profile.AssociatedSymptoms) > 0 {
			fmt.Fprintf(output, "  - 伴随表现：%s\n", cleanText(strings.Join(profile.AssociatedSymptoms, "、")))
		}
		fmt.Fprintf(output, "  - 原文依据：%s\n", cleanText(profile.SourceQuote))
		for _, quote := range profile.EvidenceQuotes {
			fmt.Fprintf(output, "  - 补充依据：%s\n", cleanText(quote))
		}
	}
	output.WriteString("\n")
}

// writeGlobalHistory surfaces medication, allergy, history, measurement, test
// and explicit denials the user provided during clarification, so they are not
// written but never shown.
func writeGlobalHistory(output *strings.Builder, session domain.Session) {
	sections := []struct {
		title  string
		values []string
	}{
		{"用药", session.Medications},
		{"过敏", session.Allergies},
		{"既往疾病", session.ChronicConditions},
		{"外伤或手术", session.TraumaHistory},
		{"测量数值", session.Measurements},
		{"检查情况", session.Tests},
		{"安全相关", session.SafetyNotes},
		{"已否认的情况", session.DeniedConditions},
	}
	any := false
	for _, section := range sections {
		if len(section.values) > 0 {
			any = true
			break
		}
	}
	if !any {
		return
	}
	output.WriteString("## 用药、过敏与既往情况\n\n")
	for _, section := range sections {
		if len(section.values) == 0 {
			continue
		}
		output.WriteString("- **" + section.title + "**：")
		output.WriteString(cleanText(strings.Join(section.values, "；")))
		output.WriteString("\n")
	}
	output.WriteString("\n")
}

func writeTimeline(output *strings.Builder, timeline []domain.TimelineEvent) {
	if len(timeline) == 0 {
		return
	}
	output.WriteString("## 就诊时间线\n\n")
	for _, event := range timeline {
		fmt.Fprintf(output, "- **%s**：%s\n", cleanText(event.TimeLabel), cleanText(event.Event))
		if strings.TrimSpace(event.Event) != strings.TrimSpace(event.SourceQuote) && strings.TrimSpace(event.SourceQuote) != "" {
			fmt.Fprintf(output, "  - 原文依据：%s\n", cleanText(event.SourceQuote))
		}
	}
	output.WriteString("\n")
}

func writeActionItems(output *strings.Builder, items []domain.ActionItem) {
	if len(items) == 0 {
		return
	}
	output.WriteString("## 就诊前行动清单\n\n")
	for _, item := range items {
		fmt.Fprintf(output, "- [ ] **%s · %s**：%s\n", priorityLabel(item.Priority), cleanText(item.Title), cleanText(item.Detail))
		fmt.Fprintf(output, "  - 目的：%s\n", cleanText(item.Reason))
	}
	output.WriteString("\n")
}

func uniqueFacts(facts []domain.Fact) []domain.Fact {
	result := make([]domain.Fact, 0, len(facts))
	seen := make(map[string]struct{}, len(facts))
	for _, fact := range facts {
		key := strings.ToLower(strings.Join(strings.Fields(fact.Content+"\x1f"+fact.SourceQuote), " "))
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, fact)
	}
	return result
}

func priorityLabel(priority domain.Priority) string {
	switch priority {
	case domain.PriorityUrgent:
		return "紧急优先"
	case domain.PriorityHigh:
		return "优先"
	default:
		return "常规"
	}
}

func writeSection(output *strings.Builder, title string, values []string) {
	output.WriteString("## " + title + "\n\n")
	for _, value := range values {
		if value != "" {
			output.WriteString(cleanText(value) + "\n")
		}
	}
	output.WriteString("\n")
}

func cleanText(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return markdownEscaper.Replace(html.EscapeString(strings.TrimSpace(value)))
}

func labelForCategory(category string) string {
	labels := map[string]string{
		"symptom": "症状", "timeline": "时间变化", "medication": "用药",
		"allergy": "过敏", "test": "检查", "history": "既往情况", "other": "其他",
	}
	if label, ok := labels[category]; ok {
		return label
	}
	return "其他"
}
