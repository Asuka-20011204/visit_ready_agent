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

func TestMarkdownSeparatesFactsQuestionsAndReferences(t *testing.T) {
	session := domain.Session{
		Status:    domain.StatusCompleted,
		VisitGoal: "向医生说明咳嗽变化 <script>alert(1)</script> ![remote](https://attacker.example/pixel)",
		Facts: []domain.Fact{{
			Category: "symptom", Content: "咳嗽三天", SourceQuote: "三天前开始咳嗽", TimeLabel: "三天前",
		}},
		Questions: []domain.Question{{Text: "是否需要进一步检查？", SourceURL: "https://www.who.int/example"}},
		Sources:   []domain.Source{{Title: "WHO 就诊资料", Domain: "who.int", URL: "https://www.who.int/example"}},
		RawInput:  "这段原始输入不应出现在导出文件中",
	}

	data, err := exporter.Markdown(session)
	if err != nil {
		t.Fatalf("Markdown() error = %v", err)
	}
	text := string(data)
	for _, want := range []string{"# 我的诊前沟通清单", "向医生说明咳嗽变化", "咳嗽三天", "原文依据：三天前开始咳嗽", "是否需要进一步检查？", "WHO 就诊资料", "仅用于整理就诊信息"} {
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
