package demo_test

import (
	"context"
	"testing"

	"visitready/internal/demo"
	"visitready/internal/domain"
)

func TestClientUsesExactInputQuotesAndClarifiesOnce(t *testing.T) {
	client := demo.Client{}
	first, err := client.Extract(context.Background(), "三天前开始咳嗽。晚上更明显。目前没有服药。")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if len(first.Facts) != 3 || first.Facts[0].SourceQuote != "三天前开始咳嗽" || len(first.ClarificationQuestions) != 1 {
		t.Fatalf("first extraction = %#v", first)
	}

	second, err := client.Extract(context.Background(), "三天前开始咳嗽。晚上更明显。目前没有服药。\n补充信息：体温37.2度")
	if err != nil {
		t.Fatalf("second Extract() error = %v", err)
	}
	if len(second.ClarificationQuestions) != 0 || len(second.SearchQueries) != 1 {
		t.Fatalf("second extraction = %#v", second)
	}
}

func TestClientQuestionsAreWithinMedicalBoundary(t *testing.T) {
	client := demo.Client{}
	result, err := client.GenerateQuestions(context.Background(), domain.QuestionInput{Sources: []domain.Source{{URL: "https://who.int/a"}}})
	if err != nil || len(result.Questions) != 3 {
		t.Fatalf("GenerateQuestions() = %#v, %v", result, err)
	}
	if result.Questions[1].SourceURL != "https://who.int/a" {
		t.Fatalf("source URL = %q", result.Questions[1].SourceURL)
	}
}

func TestClientCoversSupportedCategoriesAndTopics(t *testing.T) {
	client := demo.Client{}
	inputs := []string{
		"补充信息：每天服用一种药物。已知青霉素过敏。做过CT检查。以前有相关病史。",
		"补充信息：出现皮疹并且瘙痒。",
		"补充信息：饭后有一种说不清楚的不舒服。",
	}
	for _, input := range inputs {
		result, err := client.Extract(context.Background(), input)
		if err != nil || len(result.Facts) == 0 || len(result.SearchQueries) == 0 {
			t.Fatalf("Extract(%q) = %#v, %v", input, result, err)
		}
	}
}
