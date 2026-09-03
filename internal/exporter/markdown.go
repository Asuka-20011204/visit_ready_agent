package exporter

import (
	"errors"
	"fmt"
	"html"
	"strings"

	"visitready/internal/domain"
)

var markdownEscaper = strings.NewReplacer(
	"\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "{", "\\{", "}", "\\}",
	"[", "\\[", "]", "\\]", "(", "\\(", ")", "\\)", "#", "\\#", "+", "\\+",
	"-", "\\-", ".", "\\.", "!", "\\!", "|", "\\|", ">", "\\>",
)

func Markdown(session domain.Session) ([]byte, error) {
	if session.Status != domain.StatusCompleted {
		return nil, errors.New("session must be completed before export")
	}

	var output strings.Builder
	output.WriteString("# 我的诊前沟通清单\n\n")
	output.WriteString("> 仅用于整理就诊信息，不能替代医生诊断或治疗建议。紧急情况请立即联系当地急救服务。\n\n")
	writeSection(&output, "本次就诊目标", []string{session.VisitGoal})

	output.WriteString("## 已核对的事实\n\n")
	for _, fact := range session.Facts {
		fmt.Fprintf(&output, "- **%s**：%s", labelForCategory(fact.Category), cleanText(fact.Content))
		if fact.TimeLabel != "" {
			fmt.Fprintf(&output, "（%s）", cleanText(fact.TimeLabel))
		}
		fmt.Fprintf(&output, "\n  - 原文依据：%s\n", cleanText(fact.SourceQuote))
	}
	output.WriteString("\n")

	output.WriteString("## 准备向医生询问\n\n")
	for _, question := range session.Questions {
		fmt.Fprintf(&output, "- [ ] %s\n", cleanText(question.Text))
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
