package demo

import (
	"context"
	"strings"
	"unicode"

	"visitready/internal/domain"
)

type Client struct{}

func (Client) Extract(_ context.Context, input string) (domain.Extraction, error) {
	quotes := sentences(strings.ReplaceAll(input, "\n补充信息：", "。"))
	facts := make([]domain.Fact, 0, len(quotes))
	for _, quote := range quotes {
		facts = append(facts, domain.Fact{
			Category:    category(quote),
			Content:     quote,
			SourceQuote: quote,
		})
		if len(facts) == 6 {
			break
		}
	}
	result := domain.Extraction{
		VisitGoal: "把症状变化和已有信息清楚地告诉医生",
		Facts:     facts,
	}
	if !strings.Contains(input, "补充信息：") && !strings.Contains(input, "体温") {
		result.MissingFields = []string{"是否测量过体温"}
		result.ClarificationQuestions = []string{"最近是否测量过体温？如有，请填写数值和时间。"}
		return result, nil
	}
	result.SearchQueries = []string{searchTopic(input) + " 就诊前准备"}
	return result, nil
}

func (Client) GenerateQuestions(_ context.Context, input domain.QuestionInput) (domain.QuestionSet, error) {
	questions := []domain.Question{
		{Text: "这些症状的时间变化中，哪些细节最需要向您说明？"},
		{Text: "是否需要进一步检查，检查前需要做哪些准备？"},
		{Text: "我目前记录的信息还有哪些重要遗漏？"},
	}
	if len(input.Sources) > 0 {
		questions[1].SourceURL = input.Sources[0].URL
	}
	return domain.QuestionSet{Questions: questions}, nil
}

func sentences(input string) []string {
	return strings.FieldsFunc(input, func(char rune) bool {
		return strings.ContainsRune("。！？!?；;\n", char)
	})
}

func category(value string) string {
	switch {
	case containsAny(value, "药", "服用", "剂量"):
		return "medication"
	case containsAny(value, "过敏"):
		return "allergy"
	case containsAny(value, "检查", "化验", "CT", "核磁"):
		return "test"
	case containsAny(value, "以前", "既往", "病史"):
		return "history"
	case containsAny(value, "天", "周", "月", "开始", "后来"):
		return "timeline"
	default:
		return "symptom"
	}
}

func searchTopic(input string) string {
	for _, candidate := range []string{"咳嗽", "头痛", "胃痛", "腹痛", "皮疹", "发热"} {
		if strings.Contains(input, candidate) {
			return candidate
		}
	}
	for _, field := range strings.FieldsFunc(input, func(char rune) bool {
		return unicode.IsSpace(char) || strings.ContainsRune("，。！？；,", char)
	}) {
		if len([]rune(field)) >= 2 && len([]rune(field)) <= 10 {
			return field
		}
	}
	return "门诊沟通"
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
