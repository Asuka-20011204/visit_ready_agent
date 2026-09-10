package agent

import (
	"testing"

	"visitready/internal/domain"
)

func TestValidateExtractionRejectsUnsupportedPatientClaims(t *testing.T) {
	input := "我最近反复心悸，每次大约五分钟，希望整理后向医生说明。"
	candidate := domain.Extraction{
		VisitGoal: "整理心悸情况",
		Facts: []domain.Fact{
			{Category: "symptom", Content: "心悸", SourceQuote: "反复心悸"},
			{Category: "history", Content: "有甲状腺疾病", SourceQuote: "甲状腺疾病"},
		},
		SymptomProfiles: []domain.SymptomProfile{
			{Name: "心悸", Duration: "五分钟", SourceQuote: "反复心悸", EvidenceQuotes: []string{"每次大约五分钟"}},
			{Name: "胸痛", SourceQuote: "胸痛"},
		},
		Timeline: []domain.TimelineEvent{
			{TimeLabel: "最近", Event: "反复心悸", SourceQuote: "最近反复心悸"},
			{TimeLabel: "昨天", Event: "晕倒", SourceQuote: "昨天晕倒"},
		},
	}

	result, rejected := validateExtraction(input, candidate, nil)
	if rejected != 1 || len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, rejected = %d", result.Facts, rejected)
	}
	if len(result.SymptomProfiles) != 1 || result.SymptomProfiles[0].Name != "心悸" {
		t.Fatalf("symptom profiles = %#v", result.SymptomProfiles)
	}
	if len(result.Timeline) != 1 || result.Timeline[0].SourceQuote != "最近反复心悸" {
		t.Fatalf("timeline = %#v", result.Timeline)
	}
	if result.VisitGoal != "整理心悸相关情况并向医生说明" {
		t.Fatalf("visit goal was not derived from grounded symptoms: %q", result.VisitGoal)
	}
}

func TestValidateExtractionPreservesGroundedStructuredFactContent(t *testing.T) {
	input := "最近出现心悸发作，每次大约五分钟。"
	result, _ := validateExtraction(input, domain.Extraction{Facts: []domain.Fact{{
		Category: "symptom", Content: "心悸发作", SourceQuote: input,
	}}}, nil)
	if len(result.Facts) != 1 || result.Facts[0].Content != "心悸发作" {
		t.Fatalf("grounded structured content was replaced by raw quote: %#v", result.Facts)
	}
}

func TestValidateQuestionSetFiltersMedicalAndSensitiveBoundaryViolations(t *testing.T) {
	candidate := domain.QuestionSet{
		Questions: []domain.Question{
			{Text: "我还需要向医生说明哪些症状变化？", Reason: "完善沟通", Priority: domain.PriorityHigh, Category: "visit"},
			{Text: "是否需要记录症状持续多久？", Reason: "请联系 13800138000 补充", Priority: domain.PriorityHigh, Category: "duration"},
			{Text: "是否可以提供银行卡密码？", Reason: "收集账户信息", Priority: domain.PriorityUrgent, Category: "safety"},
			{Text: "是否应该马上吃两片药？", Reason: "给出治疗建议", Priority: domain.PriorityUrgent, Category: "medication"},
		},
		ActionItems: []domain.ActionItem{
			{Title: "记录症状变化", Detail: "记录发生时间和持续时长", Reason: "便于就诊沟通", Priority: domain.PriorityHigh, Category: "tracking"},
			{Title: "立即服药", Detail: "马上吃两片阿司匹林", Reason: "治疗症状", Priority: domain.PriorityUrgent, Category: "safety"},
			{Title: "先观察", Detail: "禁食三天观察症状是否缓解", Reason: "判断变化", Priority: domain.PriorityHigh, Category: "tracking"},
		},
	}

	result := validateQuestionSet(candidate)
	if len(result.Questions) != 2 || result.Questions[0].Category != "visit" || result.Questions[1].Reason != "" {
		t.Fatalf("questions = %#v", result.Questions)
	}
	if len(result.ActionItems) != 1 || result.ActionItems[0].Category != "tracking" {
		t.Fatalf("action items = %#v", result.ActionItems)
	}
}
