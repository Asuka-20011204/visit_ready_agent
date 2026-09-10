package agent_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"visitready/internal/agent"
	"visitready/internal/domain"
)

type fakeLLM struct {
	extractions   []domain.Extraction
	questions     domain.QuestionSet
	questionErr   error
	extractCall   int
	extractInputs []string
	questionIn    domain.QuestionInput
	questionCalls int
}

func (f *fakeLLM) Extract(_ context.Context, input string) (domain.Extraction, error) {
	if f.extractCall >= len(f.extractions) {
		return domain.Extraction{}, errors.New("unexpected extract call")
	}
	f.extractInputs = append(f.extractInputs, input)
	result := f.extractions[f.extractCall]
	f.extractCall++
	return result, nil
}

func (f *fakeLLM) GenerateQuestions(_ context.Context, input domain.QuestionInput) (domain.QuestionSet, error) {
	f.questionCalls++
	f.questionIn = input
	return f.questions, f.questionErr
}

func TestRunnerEscalatesCurrentEmergencyBeforeCallingLLMOrSearch(t *testing.T) {
	model := &fakeLLM{}
	search := &fakeSearch{}
	runner, err := agent.NewRunner(agent.Config{LLM: model, Search: search, AllowedDomains: []string{"who.int"}, SessionTTL: time.Hour, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}

	session, err := runner.Start(context.Background(), "我现在胸痛并且呼吸困难，症状正在明显加重，想知道就诊前该怎么准备。", true)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if session.Status != domain.StatusEmergency {
		t.Fatalf("status = %q, want emergency", session.Status)
	}
	if session.EmergencyMessage != agent.EmergencyEscalationMessage {
		t.Fatalf("emergency message = %q", session.EmergencyMessage)
	}
	if model.extractCall != 0 || model.questionCalls != 0 || len(search.queries) != 0 {
		t.Fatalf("downstream calls: extract=%d questions=%d search=%#v", model.extractCall, model.questionCalls, search.queries)
	}
	if len(session.RiskSignals) == 0 || session.RiskSignals[0].Priority != domain.PriorityUrgent {
		t.Fatalf("risk signals = %#v", session.RiskSignals)
	}
	if !hasEvent(session.Events, "emergency", "completed") {
		t.Fatalf("events = %#v", session.Events)
	}
	if _, err := runner.Resume(context.Background(), session, "继续补充信息"); !errors.Is(err, agent.ErrInvalidState) {
		t.Fatalf("Resume(emergency) error = %v", err)
	}
	if _, err := runner.Confirm(session, "继续准备就诊"); !errors.Is(err, agent.ErrInvalidState) {
		t.Fatalf("Confirm(emergency) error = %v", err)
	}
}

func TestRunnerDoesNotBlockShortEmergencyWithAccidentalIdentifier(t *testing.T) {
	model := &fakeLLM{}
	runner, err := agent.NewRunner(agent.Config{LLM: model, SessionTTL: time.Hour, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	session, err := runner.Start(context.Background(), "我叫张三，现在胸痛。", false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if session.Status != domain.StatusEmergency || session.RawInput != "" || len(session.RiskSignals) != 1 || session.RiskSignals[0].SourceQuote != "胸痛" {
		t.Fatalf("emergency session retained unsafe context: %#v", session)
	}
	if model.extractCall != 0 || model.questionCalls != 0 {
		t.Fatalf("LLM calls = extract %d, questions %d", model.extractCall, model.questionCalls)
	}
}

func TestRunnerEscalatesEmergencyReportedDuringClarification(t *testing.T) {
	model := &fakeLLM{extractions: []domain.Extraction{{
		VisitGoal:            "整理近期反复心悸情况",
		Facts:                []domain.Fact{{Category: "symptom", Content: "最近反复心悸", SourceQuote: "最近反复心悸"}},
		ClarificationPrompts: []domain.Question{{Text: "发作时是否伴有胸痛或呼吸困难？", Priority: domain.PriorityUrgent, Category: "safety"}},
	}}}
	search := &fakeSearch{}
	runner, err := agent.NewRunner(agent.Config{LLM: model, Search: search, AllowedDomains: []string{"who.int"}, SessionTTL: time.Hour, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	session, err := runner.Start(context.Background(), "我最近反复心悸，想在去医院前把有关情况梳理清楚。", true)
	if err != nil || session.Status != domain.StatusWaitingClarification {
		t.Fatalf("Start() = %#v, %v", session, err)
	}

	session, err = runner.Resume(context.Background(), session, "现在同时有胸痛和呼吸困难，而且比刚才更明显。")
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if session.Status != domain.StatusEmergency || session.EmergencyMessage != agent.EmergencyEscalationMessage {
		t.Fatalf("emergency session = %#v", session)
	}
	if model.extractCall != 1 || model.questionCalls != 0 || len(search.queries) != 0 {
		t.Fatalf("unexpected downstream calls: extract=%d questions=%d search=%#v", model.extractCall, model.questionCalls, search.queries)
	}
}

func TestRunnerClarificationEmergencyPreemptsPIIAndDoesNotRetainAnswer(t *testing.T) {
	model := &fakeLLM{extractions: []domain.Extraction{{
		VisitGoal:            "整理近期反复心悸情况",
		Facts:                []domain.Fact{{Category: "symptom", Content: "最近反复心悸", SourceQuote: "最近反复心悸"}},
		ClarificationPrompts: []domain.Question{{Text: "发作时是否伴有其他不适？", Priority: domain.PriorityHigh, Category: "safety"}},
	}}}
	runner := newRunner(t, model, &fakeSearch{})
	session, err := runner.Start(context.Background(), "我最近反复心悸，想在去医院前把有关情况梳理清楚。", false)
	if err != nil || session.Status != domain.StatusWaitingClarification {
		t.Fatalf("Start() = %#v, %v", session, err)
	}

	session, err = runner.Resume(context.Background(), session, "我叫张三，现在胸痛。")
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if session.Status != domain.StatusEmergency || session.RawInput != "" || session.Clarification != "" || strings.Contains(session.Clarification, "张三") || len(session.ClarificationTurns) != 0 {
		t.Fatalf("emergency clarification retained identifier: %#v", session)
	}
}

func TestRunnerConfirmationDropsPrivateConversationContext(t *testing.T) {
	runner := newRunner(t, &fakeLLM{}, &fakeSearch{})
	item := domain.Session{
		ID: "review", Status: domain.StatusWaitingReview,
		RawInput: "最近反复心悸", Clarification: "昨晚更明显",
		ClarificationTurns: []domain.ClarificationTurn{{Answer: "昨晚更明显"}},
	}
	completed, err := runner.Confirm(item, "整理心悸就诊信息")
	if err != nil {
		t.Fatal(err)
	}
	if completed.RawInput != "" || completed.Clarification != "" || len(completed.ClarificationTurns) != 0 {
		t.Fatalf("completed session retained private conversation context: %#v", completed)
	}
}

func TestRunnerUsesSafetyQuestionContextForShortEmergencyAnswer(t *testing.T) {
	model := &fakeLLM{extractions: []domain.Extraction{{
		VisitGoal:            "整理近期反复心悸情况",
		Facts:                []domain.Fact{{Category: "symptom", Content: "最近反复心悸", SourceQuote: "最近反复心悸"}},
		ClarificationPrompts: []domain.Question{{Text: "发作时是否伴有胸痛或呼吸困难？", Priority: domain.PriorityUrgent, Category: "safety"}},
	}}}
	runner := newRunner(t, model, &fakeSearch{})
	session, err := runner.Start(context.Background(), "我最近反复心悸，想在去医院前把有关情况梳理清楚。", false)
	if err != nil {
		t.Fatal(err)
	}
	session, err = runner.Resume(context.Background(), session, "有，而且越来越严重。")
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if session.Status != domain.StatusEmergency || len(session.RiskSignals) != 1 || session.RiskSignals[0].SourceQuote != "越来越严重" || len(session.ClarificationTurns) != 0 {
		t.Fatalf("contextual emergency = %#v", session)
	}
}

type fakeSearch struct {
	mu      sync.Mutex
	queries []string
	sources []domain.Source
	err     error
}

func (f *fakeSearch) Search(_ context.Context, query string, _ []string) ([]domain.Source, error) {
	f.mu.Lock()
	f.queries = append(f.queries, query)
	f.mu.Unlock()
	return append([]domain.Source(nil), f.sources...), f.err
}

type blockingSearch struct {
	started chan string
	release chan struct{}
}

func (b *blockingSearch) Search(ctx context.Context, query string, _ []string) ([]domain.Source, error) {
	select {
	case b.started <- query:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-b.release:
		return []domain.Source{{Title: query, URL: "https://who.int/" + query, Domain: "who.int"}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestRunnerSearchesIndependentQueriesConcurrently(t *testing.T) {
	model := &fakeLLM{
		extractions: []domain.Extraction{{
			VisitGoal:     "整理咳嗽和头痛情况",
			Facts:         []domain.Fact{{Category: "symptom", Content: "咳嗽和头痛持续三天", SourceQuote: "咳嗽和头痛持续三天"}},
			SearchQueries: []string{"咳嗽 就诊准备", "头痛 就诊准备"},
		}},
		questions: domain.QuestionSet{Questions: []domain.Question{{Text: "就诊时应重点说明哪些症状变化？"}}},
	}
	searcher := &blockingSearch{started: make(chan string, 2), release: make(chan struct{})}
	runner := newRunner(t, model, searcher)
	done := make(chan error, 1)
	go func() {
		_, err := runner.Start(context.Background(), "咳嗽和头痛持续三天，希望整理信息并准备就诊沟通。", true)
		done <- err
	}()

	<-searcher.started
	select {
	case <-searcher.started:
		close(searcher.release)
	case <-time.After(200 * time.Millisecond):
		close(searcher.release)
		t.Fatal("second independent search did not start concurrently")
	}
	if err := <-done; err != nil {
		t.Fatalf("Start() error = %v", err)
	}
}

func TestRunnerPausesForClarificationAndResumes(t *testing.T) {
	model := &fakeLLM{
		extractions: []domain.Extraction{
			{
				VisitGoal:              "整理咳嗽情况",
				Facts:                  []domain.Fact{{Category: "symptom", Content: "咳嗽三天", SourceQuote: "三天前开始咳嗽"}},
				MissingFields:          []string{"是否发热"},
				ClarificationQuestions: []string{"是否测量过体温？"},
			},
			{
				VisitGoal:     "整理咳嗽情况",
				Facts:         []domain.Fact{{Category: "symptom", Content: "咳嗽三天", SourceQuote: "三天前开始咳嗽"}, {Category: "temperature", Content: "体温37.2度", SourceQuote: "体温37.2度"}},
				SearchQueries: []string{"咳嗽 就诊前准备"},
			},
		},
		questions: domain.QuestionSet{Questions: []domain.Question{{Text: "我还需要做哪些检查准备？"}}},
	}
	searcher := &fakeSearch{sources: []domain.Source{{Title: "WHO", URL: "https://www.who.int/example", Domain: "who.int"}}}
	runner := newRunner(t, model, searcher)

	session, err := runner.Start(context.Background(), "三天前开始咳嗽，晚上明显，目前没有服药。", true)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if session.Status != domain.StatusWaitingClarification {
		t.Fatalf("status = %q", session.Status)
	}
	if len(searcher.queries) != 0 {
		t.Fatalf("search called before clarification: %v", searcher.queries)
	}

	session, err = runner.Resume(context.Background(), session, "测量过，体温37.2度。")
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if session.Status != domain.StatusWaitingReview {
		t.Fatalf("status after resume = %q", session.Status)
	}
	if len(session.Facts) != 2 || len(session.Sources) != 1 || len(session.Questions) != 1 {
		t.Fatalf("resumed session = %#v", session)
	}
	if model.extractCall != 2 {
		t.Fatalf("extract calls = %d", model.extractCall)
	}
}

func TestRunnerLimitsPrioritizedClarificationToTwoRounds(t *testing.T) {
	fact := domain.Fact{Category: "symptom", Content: "心悸反复出现", SourceQuote: "心悸反复出现"}
	prompts := []domain.Question{
		{Text: "症状在什么情况下更明显？", Reason: "了解诱因", Priority: domain.PriorityNormal, Category: "trigger"},
		{Text: "发作时是否出现胸痛或呼吸困难？", Reason: "确认安全优先项", Priority: domain.PriorityUrgent, Category: "safety"},
		{Text: "每次发作需要记录多长时间？", Reason: "了解持续时间", Priority: domain.PriorityHigh, Category: "duration"},
		{Text: "一天需要记录多少次发作？", Reason: "了解频率", Priority: domain.PriorityHigh, Category: "frequency"},
	}
	model := &fakeLLM{
		extractions: []domain.Extraction{
			{VisitGoal: "梳理心悸", Facts: []domain.Fact{fact}, ClarificationPrompts: prompts},
			{VisitGoal: "梳理心悸", Facts: []domain.Fact{fact}, ClarificationPrompts: prompts},
			{VisitGoal: "梳理心悸", Facts: []domain.Fact{fact}, ClarificationPrompts: prompts},
		},
		questions: domain.QuestionSet{Questions: []domain.Question{{Text: "我还需要向您说明哪些发作细节？"}}},
	}
	runner := newRunner(t, model, &fakeSearch{})

	session, err := runner.Start(context.Background(), "心悸反复出现，但我还没有记录每次持续多久以及一天发作多少次。", false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if len(session.ClarificationPrompts) != 3 || session.ClarificationPrompts[0].Priority != domain.PriorityUrgent {
		t.Fatalf("first clarification round = %#v", session.ClarificationPrompts)
	}
	session, err = runner.Resume(context.Background(), session, "第一次补充：通常在活动以后更明显。")
	if err != nil || session.Status != domain.StatusWaitingClarification || session.ClarificationCount != 1 {
		t.Fatalf("first Resume() = %#v, %v", session, err)
	}
	for _, prompt := range session.ClarificationPrompts {
		if prompt.Text == prompts[1].Text || prompt.Text == prompts[2].Text || prompt.Text == prompts[3].Text {
			t.Fatalf("second round repeated an already asked prompt: %#v", session.ClarificationPrompts)
		}
	}
	session, err = runner.Resume(context.Background(), session, "第二次补充：其他细节目前暂时不清楚。")
	if err != nil {
		t.Fatalf("second Resume() error = %v", err)
	}
	if session.Status != domain.StatusWaitingReview || session.ClarificationCount != 2 {
		t.Fatalf("second Resume() must enter review: %#v", session)
	}
	if len(model.extractInputs) != 3 || !strings.Contains(model.extractInputs[2], "发作时是否出现胸痛或呼吸困难") || !strings.Contains(model.extractInputs[2], "第一次补充") || !strings.Contains(model.extractInputs[2], "第二次补充") {
		t.Fatalf("third extraction input lost prior clarification: %#v", model.extractInputs)
	}
}

func TestRunnerKeepsPriorFactsWhenClarificationExtractionIsUngrounded(t *testing.T) {
	fact := domain.Fact{Category: "symptom", Content: "左手腕偶尔会酸胀", SourceQuote: "左手腕偶尔会酸胀"}
	model := &fakeLLM{
		extractions: []domain.Extraction{
			{VisitGoal: "整理手腕酸胀", Facts: []domain.Fact{fact}, SymptomProfiles: []domain.SymptomProfile{{Name: "左手腕酸胀", SourceQuote: fact.SourceQuote}}, ClarificationPrompts: []domain.Question{{Text: "麻木通常持续多久？", Reason: "补充病程", Priority: domain.PriorityHigh, Category: "duration"}}},
			{VisitGoal: "整理手腕酸胀", Facts: []domain.Fact{{Category: "symptom", Content: "腕管综合征", SourceQuote: "模型编造的诊断"}}},
		},
		questions: domain.QuestionSet{Questions: []domain.Question{{Text: "我还需要向医生说明哪些手腕变化？"}}},
	}
	runner := newRunner(t, model, &fakeSearch{})
	session, err := runner.Start(context.Background(), "这三周左手腕偶尔会酸胀，用鼠标超过半小时就会发酸。", false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	session, err = runner.Resume(context.Background(), session, "麻木不是每次用鼠标都会出现，大概持续 10 到 20 分钟。")
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if session.Status == domain.StatusFailed {
		t.Fatalf("ungrounded second extraction failed the session: %#v", session.Failure)
	}
	if len(session.Facts) != 1 || session.Facts[0].SourceQuote != fact.SourceQuote {
		t.Fatalf("prior grounded facts were not retained: %#v", session.Facts)
	}
	if len(session.SymptomProfiles) != 1 || session.SymptomProfiles[0].Duration != "大概持续 10 到 20 分钟" {
		t.Fatalf("duration was not recovered from the clarification answer: %#v", session.SymptomProfiles)
	}
	if session.Status != domain.StatusWaitingReview {
		t.Fatalf("ungrounded second extraction status = %q, want waiting_review", session.Status)
	}
}

func TestRunnerMergesGroundedClarificationFactsWithPriorFacts(t *testing.T) {
	fact := domain.Fact{Category: "symptom", Content: "左手腕偶尔会酸胀", SourceQuote: "左手腕偶尔会酸胀"}
	added := domain.Fact{Category: "duration", Content: "大概持续 10 到 20 分钟", SourceQuote: "大概持续 10 到 20 分钟"}
	model := &fakeLLM{
		extractions: []domain.Extraction{
			{VisitGoal: "整理手腕酸胀", Facts: []domain.Fact{fact}, ClarificationPrompts: []domain.Question{{Text: "麻木通常持续多久？", Reason: "补充病程", Priority: domain.PriorityHigh, Category: "duration"}}},
			{VisitGoal: "整理手腕酸胀", Facts: []domain.Fact{added}},
		},
		questions: domain.QuestionSet{Questions: []domain.Question{{Text: "我还需要向医生说明哪些手腕变化？"}}},
	}
	runner := newRunner(t, model, &fakeSearch{})
	session, err := runner.Start(context.Background(), "这三周左手腕偶尔会酸胀，用鼠标超过半小时就会发酸。", false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	session, err = runner.Resume(context.Background(), session, "麻木不是每次用鼠标都会出现，大概持续 10 到 20 分钟。")
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if session.Status == domain.StatusFailed {
		t.Fatalf("merged clarification extraction failed the session: %#v", session.Failure)
	}
	if len(session.Facts) != 2 {
		t.Fatalf("merged facts = %#v", session.Facts)
	}
	quotes := session.Facts[0].SourceQuote + " " + session.Facts[1].SourceQuote
	if !strings.Contains(quotes, fact.SourceQuote) || !strings.Contains(quotes, added.SourceQuote) {
		t.Fatalf("merged facts lost prior or new evidence: %#v", session.Facts)
	}
}

func TestRunnerMergesClarificationProfileFieldsWithoutDroppingPriorState(t *testing.T) {
	initial := "这三周左手腕偶尔会酸胀，用鼠标超过半小时就会发酸。"
	answer := "麻木不是每次用鼠标都会出现，大概持续 10 到 20 分钟。"
	model := &fakeLLM{
		extractions: []domain.Extraction{
			{
				VisitGoal: "整理手腕酸胀和麻木", Facts: []domain.Fact{{Category: "symptom", Content: initial, SourceQuote: initial}},
				SymptomProfiles:      []domain.SymptomProfile{{Name: "左手腕酸胀", Onset: "三周前", Trigger: "用鼠标超过半小时", SourceQuote: initial}},
				MissingFields:        []string{"麻木每次持续多久", "是否需要记录体温"},
				ClarificationPrompts: []domain.Question{{Text: "麻木每次持续多久？", Category: "duration", Priority: domain.PriorityHigh}},
			},
			{
				VisitGoal: "整理手腕酸胀和麻木", Facts: []domain.Fact{{Category: "symptom", Content: answer, SourceQuote: answer}},
				SymptomProfiles: []domain.SymptomProfile{{Name: "左手腕酸胀", Duration: "大概持续 10 到 20 分钟", SourceQuote: initial, EvidenceQuotes: []string{answer}}},
				MissingFields:   []string{"麻木每次持续多久"},
			},
		},
		questions: domain.QuestionSet{Questions: []domain.Question{
			{Text: "麻木每次持续多久？", Category: "duration"},
			{Text: "是否需要向医生说明日常活动影响？", Category: "visit"},
		}},
	}
	runner := newRunner(t, model, &fakeSearch{})
	session, err := runner.Start(context.Background(), initial, false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	session, err = runner.Resume(context.Background(), session, answer)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if len(session.SymptomProfiles) != 1 {
		t.Fatalf("profiles = %#v", session.SymptomProfiles)
	}
	profile := session.SymptomProfiles[0]
	if profile.Onset != "三周前" || profile.Trigger != "用鼠标超过半小时" || profile.Duration != "大概持续 10 到 20 分钟" {
		t.Fatalf("incremental profile merge lost fields: %#v", profile)
	}
	if len(session.Uncertainties) != 1 || session.Uncertainties[0].Topic != "是否需要记录体温" {
		t.Fatalf("answered missing field was not reconciled: %#v", session.Uncertainties)
	}
	for _, question := range session.Questions {
		if question.Category == "duration" {
			t.Fatalf("doctor questions repeated answered duration: %#v", session.Questions)
		}
	}
	for _, item := range session.Uncertainties {
		if item.Topic == "补充回答" {
			t.Fatalf("approximate answer was treated as whole-turn uncertainty: %#v", session.Uncertainties)
		}
	}
}

func TestRunnerKeepsPriorFactsWhenClarificationExtractionErrors(t *testing.T) {
	fact := domain.Fact{Category: "symptom", Content: "左手腕偶尔会酸胀", SourceQuote: "左手腕偶尔会酸胀"}
	model := &fakeLLM{
		extractions: []domain.Extraction{
			{VisitGoal: "整理手腕酸胀", Facts: []domain.Fact{fact}, ClarificationQuestions: []string{"麻木通常持续多久？"}},
		},
		questions: domain.QuestionSet{Questions: []domain.Question{{Text: "我还需要向医生说明哪些手腕变化？"}}},
	}
	runner := newRunner(t, model, &fakeSearch{})
	session, err := runner.Start(context.Background(), "这三周左手腕偶尔会酸胀，用鼠标超过半小时就会发酸。", false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	session, err = runner.Resume(context.Background(), session, "麻木不是每次用鼠标都会出现，大概持续 10 到 20 分钟。")
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if session.Status == domain.StatusFailed {
		t.Fatalf("extract error during clarification failed the session: %#v", session.Failure)
	}
	if len(session.Facts) != 1 || session.Facts[0].SourceQuote != fact.SourceQuote {
		t.Fatalf("prior grounded facts were not retained: %#v", session.Facts)
	}
	if session.Status != domain.StatusWaitingReview {
		t.Fatalf("extract error during clarification status = %q, want waiting_review", session.Status)
	}
}

func TestRunnerLetsUserSkipRemainingClarification(t *testing.T) {
	fact := domain.Fact{Category: "symptom", Content: "头痛反复出现", SourceQuote: "头痛反复出现"}
	model := &fakeLLM{
		extractions: []domain.Extraction{
			{VisitGoal: "梳理头痛", Facts: []domain.Fact{fact}, ClarificationQuestions: []string{"每次头痛需要记录多长时间？"}},
			{VisitGoal: "梳理头痛", Facts: []domain.Fact{fact, {Category: "other", Content: "不清楚，希望跳过剩余追问", SourceQuote: "不清楚，希望跳过剩余追问"}}, ClarificationQuestions: []string{"一天需要记录多少次头痛？"}},
		},
		questions: domain.QuestionSet{Questions: []domain.Question{{Text: "我还需要向您说明哪些头痛细节？"}}},
	}
	runner := newRunner(t, model, &fakeSearch{})
	session, err := runner.Start(context.Background(), "头痛反复出现，但我还没有记录持续时间和每天发作次数。", false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	session, err = runner.Resume(context.Background(), session, "不清楚，希望跳过剩余追问。")
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if session.Status != domain.StatusWaitingReview {
		t.Fatalf("skip answer did not enter review: %#v", session)
	}
	if len(session.Facts) != 1 {
		t.Fatalf("clarification control answer became a clinical fact: %#v", session.Facts)
	}
}

func TestRunnerSanitizesQueriesAndDegradesWhenSearchFails(t *testing.T) {
	model := &fakeLLM{
		extractions: []domain.Extraction{{
			VisitGoal:     "整理头痛情况",
			Facts:         []domain.Fact{{Category: "symptom", Content: "头痛两天", SourceQuote: "头痛两天"}},
			SearchQueries: []string{"我叫王小明 手机13800138000 头痛 就诊准备"},
		}},
		questions: domain.QuestionSet{Questions: []domain.Question{{Text: "需要记录哪些诱发因素？"}}},
	}
	searcher := &fakeSearch{err: errors.New("provider unavailable")}
	runner := newRunner(t, model, searcher)

	session, err := runner.Start(context.Background(), "头痛两天，休息后稍有缓解，没有服药，也没有记录其他异常。", true)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if session.Status != domain.StatusWaitingReview {
		t.Fatalf("status = %q", session.Status)
	}
	if len(searcher.queries) != 1 || strings.Contains(searcher.queries[0], "13800138000") || strings.Contains(searcher.queries[0], "王小明") {
		t.Fatalf("queries leaked PII: %v", searcher.queries)
	}
	if !hasEvent(session.Events, "search", "degraded") {
		t.Fatalf("events do not record degraded search: %#v", session.Events)
	}
}

func TestRunnerProvidesUsefulSafeFallbackWhenQuestionGenerationFails(t *testing.T) {
	model := &fakeLLM{
		extractions: []domain.Extraction{{
			VisitGoal:       "梳理心悸",
			Facts:           []domain.Fact{{Category: "symptom", Content: "最近有心悸", SourceQuote: "最近有心悸"}},
			SymptomProfiles: []domain.SymptomProfile{{Name: "心悸", SourceQuote: "最近有心悸"}},
		}},
		questionErr: errors.New("model unavailable"),
	}
	runner := newRunner(t, model, &fakeSearch{})

	session, err := runner.Start(context.Background(), "最近有心悸，目前没有记录发作时间、持续多久或者当时活动。", false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if len(session.Questions) == 0 || session.Questions[0].Reason == "" || session.Questions[0].Priority == "" {
		t.Fatalf("fallback questions lack useful metadata: %#v", session.Questions)
	}
	if len(session.ActionItems) < 2 || len(session.ActionItems) > 3 {
		t.Fatalf("fallback action items = %#v", session.ActionItems)
	}
	for _, item := range session.ActionItems {
		if item.Reason == "" || item.Priority == "" || item.Category == "" {
			t.Fatalf("fallback action item is incomplete: %#v", item)
		}
	}
}

func TestRunnerFiltersMedicalOverreachAndRequiresReviewBeforeCompletion(t *testing.T) {
	model := &fakeLLM{
		extractions: []domain.Extraction{{
			VisitGoal: "整理胃痛情况",
			Facts:     []domain.Fact{{Category: "symptom", Content: "胃痛一天", SourceQuote: "胃痛一天"}},
		}},
		questions: domain.QuestionSet{Questions: []domain.Question{
			{Text: "建议停用目前药物"},
			{Text: "就诊时需要说明哪些饮食变化？"},
		}},
	}
	runner := newRunner(t, model, &fakeSearch{})

	session, err := runner.Start(context.Background(), "胃痛一天，饭后更明显，正在服用医生开的药。", false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if len(session.Questions) != 1 || strings.Contains(session.Questions[0].Text, "停用") {
		t.Fatalf("questions = %#v", session.Questions)
	}
	if _, err := runner.Confirm(session, "联系 13800138000 说明胃痛变化"); !errors.Is(err, agent.ErrInvalidInput) {
		t.Fatalf("PII visit goal error = %v", err)
	}
	completed, err := runner.Confirm(session, "向医生说明胃痛变化")
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if completed.Status != domain.StatusCompleted || completed.VisitGoal != "向医生说明胃痛变化" {
		t.Fatalf("completed = %#v", completed)
	}
}

func TestRunnerGroundsFactContentAndFiltersUnsafeClarification(t *testing.T) {
	model := &fakeLLM{
		extractions: []domain.Extraction{{
			VisitGoal: "诊断为肺炎",
			Facts: []domain.Fact{{
				Category: "symptom", Content: "诊断为肺炎", SourceQuote: "咳嗽三天",
			}},
			ClarificationQuestions: []string{"建议服用抗生素", "是否测量过体温？"},
			ClarificationPrompts: []domain.Question{
				{Text: "是否可以提供银行卡密码？", Priority: domain.PriorityUrgent, Category: "safety"},
				{Text: "是否测量过体温？", Reason: "补充客观测量", Priority: domain.PriorityHigh, Category: "measurement"},
			},
		}},
	}
	runner := newRunner(t, model, &fakeSearch{})

	session, err := runner.Start(context.Background(), "咳嗽三天，晚上加重，目前还没有测量体温。", false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if session.Status != domain.StatusWaitingClarification {
		t.Fatalf("status = %q", session.Status)
	}
	if len(session.Facts) != 1 || session.Facts[0].Content != "咳嗽三天" {
		t.Fatalf("facts = %#v", session.Facts)
	}
	if session.VisitGoal == "诊断为肺炎" {
		t.Fatalf("unsafe visit goal was retained: %q", session.VisitGoal)
	}
	if len(session.ClarificationQuestions) != 1 || strings.Contains(session.ClarificationQuestions[0], "服用") {
		t.Fatalf("clarification questions = %#v", session.ClarificationQuestions)
	}
	for _, answer := range []string{"123456", "1234567890123456789"} {
		if _, err := runner.Resume(context.Background(), session, answer); !errors.Is(err, agent.ErrInvalidInput) {
			t.Fatalf("numeric credential answer %q error = %v", answer, err)
		}
	}
}

func TestRunnerCarriesStructuredIntelligenceIntoReviewAndQuestions(t *testing.T) {
	model := &fakeLLM{
		extractions: []domain.Extraction{{
			VisitGoal: "梳理心悸和夜间发热情况",
			Facts: []domain.Fact{{
				Category: "symptom", Content: "最近有心悸", SourceQuote: "最近有心悸",
			}, {
				Category: "other", Content: "最近有心悸", SourceQuote: "最近 有心悸。",
			}},
			SymptomProfiles: []domain.SymptomProfile{
				{Name: "心悸", Severity: "严重", Pattern: "夜间伴发热", AssociatedSymptoms: []string{"手脚发汗", "恶心"}, SourceQuote: "心悸，手脚发汗，晚上睡觉发热"},
				{Name: "恶性肿瘤", Severity: "严重", SourceQuote: "心悸，手脚发汗，晚上睡觉发热"},
			},
			Timeline: []domain.TimelineEvent{{
				TimeLabel: "昨天", Event: "出现心悸", SourceQuote: "最近有心悸",
			}},
			RiskSignals:   []domain.RiskSignal{{Priority: domain.PriorityUrgent, Title: "无原文", Evidence: "呼吸困难", Guidance: "不应展示", SourceQuote: "呼吸困难"}},
		}},
		questions: domain.QuestionSet{
			Questions: []domain.Question{
				{Text: "心悸每次持续多久，是否影响日常活动？", Reason: "持续时间和影响程度有助于医生判断就诊优先级", Priority: domain.PriorityHigh, Category: "symptom_detail"},
				{Text: "是否可以提供银行卡密码？", Reason: "不可信请求", Priority: domain.PriorityUrgent, Category: "safety"},
			},
			ActionItems: []domain.ActionItem{{
				Title: "记录发作", Detail: "记录发作时间、持续时长和当时活动", Reason: "帮助医生了解发作模式", Priority: domain.PriorityHigh, Category: "tracking",
			}, {
				Title: "立即服药", Detail: "口服两片阿司匹林", Reason: "防止加重", Priority: domain.PriorityUrgent, Category: "safety",
			}},
		},
	}
	runner := newRunner(t, model, &fakeSearch{})

	session, err := runner.Start(context.Background(), "我最近有心悸，手脚发汗，晚上睡觉发热的问题，目前没有记录具体体温，也没有其他明显不适。", false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if len(session.Facts) != 1 || len(session.SymptomProfiles) != 1 || len(session.Timeline) != 1 || len(session.RiskSignals) != 0 {
		t.Fatalf("structured review data was lost: %#v", session)
	}
	if session.SymptomProfiles[0].Severity != "" || session.SymptomProfiles[0].Pattern != "" || len(session.SymptomProfiles[0].AssociatedSymptoms) != 1 || session.Timeline[0].TimeLabel != "" {
		t.Fatalf("ungrounded structured fields survived: profile=%#v timeline=%#v", session.SymptomProfiles[0], session.Timeline[0])
	}
	if len(model.questionIn.SymptomProfiles) != 1 || len(model.questionIn.Timeline) != 1 || len(model.questionIn.RiskSignals) != 0 {
		t.Fatalf("question input lacks structured context: %#v", model.questionIn)
	}
	if len(session.Questions) != 1 {
		t.Fatalf("sensitive questions survived: %#v", session.Questions)
	}
	if got := session.Questions[0]; got.Reason == "" || got.Priority != domain.PriorityHigh || got.Category != "symptom_detail" {
		t.Fatalf("question metadata was lost: %#v", got)
	}
	if len(session.ActionItems) < 3 || session.ActionItems[0].Category == "safety" || strings.Contains(session.ActionItems[0].Title+session.ActionItems[0].Detail, "服药") {
		t.Fatalf("action items were lost: %#v", session.ActionItems)
	}
	if !strings.Contains(session.ActionItems[0].Title, "心悸") {
		t.Fatalf("tracking action is not personalized to the symptom: %#v", session.ActionItems)
	}
	if !strings.Contains(session.ActionItems[0].Detail, "持续时长") || !strings.Contains(session.ActionItems[0].Detail, "频率") {
		t.Fatalf("tracking action does not cover symptom timing and frequency: %#v", session.ActionItems[0])
	}

	session.SymptomProfiles[0].AssociatedSymptoms[0] = "被修改"
	if model.questionIn.SymptomProfiles[0].AssociatedSymptoms[0] == "被修改" {
		t.Fatal("structured context shares nested symptom slices")
	}
}

func TestRunnerKeepsPositiveRedFlagBesideNegatedRedFlag(t *testing.T) {
	input := "我有胸痛没有呼吸困难，症状现在还在持续，需要尽快整理这次就诊信息。"
	model := &fakeLLM{
		extractions: []domain.Extraction{{
			VisitGoal: "整理就诊信息",
			Facts:     []domain.Fact{{Category: "symptom", Content: input, SourceQuote: input}},
		}},
		questions: domain.QuestionSet{Questions: []domain.Question{{Text: "胸痛从什么时候开始，是否仍在持续？"}}},
	}
	session, err := newRunner(t, model, &fakeSearch{}).Start(context.Background(), input, false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if len(session.RiskSignals) != 1 || !strings.Contains(session.RiskSignals[0].Evidence, "胸痛") || strings.Contains(session.RiskSignals[0].Evidence, "呼吸困难") {
		t.Fatalf("mixed positive and negated risks = %#v", session.RiskSignals)
	}
}

func TestRunnerGroundsAssociatedSymptomFromClarificationEvidence(t *testing.T) {
	initial := "最近反复出现心悸，希望整理后向医生说明。"
	answer := "发作时还伴有头晕。"
	model := &fakeLLM{
		extractions: []domain.Extraction{
			{
				VisitGoal:              "整理心悸",
				Facts:                  []domain.Fact{{Category: "symptom", Content: "心悸", SourceQuote: "心悸"}},
				SymptomProfiles:        []domain.SymptomProfile{{Name: "心悸", SourceQuote: "心悸"}},
				ClarificationQuestions: []string{"心悸发作时还伴有哪些不适？"},
				ClarificationPrompts:   []domain.Question{{Text: "心悸发作时还伴有哪些不适？", Reason: "补全伴随表现", Priority: domain.PriorityHigh, Category: "associated_symptom"}},
			},
			{
				VisitGoal:       "整理心悸",
				Facts:           []domain.Fact{{Category: "symptom", Content: "心悸", SourceQuote: "心悸"}},
				SymptomProfiles: []domain.SymptomProfile{{Name: "心悸", AssociatedSymptoms: []string{"头晕"}, SourceQuote: "心悸", EvidenceQuotes: []string{answer}}},
			},
		},
		questions: domain.QuestionSet{Questions: []domain.Question{{Text: "心悸伴头晕时，我需要重点说明哪些变化？"}}},
	}
	runner := newRunner(t, model, &fakeSearch{})
	session, err := runner.Start(context.Background(), initial, false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	session, err = runner.Resume(context.Background(), session, answer)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if got := session.SymptomProfiles[0].AssociatedSymptoms; len(got) != 1 || got[0] != "头晕" {
		t.Fatalf("associated symptoms = %#v", got)
	}
}

func TestRunnerGroundsProactiveAndLateClarificationDetails(t *testing.T) {
	initial := "最近反复出现心悸，希望整理后向医生说明。"
	frequencyAnswer := "心悸每次约五分钟，一天两三次。"
	timingAnswer := "这些情况近一周开始，心悸多在安静时出现，深呼吸后会缓解。"
	baseFact := domain.Fact{Category: "symptom", Content: "心悸", SourceQuote: "心悸"}
	model := &fakeLLM{
		extractions: []domain.Extraction{
			{
				VisitGoal: "整理心悸", Facts: []domain.Fact{baseFact},
				SymptomProfiles:        []domain.SymptomProfile{{Name: "心悸", SourceQuote: "心悸"}},
				ClarificationQuestions: []string{"心悸的开始时间是什么时候？"},
				ClarificationPrompts:   []domain.Question{{Text: "心悸的开始时间是什么时候？", Reason: "补全时间线", Priority: domain.PriorityHigh, Category: "timeline"}},
			},
			{
				VisitGoal: "整理心悸", Facts: []domain.Fact{baseFact},
				SymptomProfiles:        []domain.SymptomProfile{{Name: "心悸", Frequency: "一天两三次", SourceQuote: "心悸", EvidenceQuotes: []string{frequencyAnswer}}},
				ClarificationQuestions: []string{"心悸一天或一周大约发作几次？", "心悸通常在什么情况下出现，怎样做会缓解？"},
				ClarificationPrompts: []domain.Question{
					{Text: "心悸一天或一周大约发作几次？", Reason: "补全频率", Priority: domain.PriorityHigh, Category: "frequency"},
					{Text: "心悸通常在什么情况下出现，怎样做会缓解？", Reason: "补全诱因和缓解因素", Priority: domain.PriorityNormal, Category: "trigger"},
				},
			},
			{
				VisitGoal: "整理心悸", Facts: []domain.Fact{baseFact},
				SymptomProfiles: []domain.SymptomProfile{{
					Name: "心悸", Onset: "近一周", Duration: "每次约五分钟", Frequency: "一天两三次", Trigger: "安静时", RelievingFactors: "深呼吸后会缓解",
					SourceQuote: "心悸", EvidenceQuotes: []string{frequencyAnswer, timingAnswer},
				}},
			},
		},
		questions: domain.QuestionSet{Questions: []domain.Question{{Text: "心悸这些变化需要如何向医生说明？"}}},
	}
	runner := newRunner(t, model, &fakeSearch{})
	session, err := runner.Start(context.Background(), initial, false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	session, err = runner.Resume(context.Background(), session, frequencyAnswer)
	if err != nil {
		t.Fatalf("first Resume() error = %v", err)
	}
	session, err = runner.Resume(context.Background(), session, timingAnswer)
	if err != nil {
		t.Fatalf("second Resume() error = %v", err)
	}
	profile := session.SymptomProfiles[0]
	if profile.Onset != "近一周" || profile.Duration != "每次约五分钟" || profile.Frequency != "一天两三次" || profile.Trigger != "安静时" || profile.RelievingFactors != "深呼吸后会缓解" {
		t.Fatalf("profile lost proactive or late clarification details: profile=%#v turns=%#v", profile, session.ClarificationTurns)
	}
}

func TestRunnerDerivesOnlyExplicitCurrentRedFlags(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantRisk bool
	}{
		{name: "model omission", input: "现在胸痛且呼吸困难，症状仍在持续，需要整理后向医生说明。", wantRisk: true},
		{name: "expanded red flag", input: "现在突然出现单侧无力和说话不清，症状仍在持续，需要整理。", wantRisk: true},
		{name: "coordinated negation", input: "我否认胸痛和呼吸困难，目前只是心悸，需要整理就诊信息。", wantRisk: false},
		{name: "hypothetical", input: "如果以后出现胸痛或呼吸困难，我想知道就诊时应如何说明。", wantRisk: false},
		{name: "historical", input: "过去曾经胸痛，目前没有这些表现，只想整理既往情况。", wantRisk: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := &fakeLLM{
				extractions: []domain.Extraction{{
					VisitGoal:   "整理就诊信息",
					Facts:       []domain.Fact{{Category: "symptom", Content: test.input, SourceQuote: test.input}},
					RiskSignals: []domain.RiskSignal{{Priority: domain.PriorityNormal, Title: "模型信号", SourceQuote: test.input}},
				}},
				questions: domain.QuestionSet{Questions: []domain.Question{{Text: "我还需要向您说明哪些相关信息？"}}},
			}
			session, err := newRunner(t, model, &fakeSearch{}).Start(context.Background(), test.input, false)
			if err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			if got := len(session.RiskSignals) > 0; got != test.wantRisk {
				t.Fatalf("risk signals = %#v, wantRisk = %t", session.RiskSignals, test.wantRisk)
			}
			if test.wantRisk && session.RiskSignals[0].Priority != domain.PriorityUrgent {
				t.Fatalf("risk priority = %q", session.RiskSignals[0].Priority)
			}
		})
	}
}

func TestRunnerClassifiesInputAndUpstreamErrors(t *testing.T) {
	runner := newRunner(t, &fakeLLM{}, &fakeSearch{})
	if _, err := runner.Start(context.Background(), "太短", false); !errors.Is(err, agent.ErrInvalidInput) {
		t.Fatalf("short input error = %v", err)
	}
	if _, err := runner.Start(context.Background(), "这是一段足够长但模型会失败的脱敏健康情况描述。", false); !errors.Is(err, agent.ErrUpstream) {
		t.Fatalf("upstream error = %v", err)
	}
}

func newRunner(t *testing.T, model *fakeLLM, searcher agent.SearchClient) *agent.Runner {
	t.Helper()
	fixedNow := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	runner, err := agent.NewRunner(agent.Config{
		LLM:            model,
		Search:         searcher,
		AllowedDomains: []string{"who.int"},
		SessionTTL:     30 * time.Minute,
		Now:            func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	return runner
}

func hasEvent(events []domain.AgentEvent, step, status string) bool {
	for _, event := range events {
		if event.Step == step && event.Status == status {
			return true
		}
	}
	return false
}

func TestRunnerPreservesContradictoryDetailsAndAsksBeforeReview(t *testing.T) {
	initial := "心悸从昨天开始，最近反复出现，希望整理后向医生说明。"
	answer := "我不确定：有时记得是两周前开始，有时又记得是昨天开始。"
	model := &fakeLLM{
		extractions: []domain.Extraction{
			{
				VisitGoal:            "整理心悸",
				Facts:                []domain.Fact{{Category: "symptom", Content: "心悸从昨天开始", SourceQuote: initial}},
				SymptomProfiles:      []domain.SymptomProfile{{Name: "心悸", Onset: "昨天", SourceQuote: initial}},
				ClarificationPrompts: []domain.Question{{Text: "心悸每次持续多久？", Reason: "补全病程", Priority: domain.PriorityHigh, Category: "duration"}},
			},
			{
				VisitGoal: "整理心悸",
				Facts: []domain.Fact{
					{Category: "symptom", Content: "心悸从昨天开始", SourceQuote: initial},
					{Category: "symptom", Content: "心悸可能从两周前开始", SourceQuote: answer},
				},
				SymptomProfiles: []domain.SymptomProfile{{Name: "心悸", Onset: "两周前", SourceQuote: initial, EvidenceQuotes: []string{answer}}},
			},
		},
	}
	runner := newRunner(t, model, &fakeSearch{})
	session, err := runner.Start(context.Background(), initial, false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	session, err = runner.Resume(context.Background(), session, answer)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if len(session.Contradictions) == 0 {
		t.Fatalf("contradictory onset was silently accepted: %#v", session)
	}
	if session.Status != domain.StatusWaitingClarification {
		t.Fatalf("contradiction must require clarification before review, got %q", session.Status)
	}
	if len(session.ClarificationPrompts) == 0 || !strings.Contains(session.ClarificationPrompts[0].Text, "时间") && !strings.Contains(session.ClarificationPrompts[0].Text, "开始") {
		t.Fatalf("clarification did not target conflicting onset: %#v", session.ClarificationPrompts)
	}
}

func TestRunnerKeepsUncertainAnswerOutOfDefinitiveProfile(t *testing.T) {
	initial := "最近反复心悸，但每次持续多久我不确定，希望准备就诊沟通。"
	answer := "不确定，可能五分钟，也可能更久，我记不清。"
	model := &fakeLLM{
		extractions: []domain.Extraction{
			{
				VisitGoal:            "整理心悸",
				Facts:                []domain.Fact{{Category: "symptom", Content: "反复心悸", SourceQuote: initial}},
				SymptomProfiles:      []domain.SymptomProfile{{Name: "心悸", SourceQuote: initial}},
				ClarificationPrompts: []domain.Question{{Text: "心悸每次持续多久？", Reason: "补全持续时间", Priority: domain.PriorityHigh, Category: "duration"}},
			},
			{
				VisitGoal:       "整理心悸",
				Facts:           []domain.Fact{{Category: "symptom", Content: "反复心悸", SourceQuote: initial}},
				SymptomProfiles: []domain.SymptomProfile{{Name: "心悸", Duration: "五分钟", SourceQuote: initial, EvidenceQuotes: []string{answer}}},
			},
		},
	}
	runner := newRunner(t, model, &fakeSearch{})
	session, err := runner.Start(context.Background(), initial, false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	session, err = runner.Resume(context.Background(), session, answer)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if len(session.Uncertainties) == 0 {
		t.Fatalf("uncertain answer was not retained as uncertainty: %#v", session)
	}
	if len(session.SymptomProfiles) == 0 || session.SymptomProfiles[0].Duration == "五分钟" {
		t.Fatalf("uncertain duration became definitive: %#v", session.SymptomProfiles)
	}
}

func TestRunnerLabelsSkippedClarificationAsUncertain(t *testing.T) {
	initial := "最近反复头痛，但每次持续多久我说不清，希望准备就诊沟通。"
	answer := "这个问题我记不清，先跳过，之后就诊时再问医生。"
	model := &fakeLLM{
		extractions: []domain.Extraction{
			{
				VisitGoal:            "整理头痛",
				Facts:                []domain.Fact{{Category: "symptom", Content: "反复头痛", SourceQuote: initial}},
				SymptomProfiles:      []domain.SymptomProfile{{Name: "头痛", SourceQuote: initial}},
				ClarificationPrompts: []domain.Question{{Text: "头痛每次持续多久？", Reason: "补全持续时间", Priority: domain.PriorityHigh, Category: "duration"}},
			},
			{
				VisitGoal:       "整理头痛",
				Facts:           []domain.Fact{{Category: "symptom", Content: "反复头痛", SourceQuote: initial}},
				SymptomProfiles: []domain.SymptomProfile{{Name: "头痛", SourceQuote: initial}},
			},
		},
	}
	runner := newRunner(t, model, &fakeSearch{})
	session, err := runner.Start(context.Background(), initial, false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	session, err = runner.Resume(context.Background(), session, answer)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if session.Status != domain.StatusWaitingReview {
		t.Fatalf("skip should finish clarification without fabricating data: %q", session.Status)
	}
	if len(session.Uncertainties) == 0 {
		t.Fatalf("skipped clarification was not represented as uncertainty: %#v", session)
	}
	foundSkipped := false
	for _, item := range session.Uncertainties {
		if item.Status == "skipped" && strings.Contains(item.Topic, "持续多久") {
			foundSkipped = true
		}
	}
	if !foundSkipped {
		t.Fatalf("uncertainty did not retain skipped question topic: %#v", session.Uncertainties)
	}
	if len(session.Facts) != 1 {
		t.Fatalf("skip answer became a clinical fact: %#v", session.Facts)
	}
}

func TestRunnerDoesNotConfuseDifferentSymptomsAsContradictory(t *testing.T) {
	input := "心悸每天两次，头痛每周一次，两个情况都已持续一段时间，希望分别整理后就诊。"
	model := &fakeLLM{
		extractions: []domain.Extraction{{
			VisitGoal: "分别整理心悸和头痛",
			Facts:     []domain.Fact{{Category: "symptom", Content: "心悸每天两次", SourceQuote: input}},
			SymptomProfiles: []domain.SymptomProfile{
				{Name: "心悸", Frequency: "每天两次", SourceQuote: input},
				{Name: "头痛", Frequency: "每周一次", SourceQuote: input},
			},
		}},
	}
	session, err := newRunner(t, model, &fakeSearch{}).Start(context.Background(), input, false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if len(session.Contradictions) != 0 {
		t.Fatalf("different symptom frequencies were conflated: %#v", session.Contradictions)
	}
}

func TestRunnerStopsUnresolvedConflictAtInterviewBudget(t *testing.T) {
	initial := "心悸从昨天开始，最近反复出现，希望整理后向医生说明。"
	firstAnswer := "我又记得可能是两周前开始，但不能确定。"
	secondAnswer := "还是记不清哪一个时间更准确，请保留两种说法。"
	baseFact := domain.Fact{Category: "symptom", Content: "心悸从昨天开始", SourceQuote: initial}
	model := &fakeLLM{
		extractions: []domain.Extraction{
			{
				VisitGoal: "整理心悸", Facts: []domain.Fact{baseFact},
				SymptomProfiles:      []domain.SymptomProfile{{Name: "心悸", Onset: "昨天", SourceQuote: initial}},
				ClarificationPrompts: []domain.Question{{Text: "心悸发作的开始时间是什么时候？", Reason: "补全时间线", Priority: domain.PriorityHigh, Category: "timeline"}},
			},
			{
				VisitGoal: "整理心悸", Facts: []domain.Fact{baseFact},
				SymptomProfiles: []domain.SymptomProfile{{Name: "心悸", SourceQuote: initial, EvidenceQuotes: []string{firstAnswer}}},
			},
			{
				VisitGoal: "整理心悸", Facts: []domain.Fact{baseFact},
				SymptomProfiles: []domain.SymptomProfile{{Name: "心悸", SourceQuote: initial, EvidenceQuotes: []string{firstAnswer, secondAnswer}}},
			},
		},
	}
	runner := newRunner(t, model, &fakeSearch{})
	session, err := runner.Start(context.Background(), initial, false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	session, err = runner.Resume(context.Background(), session, firstAnswer)
	if err != nil {
		t.Fatalf("first Resume() error = %v", err)
	}
	if session.Status != domain.StatusWaitingClarification {
		t.Fatalf("first conflict should request resolution: %#v", session)
	}
	session, err = runner.Resume(context.Background(), session, secondAnswer)
	if err != nil {
		t.Fatalf("second Resume() error = %v", err)
	}
	if session.Status != domain.StatusWaitingReview || session.ClarificationCount != 2 {
		t.Fatalf("unresolved conflict exceeded interview budget: %#v", session)
	}
	if len(session.Contradictions) == 0 {
		t.Fatalf("unresolved conflict must remain visible for review: %#v", session)
	}
}

func TestRunnerBuildsConversationSummaryWithOpenThreads(t *testing.T) {
	input := "最近反复心悸三天，尚未记录每次持续多久，希望准备就诊沟通。"
	model := &fakeLLM{
		extractions: []domain.Extraction{{
			VisitGoal:            "整理心悸",
			Facts:                []domain.Fact{{Category: "symptom", Content: "反复心悸三天", SourceQuote: input}},
			SymptomProfiles:      []domain.SymptomProfile{{Name: "心悸", Onset: "三天", SourceQuote: input}},
			MissingFields:        []string{"心悸每次持续时间"},
			ClarificationPrompts: []domain.Question{{Text: "心悸每次持续多久？", Reason: "补全持续时间", Priority: domain.PriorityHigh, Category: "duration"}},
		}},
	}
	session, err := newRunner(t, model, &fakeSearch{}).Start(context.Background(), input, false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if strings.TrimSpace(session.ConversationSummary.Headline) == "" {
		t.Fatalf("conversation summary headline is empty: %#v", session.ConversationSummary)
	}
	if len(session.ConversationSummary.OpenThreads) == 0 {
		t.Fatalf("conversation summary omitted open information gaps: %#v", session.ConversationSummary)
	}
}

func TestRunnerDetectsCurrentRiskAfterHistoricalNegationInSameSentence(t *testing.T) {
	input := "我以前没有胸痛现在突然胸痛很明显，希望整理后尽快与医生沟通。"
	model := &fakeLLM{extractions: []domain.Extraction{{
		VisitGoal: "整理胸痛情况",
		Facts:     []domain.Fact{{Category: "symptom", Content: input, SourceQuote: input}},
	}}}

	session, err := newRunner(t, model, &fakeSearch{}).Start(context.Background(), input, false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if len(session.RiskSignals) == 0 || !strings.Contains(session.RiskSignals[0].Evidence, "胸痛") {
		t.Fatalf("current chest pain was missed after historical negation: %#v", session.RiskSignals)
	}
}

func TestRunnerAcceptsSchemaApprovedSymptomClarificationCategory(t *testing.T) {
	input := "最近反复头痛，具体影响还没有说明，希望准备就诊沟通。"
	model := &fakeLLM{extractions: []domain.Extraction{{
		VisitGoal:            "整理头痛",
		Facts:                []domain.Fact{{Category: "symptom", Content: input, SourceQuote: input}},
		ClarificationPrompts: []domain.Question{{Text: "头痛对睡眠和日常活动有什么影响？", Reason: "了解症状影响", Priority: domain.PriorityHigh, Category: "symptom"}},
	}}}
	session, err := newRunner(t, model, &fakeSearch{}).Start(context.Background(), input, false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if session.Status != domain.StatusWaitingClarification || len(session.ClarificationPrompts) != 1 {
		t.Fatalf("approved category was removed: %#v", session.ClarificationPrompts)
	}
}
