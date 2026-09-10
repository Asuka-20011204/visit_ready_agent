package exporter_test

import (
	"strings"
	"testing"

	"visitready/internal/domain"
	"visitready/internal/exporter"
)

func TestMarkdownRequiresCompletedSession(t *testing.T) {
	_, err := exporter.Markdown(domain.Session{Status: domain.StatusWaitingReview})
	if err == nil {
		t.Fatal("Markdown() expected an error")
	}
}

func TestMarkdownExportsEmergencySessionWithoutReview(t *testing.T) {
	data, err := exporter.Markdown(domain.Session{
		Status:           domain.StatusEmergency,
		EmergencyMessage: "这些表现正在发生或明显加重，请立即联系当地急救服务或前往急诊。",
		RiskSignals:      []domain.RiskSignal{{Priority: domain.PriorityUrgent, Title: "需要立即处理的表现", Evidence: "胸痛和呼吸困难", SourceQuote: "胸痛和呼吸困难"}},
	})
	if err != nil {
		t.Fatalf("Markdown() error = %v", err)
	}
	output := string(data)
	if !strings.Contains(output, "# 紧急安全摘要") || !strings.Contains(output, "请立即联系当地急救服务或前往急诊") {
		t.Fatalf("emergency export = %s", output)
	}
	if strings.Contains(output, "准备向医生询问") {
		t.Fatalf("emergency export contains normal workflow: %s", output)
	}
}

func TestMarkdownSeparatesFactsQuestionsAndReferences(t *testing.T) {
	session := domain.Session{
		Status:    domain.StatusCompleted,
		VisitGoal: "向医生说明咳嗽变化 <script>alert(1)</script> ![remote](https://attacker.example/pixel)",
		Facts: []domain.Fact{{
			Category: "symptom", Content: "咳嗽三天", SourceQuote: "三天前开始咳嗽", TimeLabel: "三天前",
		}},
		SymptomProfiles: []domain.SymptomProfile{{Name: "咳嗽", Onset: "三天前", Pattern: "夜间更明显", SourceQuote: "三天前开始咳嗽，夜间更明显", EvidenceQuotes: []string{"补充回答：每晚咳嗽两次"}}},
		Timeline:        []domain.TimelineEvent{{TimeLabel: "三天前", Event: "开始咳嗽", SourceQuote: "三天前开始咳嗽"}},
		RiskSignals: []domain.RiskSignal{{
			Priority: domain.PriorityUrgent, Title: "需要优先处理的伴随表现", Evidence: "呼吸困难", Guidance: "如果正在发生或明显加重，请立即联系当地急救服务。", SourceQuote: "呼吸困难",
		}},
		Questions:   []domain.Question{{Text: "咳嗽期间需要记录哪些变化？", Reason: "帮助医生了解症状规律", Priority: domain.PriorityHigh, SourceURL: "https://www.who.int/example"}},
		ActionItems: []domain.ActionItem{{Title: "记录咳嗽变化", Detail: "记录发生时段和每次持续多久", Reason: "形成可供医生快速查看的症状记录", Priority: domain.PriorityHigh, Category: "tracking"}},
		Sources:     []domain.Source{{Title: "WHO 就诊资料", Domain: "who.int", URL: "https://www.who.int/example"}},
		RawInput:    "这段原始输入不应出现在导出文件中",
	}

	data, err := exporter.Markdown(session)
	if err != nil {
		t.Fatalf("Markdown() error = %v", err)
	}
	text := string(data)
	for _, want := range []string{"# 我的诊前沟通清单", "向医生说明咳嗽变化", "症状画像", "三天前", "补充回答：每晚咳嗽两次", "就诊时间线", "优先安全提醒", "呼吸困难", "就诊前行动清单", "记录咳嗽变化", "咳嗽期间需要记录哪些变化？", "为什么问：帮助医生了解症状规律", "WHO 就诊资料", "仅用于整理就诊信息"} {
		if !strings.Contains(text, want) {
			t.Fatalf("markdown missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, session.RawInput) {
		t.Fatal("markdown leaked raw input")
	}
	if strings.Contains(text, "<script>") || !strings.Contains(text, "&lt;script&gt;") {
		t.Fatalf("markdown did not escape inline HTML:\n%s", text)
	}
	if strings.Contains(text, "![remote]") || !strings.Contains(text, `\!\[remote\]`) {
		t.Fatalf("markdown did not escape image syntax:\n%s", text)
	}
}

func TestMarkdownDefensivelyDeduplicatesFacts(t *testing.T) {
	fact := domain.Fact{Category: "symptom", Content: "最近有心悸", SourceQuote: "最近有心悸"}
	data, err := exporter.Markdown(domain.Session{
		Status: domain.StatusCompleted, VisitGoal: "准备说明心悸", Facts: []domain.Fact{fact, fact, fact},
	})
	if err != nil {
		t.Fatalf("Markdown() error = %v", err)
	}
	if count := strings.Count(string(data), "**症状**：最近有心悸"); count != 1 {
		t.Fatalf("duplicate fact count = %d:\n%s", count, data)
	}
}

func TestMarkdownDoesNotDuplicateConversationSummaryPunctuation(t *testing.T) {
	data, err := exporter.Markdown(domain.Session{
		Status: domain.StatusCompleted,
		ConversationSummary: domain.ConversationSummary{
			Headline:  "我这次主要想说明心悸。",
			Confirmed: []string{"心悸：最近一周出现"},
		},
	})
	if err != nil {
		t.Fatalf("Markdown() error = %v", err)
	}
	text := string(data)
	if strings.Contains(text, "。。") {
		t.Fatalf("conversation summary contains duplicate punctuation:\n%s", text)
	}
	if !strings.Contains(text, "我这次主要想说明心悸。目前已明确：心悸：最近一周出现。") {
		t.Fatalf("conversation summary = %q", text)
	}
}

func TestMarkdownRendersClarificationGlobals(t *testing.T) {
	data, err := exporter.Markdown(domain.Session{
		Status:           domain.StatusCompleted,
		VisitGoal:        "整理心悸相关情况并向医生说明",
		Medications:      []string{"在用布洛芬"},
		Allergies:        []string{"青霉素"},
		DeniedConditions: []string{"否认既往疾病：没有糖尿病"},
		Measurements:     []string{"体温38度"},
	})
	if err != nil {
		t.Fatalf("Markdown() error = %v", err)
	}
	text := string(data)
	for _, want := range []string{"用药、过敏与既往情况", "在用布洛芬", "青霉素", "否认既往疾病：没有糖尿病", "体温38度"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in export:\n%s", want, text)
		}
	}
}

func TestMarkdownCollapsesEvidenceWhenContentIsTheSourceQuote(t *testing.T) {
	session := domain.Session{
		Status:    domain.StatusCompleted,
		VisitGoal: "整理心悸相关情况并向医生说明",
		Facts:     []domain.Fact{{Category: "symptom", Content: "最近有心悸", SourceQuote: "最近有心悸"}},
		Timeline:  []domain.TimelineEvent{{TimeLabel: "最近", Event: "最近有心悸", SourceQuote: "最近有心悸"}},
	}
	data, err := exporter.Markdown(session)
	if err != nil {
		t.Fatalf("Markdown() error = %v", err)
	}
	text := string(data)
	if strings.Contains(text, "原文依据：最近有心悸") {
		t.Fatalf("duplicate evidence line was not collapsed:\n%s", text)
	}
	if strings.Contains(text, "就诊时间线") && strings.Contains(text, "原文依据：最近有心悸") {
		t.Fatalf("timeline repeated identical evidence:\n%s", text)
	}
}
