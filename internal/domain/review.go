package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// FactReviewDigest identifies the exact ordered fact set shown for review.
func FactReviewDigest(facts []Fact) string {
	var canonical strings.Builder
	for _, fact := range facts {
		canonical.WriteString(fact.Category)
		canonical.WriteByte(0x1f)
		canonical.WriteString(fact.Content)
		canonical.WriteByte(0x1f)
		canonical.WriteString(fact.SourceQuote)
		canonical.WriteByte(0x1f)
		canonical.WriteString(fact.TimeLabel)
		canonical.WriteByte(0x1e)
	}
	digest := sha256.Sum256([]byte(canonical.String()))
	return hex.EncodeToString(digest[:])
}

// ReviewDigest identifies the exact structured analysis shown beside the facts.
// Facts and the editable visit goal have separate confirmation semantics.
func ReviewDigest(session Session) string {
	var canonical strings.Builder
	writeReviewValue(&canonical, "visitready-review-v1")

	writeReviewValue(&canonical, "symptom_profiles")
	writeReviewValue(&canonical, strconv.Itoa(len(session.SymptomProfiles)))
	for _, profile := range session.SymptomProfiles {
		for _, value := range []string{
			profile.Name, profile.Onset, profile.Duration, profile.Frequency,
			profile.Severity, profile.Pattern, profile.Trigger,
			profile.RelievingFactors, profile.SourceQuote,
		} {
			writeReviewValue(&canonical, value)
		}
		writeReviewValue(&canonical, strconv.Itoa(len(profile.AssociatedSymptoms)))
		for _, symptom := range profile.AssociatedSymptoms {
			writeReviewValue(&canonical, symptom)
		}
		writeReviewValue(&canonical, strconv.Itoa(len(profile.EvidenceQuotes)))
		for _, quote := range profile.EvidenceQuotes {
			writeReviewValue(&canonical, quote)
		}
	}

	writeReviewValue(&canonical, "timeline")
	writeReviewValue(&canonical, strconv.Itoa(len(session.Timeline)))
	for _, event := range session.Timeline {
		writeReviewValue(&canonical, event.TimeLabel)
		writeReviewValue(&canonical, event.Event)
		writeReviewValue(&canonical, event.SourceQuote)
	}

	writeReviewValue(&canonical, "risk_signals")
	writeReviewValue(&canonical, strconv.Itoa(len(session.RiskSignals)))
	for _, signal := range session.RiskSignals {
		writeReviewValue(&canonical, string(signal.Priority))
		writeReviewValue(&canonical, signal.Title)
		writeReviewValue(&canonical, signal.Evidence)
		writeReviewValue(&canonical, signal.Guidance)
		writeReviewValue(&canonical, signal.SourceQuote)
	}

	writeReviewStrings(&canonical, "missing_fields", session.MissingFields)
	writeReviewValue(&canonical, "uncertainties")
	writeReviewValue(&canonical, strconv.Itoa(len(session.Uncertainties)))
	for _, item := range session.Uncertainties {
		for _, value := range []string{item.Topic, item.Detail, item.Why, item.Status, string(item.Priority)} {
			writeReviewValue(&canonical, value)
		}
	}
	writeReviewValue(&canonical, "contradictions")
	writeReviewValue(&canonical, strconv.Itoa(len(session.Contradictions)))
	for _, item := range session.Contradictions {
		for _, value := range []string{item.Topic, item.FirstEvidence, item.SecondEvidence, item.ClarifyingQuestion, item.Resolution, string(item.Priority)} {
			writeReviewValue(&canonical, value)
		}
	}
	writeReviewStrings(&canonical, "medications", session.Medications)
	writeReviewStrings(&canonical, "allergies", session.Allergies)
	writeReviewStrings(&canonical, "chronic_conditions", session.ChronicConditions)
	writeReviewStrings(&canonical, "trauma_history", session.TraumaHistory)
	writeReviewStrings(&canonical, "safety_notes", session.SafetyNotes)
	writeReviewStrings(&canonical, "denied_conditions", session.DeniedConditions)
	writeReviewStrings(&canonical, "measurements", session.Measurements)
	writeReviewStrings(&canonical, "tests", session.Tests)
	for _, value := range []string{session.ConversationSummary.Headline, session.ConversationSummary.Intent} {
		writeReviewValue(&canonical, value)
	}
	writeReviewStrings(&canonical, "summary_confirmed", session.ConversationSummary.Confirmed)
	writeReviewStrings(&canonical, "summary_open_threads", session.ConversationSummary.OpenThreads)
	writeReviewValue(&canonical, "interview_state")
	writeReviewValue(&canonical, strconv.Itoa(session.InterviewState.TurnCount))
	writeReviewValue(&canonical, strconv.Itoa(session.InterviewState.MaxTurns))
	writeReviewValue(&canonical, strconv.Itoa(session.InterviewState.ConfirmedCount))
	writeReviewValue(&canonical, strconv.Itoa(session.InterviewState.OpenCount))
	writeReviewValue(&canonical, session.InterviewState.CompletionReason)

	writeReviewValue(&canonical, "questions")
	writeReviewValue(&canonical, strconv.Itoa(len(session.Questions)))
	for _, question := range session.Questions {
		writeReviewValue(&canonical, question.Text)
		writeReviewValue(&canonical, question.SourceURL)
		writeReviewValue(&canonical, question.Reason)
		writeReviewValue(&canonical, string(question.Priority))
		writeReviewValue(&canonical, question.Category)
	}

	writeReviewValue(&canonical, "action_items")
	writeReviewValue(&canonical, strconv.Itoa(len(session.ActionItems)))
	for _, item := range session.ActionItems {
		writeReviewValue(&canonical, item.Title)
		writeReviewValue(&canonical, item.Detail)
		writeReviewValue(&canonical, item.Reason)
		writeReviewValue(&canonical, string(item.Priority))
		writeReviewValue(&canonical, item.Category)
	}

	writeReviewValue(&canonical, "sources")
	writeReviewValue(&canonical, strconv.Itoa(len(session.Sources)))
	for _, source := range session.Sources {
		writeReviewValue(&canonical, source.Title)
		writeReviewValue(&canonical, source.URL)
		writeReviewValue(&canonical, source.Domain)
		writeReviewValue(&canonical, source.Snippet)
	}

	digest := sha256.Sum256([]byte(canonical.String()))
	return hex.EncodeToString(digest[:])
}

func writeReviewStrings(canonical *strings.Builder, name string, values []string) {
	writeReviewValue(canonical, name)
	writeReviewValue(canonical, strconv.Itoa(len(values)))
	for _, value := range values {
		writeReviewValue(canonical, value)
	}
}

func writeReviewValue(canonical *strings.Builder, value string) {
	canonical.WriteString(strconv.Itoa(len([]byte(value))))
	canonical.WriteByte(':')
	canonical.WriteString(value)
}
