package agent

import (
 "context"
 "testing"
 "time"

 "visitready/internal/domain"
)

func TestDeterministicPromptsPassSafetyContract(t *testing.T) {
 s := domain.Session{SymptomProfiles: []domain.SymptomProfile{{Name:"头痛"}}}
 for _, tc := range []struct{ field, category string }{
  {"头痛持续时间","duration"}, {"头痛频率","frequency"}, {"头痛开始时间","onset"},
  {"头痛诱因","trigger"}, {"头痛程度","severity"}, {"头痛伴随表现","associated"},
  {"用药情况","medication"}, {"过敏情况","allergy"}, {"既往疾病","history"},
 } {
  t.Run(tc.category,func(t *testing.T){
   p:=clarificationPromptForMissingField(tc.field,tc.category,s)
   if !isSafeClarificationPrompt(p) { t.Fatalf("generated prompt rejected: %+v",p) }
  })
 }
}

func TestFirstRoundMissingFieldGetsDeterministicQuestion(t *testing.T) {
 r:= &Runner{now:time.Now}
 next,err:=r.evidenceNode(context.Background(),workflowState{Session:domain.Session{
  SymptomProfiles:[]domain.SymptomProfile{{Name:"头痛"}},
  MissingFields:[]string{"头痛持续时间"},
  MissingFieldItems:[]domain.MissingField{{Field:"头痛持续时间",Category:"duration"}},
 }})
 if err!=nil { t.Fatal(err) }
 if next.Session.Status!=domain.StatusWaitingClarification || len(next.Session.ClarificationQuestions)==0 {
  t.Fatalf("unanswered first-round gap did not trigger question: %+v",next.Session)
 }
}

func TestConflictsRespectInterviewBudget(t *testing.T) {
 r:= &Runner{now:time.Now}
 next,err:=r.evidenceNode(context.Background(),workflowState{Session:domain.Session{
  RawInput:"头痛每次持续半小时，有时候每次持续一小时",
  SymptomProfiles:[]domain.SymptomProfile{{Name:"头痛"}},
  ClarificationCount:maxClarificationRounds,
 }})
 if err!=nil {t.Fatal(err)}
 if len(next.Session.Contradictions)==0 {t.Fatal("fixture did not create conflict")}
 if next.Session.Status==domain.StatusWaitingClarification {t.Fatal("conflict exceeded interview budget")}
}
