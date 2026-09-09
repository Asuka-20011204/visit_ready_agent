package domain

import "time"

type Fact struct {
	Category    string `json:"category"`
	Content     string `json:"content"`
	SourceQuote string `json:"source_quote"`
	TimeLabel   string `json:"time_label,omitempty"`
}

type Priority string

const (
	PriorityUrgent Priority = "urgent"
	PriorityHigh   Priority = "high"
	PriorityNormal Priority = "normal"
)

// SymptomProfile contains only details explicitly found in the user's text.
// Empty fields mean the detail is still unknown, never that it is absent.
type SymptomProfile struct {
	Name               string   `json:"name"`
	Onset              string   `json:"onset,omitempty"`
	Duration           string   `json:"duration,omitempty"`
	Frequency          string   `json:"frequency,omitempty"`
	Severity           string   `json:"severity,omitempty"`
	Pattern            string   `json:"pattern,omitempty"`
	Trigger            string   `json:"trigger,omitempty"`
	RelievingFactors   string   `json:"relieving_factors,omitempty"`
	AssociatedSymptoms []string `json:"associated_symptoms,omitempty"`
	SourceQuote        string   `json:"source_quote"`
	EvidenceQuotes     []string `json:"evidence_quotes,omitempty"`
}

type TimelineEvent struct {
	TimeLabel   string `json:"time_label"`
	Event       string `json:"event"`
	SourceQuote string `json:"source_quote"`
}

// RiskSignal is a triage-oriented reminder, not a diagnosis or probability.
type RiskSignal struct {
	Priority    Priority `json:"priority"`
	Title       string   `json:"title"`
	Evidence    string   `json:"evidence"`
	Guidance    string   `json:"guidance"`
	SourceQuote string   `json:"source_quote"`
}

type ActionItem struct {
	Title    string   `json:"title"`
	Detail   string   `json:"detail"`
	Reason   string   `json:"reason"`
	Priority Priority `json:"priority"`
	Category string   `json:"category"`
}

// Uncertainty is an explicit information state, not a clinical conclusion.
type Uncertainty struct {
	Topic    string   `json:"topic"`
	Detail   string   `json:"detail"`
	Why      string   `json:"why"`
	Status   string   `json:"status"` // unknown, skipped, unconfirmed
	Priority Priority `json:"priority"`
}

// Contradiction keeps both user-provided claims visible until they are resolved.
type Contradiction struct {
	Topic              string   `json:"topic"`
	FirstEvidence      string   `json:"first_evidence"`
	SecondEvidence     string   `json:"second_evidence"`
	ClarifyingQuestion string   `json:"clarifying_question"`
	Priority           Priority `json:"priority"`
}

type ConversationSummary struct {
	Headline    string   `json:"headline"`
	Intent      string   `json:"intent"`
	Confirmed   []string `json:"confirmed,omitempty"`
	OpenThreads []string `json:"open_threads,omitempty"`
}

type InterviewState struct {
	TurnCount        int    `json:"turn_count"`
	MaxTurns         int    `json:"max_turns"`
	ConfirmedCount   int    `json:"confirmed_count"`
	OpenCount        int    `json:"open_count"`
	CompletionReason string `json:"completion_reason,omitempty"`
}

type Extraction struct {
	VisitGoal              string              `json:"visit_goal"`
	Facts                  []Fact              `json:"facts"`
	SymptomProfiles        []SymptomProfile    `json:"symptom_profiles,omitempty"`
	Timeline               []TimelineEvent     `json:"timeline,omitempty"`
	RiskSignals            []RiskSignal        `json:"risk_signals,omitempty"`
	MissingFields          []string            `json:"missing_fields"`
	ClarificationQuestions []string            `json:"clarification_questions"`
	ClarificationPrompts   []Question          `json:"clarification_prompts,omitempty"`
	SearchQueries          []string            `json:"search_queries"`
	Uncertainties          []Uncertainty       `json:"uncertainties,omitempty"`
	Contradictions         []Contradiction     `json:"contradictions,omitempty"`
	ConversationSummary    ConversationSummary `json:"conversation_summary,omitempty"`
	InterviewState         InterviewState      `json:"interview_state,omitempty"`
}

type Source struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Domain  string `json:"domain"`
	Snippet string `json:"snippet"`
}

