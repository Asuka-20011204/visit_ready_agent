package domain

import "time"

type Fact struct {
	Category    string `json:"category"`
	Content     string `json:"content"`
	SourceQuote string `json:"source_quote"`
	TimeLabel   string `json:"time_label,omitempty"`
}

type Extraction struct {
	VisitGoal              string   `json:"visit_goal"`
	Facts                  []Fact   `json:"facts"`
	MissingFields          []string `json:"missing_fields"`
	ClarificationQuestions []string `json:"clarification_questions"`
	SearchQueries          []string `json:"search_queries"`
}

type Source struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Domain  string `json:"domain"`
	Snippet string `json:"snippet"`
}

type Question struct {
	Text      string `json:"text"`
	SourceURL string `json:"source_url,omitempty"`
}

type QuestionSet struct {
	Questions []Question `json:"questions"`
}

type QuestionInput struct {
	VisitGoal     string   `json:"visit_goal"`
	Facts         []Fact   `json:"facts"`
	MissingFields []string `json:"missing_fields"`
	Sources       []Source `json:"sources"`
}

type AgentStatus string

const (
	StatusNew                  AgentStatus = "new"
	StatusExtracting           AgentStatus = "extracting"
	StatusValidating           AgentStatus = "validating"
	StatusWaitingClarification AgentStatus = "waiting_clarification"
	StatusSearching            AgentStatus = "searching"
	StatusGenerating           AgentStatus = "generating"
	StatusWaitingReview        AgentStatus = "waiting_review"
	StatusCompleted            AgentStatus = "completed"
	StatusFailed               AgentStatus = "failed"
)

type AgentEvent struct {
	Step      string    `json:"step"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type Session struct {
	ID                     string       `json:"id"`
	Status                 AgentStatus  `json:"status"`
	RawInput               string       `json:"-"`
	Clarification          string       `json:"-"`
	AllowWebSearch         bool         `json:"allow_web_search"`
	ClarificationCount     int          `json:"clarification_count"`
	VisitGoal              string       `json:"visit_goal"`
	Facts                  []Fact       `json:"facts"`
	RejectedFactCount      int          `json:"rejected_fact_count"`
	MissingFields          []string     `json:"missing_fields"`
	ClarificationQuestions []string     `json:"clarification_questions"`
	Questions              []Question   `json:"questions"`
	Sources                []Source     `json:"sources"`
	Events                 []AgentEvent `json:"events"`
	CreatedAt              time.Time    `json:"created_at"`
	ExpiresAt              time.Time    `json:"expires_at"`
}
