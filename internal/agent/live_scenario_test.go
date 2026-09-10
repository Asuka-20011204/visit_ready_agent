//go:build live

package agent_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"visitready/internal/agent"
	"visitready/internal/domain"
	"visitready/internal/llm"
)

// startWithRetry mirrors the real user flow: a retryable upstream failure is
// retried a couple of times before giving up.
func startWithRetry(t *testing.T, runner *agent.Runner, input string) domain.Session {
	t.Helper()
	session, err := runner.Start(context.Background(), input, false)
	for attempt := 0; attempt < 2 && err != nil && errors.Is(err, agent.ErrUpstream); attempt++ {
		session, err = runner.Retry(context.Background(), session)
	}
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return session
}

// TestLiveStomachPainScenario drives the real LLM through the exact
// two-round stomach-pain conversation that failed in production, so the fix can
// be verified before release. Run with: go test -tags live -run TestLiveStomachPainScenario -v
func TestLiveStomachPainScenario(t *testing.T) {
	endpoint := os.Getenv("LLM_ENDPOINT")
	key := os.Getenv("LLM_API_KEY")
	model := os.Getenv("LLM_MODEL")
	if endpoint == "" || key == "" || model == "" {
		t.Skip("LLM_ENDPOINT/LLM_API_KEY/LLM_MODEL not set")
	}
	client, err := llm.NewOpenAICompatibleClient(endpoint, key, model, &http.Client{Timeout: 60 * time.Second}, nil)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	runner, err := agent.NewRunner(agent.Config{LLM: client, SessionTTL: time.Hour, Now: time.Now})
	if err != nil {
		t.Fatalf("runner: %v", err)
	}

	initial := "这一个多月，吃完饭以后胃这里会隐隐地疼，饭后半小时左右开始，一般要疼一两个小时才过去。最近两周更频繁了，一周四五次。有时候还会反酸、烧心，嘴里发苦。没吃过什么药，也没去检查过。以前没有胃病。"
	answer := "最疼的时候是胀痛，能忍，不用吃止痛药。会有点不想吃饭，吃个七八分饱就不太想继续了。睡觉不受影响，夜里不会疼醒。上班能坚持，就是饭后那一两个小时不太想动，得坐直缓一缓。没有呕血，也没有黑便，大便颜色正常。最近体重没掉，称过，跟以前差不多。吞咽没问题，吃东西不噎。没有持续剧烈腹痛，疼都是一阵一阵的，能过去。以前没有得过需要长期治疗的病，也没做过手术，没受过外伤。就是前几年体检查出过幽门螺杆菌，当时吃过一阵药，没再复查。"

	ctx := context.Background()
	first := startWithRetry(t, runner, initial)
	t.Logf("round1: status=%s facts=%d profiles=%d missing=%d questions=%d",
		first.Status, len(first.Facts), len(first.SymptomProfiles), len(first.MissingFields), len(first.ClarificationQuestions))

	second, err := runner.Resume(ctx, first, answer)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	t.Logf("round2: status=%s facts=%d profiles=%d medications=%d history=%d denied=%d",
		second.Status, len(second.Facts), len(second.SymptomProfiles), len(second.Medications), len(second.ChronicConditions), len(second.DeniedConditions))
	for _, fact := range second.Facts {
		t.Logf("  fact[%s] content=%q quote=%q", fact.Category, fact.Content, fact.SourceQuote)
	}
	if second.Failure != nil {
		t.Fatalf("round2 failure: code=%s message=%q", second.Failure.Code, second.Failure.Message)
	}
	if second.Status == domain.StatusFailed {
		t.Fatalf("round2 failed: %#v", second)
	}
}

// TestLiveProfileExtractionAcrossCases checks whether symptom profiles are
// extracted for inputs that DO contain a compact, verbatim symptom word, to
// tell a specific phrasing gap apart from a broader profile-extraction failure.
func TestLiveProfileExtractionAcrossCases(t *testing.T) {
	endpoint := os.Getenv("LLM_ENDPOINT")
	key := os.Getenv("LLM_API_KEY")
	model := os.Getenv("LLM_MODEL")
	if endpoint == "" || key == "" || model == "" {
		t.Skip("LLM_ENDPOINT/LLM_API_KEY/LLM_MODEL not set")
	}
	client, err := llm.NewOpenAICompatibleClient(endpoint, key, model, &http.Client{Timeout: 60 * time.Second}, nil)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	runner, err := agent.NewRunner(agent.Config{LLM: client, SessionTTL: time.Hour, Now: time.Now})
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	cases := []string{
		"我最近一个星期经常头痛，每次疼起来就只想躺着休息一下。",
		"咳嗽已经有三天了，这两天还开始有点发烧，想整理一下再去医院。",
		"最近总是反复心悸，每天大概发作好几次，想整理后就诊时向医生说明。",
	}
	for _, input := range cases {
		session, err := runner.Start(context.Background(), input, false)
		if err != nil {
			t.Fatalf("Start(%q): %v", input, err)
		}
		names := make([]string, 0, len(session.SymptomProfiles))
		for _, p := range session.SymptomProfiles {
			names = append(names, p.Name)
		}
		t.Logf("input=%q -> status=%s profiles=%v facts=%d", input, session.Status, names, len(session.Facts))
	}
}