type Question struct {
	Text      string   `json:"text"`
	SourceURL string   `json:"source_url,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	Priority  Priority `json:"priority,omitempty"`
	Category  string   `json:"category,omitempty"`
}

type QuestionSet struct {
	Questions   []Question   `json:"questions"`
	ActionItems []ActionItem `json:"action_items,omitempty"`
}

type QuestionInput struct {
	VisitGoal       string           `json:"visit_goal"`
	Facts           []Fact           `json:"facts"`
	SymptomProfiles []SymptomProfile `json:"symptom_profiles,omitempty"`
	Timeline        []TimelineEvent  `json:"timeline,omitempty"`
	RiskSignals     []RiskSignal     `json:"risk_signals,omitempty"`
	MissingFields   []string         `json:"missing_fields"`
	Sources         []Source         `json:"sources"`
	Uncertainties   []Uncertainty    `json:"uncertainties,omitempty"`
	Contradictions  []Contradiction  `json:"contradictions,omitempty"`
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
	StatusEmergency            AgentStatus = "emergency"
	StatusFailed               AgentStatus = "failed"
)

type AgentEvent struct {
	Step      string    `json:"step"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type AgentProgress struct {
	Node       string `json:"node"`
	Status     string `json:"status"`
	Message    string `json:"message"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

type NodeTiming struct {
	Node        string    `json:"node"`
	DurationMS  int64     `json:"duration_ms"`
	Status      string    `json:"status"`
	CompletedAt time.Time `json:"completed_at"`
}

// RunFailure exposes only recovery metadata safe for clients. Provider errors,
// prompts, and health input remain private server-side state.
type RunFailure struct {
	Code      string    `json:"code"`
	Message   string    `json:"message"`
	Retryable bool      `json:"retryable"`
	Attempts  int       `json:"attempts"`
	FailedAt  time.Time `json:"failed_at"`
}

const (
	FailureTemporary = "agent_temporarily_unavailable"
	FailurePermanent = "agent_processing_failed"
	FailureExhausted = "agent_recovery_exhausted"
)

// ClarificationTurn is private server-side context. Questions are retained so
// short answers such as "first: five minutes" remain attributable next round.
type ClarificationTurn struct {
	Questions []Question `json:"-"`
	Answer    string     `json:"-"`
}

type Session struct {
	ID                     string              `json:"id"`
	OwnerHash              string              `json:"-"`
	Revision               uint64              `json:"revision"`
	Status                 AgentStatus         `json:"status"`
	RawInput               string              `json:"-"`
	Clarification          string              `json:"-"`
	ClarificationTurns     []ClarificationTurn `json:"-"`
	AllowWebSearch         bool                `json:"allow_web_search"`
	ClarificationCount     int                 `json:"clarification_count"`
	VisitGoal              string              `json:"visit_goal"`
	Facts                  []Fact              `json:"facts"`
	SymptomProfiles        []SymptomProfile    `json:"symptom_profiles,omitempty"`
	Timeline               []TimelineEvent     `json:"timeline,omitempty"`
	RiskSignals            []RiskSignal        `json:"risk_signals,omitempty"`
	EmergencyMessage       string              `json:"emergency_message,omitempty"`
	RejectedFactCount      int                 `json:"rejected_fact_count"`
	MissingFields          []string            `json:"missing_fields"`
	ClarificationQuestions []string            `json:"clarification_questions"`
	ClarificationPrompts   []Question          `json:"clarification_prompts,omitempty"`
	Questions              []Question          `json:"questions"`
	ActionItems            []ActionItem        `json:"action_items,omitempty"`
	Sources                []Source            `json:"sources"`
	Uncertainties          []Uncertainty       `json:"uncertainties,omitempty"`
	Contradictions         []Contradiction     `json:"contradictions,omitempty"`
	ConversationSummary    ConversationSummary `json:"conversation_summary,omitempty"`
	InterviewState         InterviewState      `json:"interview_state,omitempty"`
	Failure                *RunFailure         `json:"failure,omitempty"`
	Events                 []AgentEvent        `json:"events"`
	NodeTimings            []NodeTiming        `json:"node_timings,omitempty"`
	CreatedAt              time.Time           `json:"created_at"`
	ExpiresAt              time.Time           `json:"expires_at"`
}
