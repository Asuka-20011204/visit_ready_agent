package domain_test

import (
	"testing"

	"visitready/internal/domain"
)

func TestReviewDigestChangesWithDisplayedIntelligence(t *testing.T) {
	session := domain.Session{
		SymptomProfiles: []domain.SymptomProfile{{Name: "心悸", Duration: "五分钟", SourceQuote: "心悸持续五分钟"}},
		Timeline:        []domain.TimelineEvent{{TimeLabel: "昨晚", Event: "出现心悸", SourceQuote: "昨晚出现心悸"}},
		MissingFields:   []string{"发作频率"},
		Questions:       []domain.Question{{Text: "我需要说明哪些诱因？", Reason: "补充触发条件", Priority: domain.PriorityHigh, Category: "trigger"}},
		ActionItems:     []domain.ActionItem{{Title: "记录发作", Detail: "记录出现时间", Reason: "帮助沟通", Priority: domain.PriorityHigh, Category: "tracking"}},
	}
	first := domain.ReviewDigest(session)
	if first == "" || first != domain.ReviewDigest(session) {
		t.Fatal("review digest must be stable and non-empty")
	}

	changed := session
	changed.SymptomProfiles = append([]domain.SymptomProfile(nil), session.SymptomProfiles...)
	changed.SymptomProfiles[0].Duration = "十分钟"
	if first == domain.ReviewDigest(changed) {
		t.Fatal("review digest did not change with displayed intelligence")
	}
}

func TestFactReviewDigestIsStableAndTracksFactChanges(t *testing.T) {
	facts := []domain.Fact{{Category: "symptom", Content: "心悸持续五分钟", SourceQuote: "心悸持续五分钟", TimeLabel: "昨晚"}}
	first := domain.FactReviewDigest(facts)
	if first == "" || first != domain.FactReviewDigest(facts) {
		t.Fatal("fact digest must be stable and non-empty")
	}

	changed := append([]domain.Fact(nil), facts...)
	changed[0].TimeLabel = "今早"
	if first == domain.FactReviewDigest(changed) {
		t.Fatal("fact digest did not change with reviewed fact")
	}
}

func TestReviewDigestTracksConversationAndInterviewState(t *testing.T) {
	session := domain.Session{
		ConversationSummary: domain.ConversationSummary{Headline: "我想说明心悸", Confirmed: []string{"持续五分钟"}, OpenThreads: []string{"开始时间"}},
		InterviewState:      domain.InterviewState{TurnCount: 1, MaxTurns: 4, ConfirmedCount: 1, OpenCount: 1},
	}
	first := domain.ReviewDigest(session)

	changedSummary := session
	changedSummary.ConversationSummary.Confirmed = []string{"持续十分钟"}
	if first == domain.ReviewDigest(changedSummary) {
		t.Fatal("review digest ignored displayed conversation summary")
	}

	changedInterview := session
	changedInterview.InterviewState.OpenCount = 2
	if first == domain.ReviewDigest(changedInterview) {
		t.Fatal("review digest ignored displayed interview state")
	}
}
