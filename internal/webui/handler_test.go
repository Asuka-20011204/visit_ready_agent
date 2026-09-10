package webui_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"visitready/internal/webui"
)

func TestHandlerRendersConfiguredSessionRetention(t *testing.T) {
	api := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	handler, err := webui.New(api, "live", 90*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	page := servedBody(t, handler, "/")
	if strings.Count(page, "1 小时 30 分钟") != 3 || strings.Contains(page, "24 小时") {
		t.Fatal("page does not reflect configured retention")
	}
}

func TestHandlerRendersDemoDisclosureAndSecurityHeaders(t *testing.T) {
	handler := newHandler(t, "demo")
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "离线演示 · 未调用 AI") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if csp := response.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "object-src 'none'") {
		t.Fatalf("CSP = %q", csp)
	}
}

func TestLivePageExposesAccountEntryWithoutDemoDeviceRecovery(t *testing.T) {
	page := servedBody(t, newHandler(t, "live"), "/")
	for _, marker := range []string{`id="account-dialog"`, `id="account-form"`, `id="account-button"`} {
		if !strings.Contains(page, marker) {
			t.Fatalf("live page missing %s", marker)
		}
	}
	if strings.Contains(page, `id="device-access-button"`) {
		t.Fatal("live page must not offer demo device-key recovery")
	}
}

func TestHandlerServesEmbeddedAssetsAndDelegatesAPI(t *testing.T) {
	handler := newHandler(t, "live")

	assetRequest := httptest.NewRequest(http.MethodGet, "/assets/styles.css", nil)
	assetResponse := httptest.NewRecorder()
	handler.ServeHTTP(assetResponse, assetRequest)
	if assetResponse.Code != http.StatusOK || !strings.Contains(assetResponse.Body.String(), "--green") {
		t.Fatalf("asset status = %d", assetResponse.Code)
	}

	for _, path := range []string{"/livez", "/readyz", "/healthz"} {
		apiRequest := httptest.NewRequest(http.MethodGet, path, nil)
		apiResponse := httptest.NewRecorder()
		handler.ServeHTTP(apiResponse, apiRequest)
		if apiResponse.Body.String() != "api" {
			t.Fatalf("%s API response = %q", path, apiResponse.Body.String())
		}
	}
}

func TestAppIgnoresResponsesFromBeforeWorkspaceReset(t *testing.T) {
	handler := newHandler(t, "live")
	request := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	script := response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("asset status = %d", response.Code)
	}
	if !strings.Contains(script, "let workspaceGeneration = 0;") ||
		!strings.Contains(script, "workspaceGeneration += 1;") {
		t.Fatal("app.js does not invalidate pre-reset requests with a workspace generation")
	}
	if count := strings.Count(script, "generation !== workspaceGeneration"); count < 3 {
		t.Fatalf("app.js has %d stale-response guards, want at least 3", count)
	}
}

func TestApprovedUIExposesSemanticStableHooks(t *testing.T) {
	handler := newHandler(t, "live")
	page := servedBody(t, handler, "/")

	for _, hook := range []string{
		"<main",
		`data-testid="visit-ready-workspace"`,
		`data-testid="input-panel"`,
		`data-testid="session-panel"`,
		`data-testid="event-timeline"`,
		`data-testid="fact-review"`,
		`aria-live="polite"`,
	} {
		if !strings.Contains(page, hook) {
			t.Errorf("page does not expose required semantic hook %q", hook)
		}
	}
}

func TestConversationUIExposesThreadHistoryAndComposer(t *testing.T) {
	page := servedBody(t, newHandler(t, "live"), "/")

	for _, contract := range []string{
		`aria-label="往期会话"`,
		`aria-label="新建诊前准备"`,
		`data-testid="session-history"`,
		`data-testid="conversation-thread"`,
		`role="log"`,
		`data-testid="composer"`,
		`id="new-conversation-button"`,
		`data-testid="run-record"`,
	} {
		if !strings.Contains(page, contract) {
			t.Errorf("conversation UI is missing %q", contract)
		}
	}
}

func TestAgentUIUsesAiryThreadCompositionTokens(t *testing.T) {
	page := servedBody(t, newHandler(t, "live"), "/")
	styles := servedBody(t, newHandler(t, "live"), "/assets/styles.css")

	for _, contract := range []string{
		`name="theme-color" content="#f3f8f5"`,
		`class="welcome-intro"`,
		`class="prompt-paths"`,
		`class="context-strip"`,
		`class="composer-state"`,
	} {
		if !strings.Contains(page, contract) {
			t.Errorf("agent UI is missing %q", contract)
		}
	}
	for _, token := range []string{
		"--canvas: #f3f8f5",
		"--canvas-warm:",
		"--surface-raised:",
		"--accent-warm:",
		"--shadow-float:",
		"--text-caption: 15px",
		"--text-body: 17px",
	} {
		if !strings.Contains(styles, token) {
			t.Errorf("design system is missing %q", token)
		}
	}
	if strings.Contains(styles, "--canvas: #d9e1dc") {
		t.Fatal("design system still uses the rejected dark gray-green canvas")
	}
	if strings.Contains(styles, `grid-template-areas: "intro paths"`) {
		t.Fatal("welcome empty state still uses a two-column intro/paths grid")
	}
}

func TestConversationHistoryPersistsOnlyBoundedSessionIDs(t *testing.T) {
	page := servedBody(t, newHandler(t, "live"), "/")
	script := servedAppScripts(t, newHandler(t, "live"))

	for _, contract := range []string{
		`"visitready.session_ids"`,
		"MAX_HISTORY_ITEMS",
		`localStorage.getItem(HISTORY_STORAGE_KEY)`,
		`localStorage.setItem(HISTORY_STORAGE_KEY, JSON.stringify(historyIDs))`,
		`window.addEventListener("storage", syncHistoryFromStorage)`,
		"renderHistory",
		"loadHistoricalSession",
		"undoHistoryRemoval",
		"history-remove-button",
		"aria-current",
	} {
		if !strings.Contains(script, contract) {
			t.Errorf("conversation history is missing %q", contract)
		}
	}
	if !strings.Contains(page, `id="history-toast"`) || !strings.Contains(page, `id="undo-history-button"`) {
		t.Fatal("conversation history does not expose an undo notice")
	}
	if strings.Contains(script, `localStorage.setItem(HISTORY_STORAGE_KEY, JSON.stringify(session))`) {
		t.Fatal("conversation history stores a full health session")
	}
}

func TestUISeparatesHidingHistoryFromPermanentDeletion(t *testing.T) {
	handler := newHandler(t, "live")
	page := servedBody(t, handler, "/")
	script := servedAppScripts(t, handler)
	for _, contract := range []string{
		`id="delete-session-button"`,
		`id="delete-session-dialog"`,
		`id="confirm-delete-session-button"`,
	} {
		if !strings.Contains(page, contract) {
			t.Errorf("document is missing deletion contract %q", contract)
		}
	}
	for _, contract := range []string{
		`method: "DELETE"`,
		`永久删除`,
		`仅从本浏览器隐藏`,
		`hydrateHistory`,
	} {
		if !strings.Contains(script, contract) {
			t.Errorf("script is missing history/deletion behavior %q", contract)
		}
	}
}

func TestUIUsesReadableMinimumTextScale(t *testing.T) {
	styles := servedBody(t, newHandler(t, "live"), "/assets/styles.css")
	for _, token := range []string{
		`--text-micro: 14px`,
		`--text-caption: 15px`,
		`--text-ui: 16px`,
		`--text-body: 17px`,
	} {
		if !strings.Contains(styles, token) {
			t.Errorf("styles are missing readable type token %q", token)
		}
	}
}

func TestComposerExplainsAutomaticPrivacyGuardWithoutBlockingSend(t *testing.T) {
	handler := newHandler(t, "live")
	page := servedBody(t, handler, "/")
	script := servedAppScripts(t, handler)

	formStart := strings.Index(page, `<form class="composer" id="input-form"`)
	if formStart < 0 {
		t.Fatal("input composer not found")
	}
	formEndOffset := strings.Index(page[formStart:], "</form>")
	if formEndOffset < 0 {
		t.Fatal("input composer closing tag not found")
	}
	form := page[formStart : formStart+formEndOffset]
	for _, contract := range []string{`id="input-error"`, `class="composer-inline-error"`, `class="privacy-notice"`} {
		if !strings.Contains(form, contract) {
			t.Errorf("composer privacy validation is missing %q", contract)
		}
	}
	if strings.Contains(form, `id="privacy-check"`) {
		t.Error("composer still requires a per-message privacy checkbox")
	}
	if strings.Contains(script, "privacy_confirmed") || strings.Contains(script, "ui.privacy") {
		t.Error("browser still implements a repeated privacy-confirmation gate")
	}
	if !strings.Contains(form, "身份信息会在发送前自动拦截") {
		t.Error("composer does not explain the automatic identifier guard")
	}
}

func TestWorkingStateShowsElapsedTimeHeadersAndCancellation(t *testing.T) {
	handler := newHandler(t, "live")
	page := servedBody(t, handler, "/")
	script := servedAppScripts(t, handler)

	for _, id := range []string{"working-status", "working-elapsed", "cancel-run-button"} {
		if !strings.Contains(page, `id="`+id+`"`) {
			t.Errorf("working state is missing %q", id)
		}
	}
	for _, contract := range []string{
		"startWorkingTimer",
		"stopWorkingTimer",
		"function clearActiveWork({ abort = false } = {})",
		"function cancelActiveRequest()",
		"activeWorkingButton",
		"canceledButton === ui.clarify",
		"ui.clarificationInput.focus()",
		"ui.confirm.focus()",
		"ui.working.hidden = !active",
		"options.onHeaders",
		"已等待",
	} {
		if !strings.Contains(script, contract) {
			t.Errorf("working state feedback is missing %q", contract)
		}
	}
}

func TestSessionExpiryUsesHumanReadableLongDuration(t *testing.T) {
	script := servedAppScripts(t, newHandler(t, "live"))
	for _, contract := range []string{"function formatRemainingTime", "小时后清除", "天后清除"} {
		if !strings.Contains(script, contract) {
			t.Errorf("long session expiry is not human readable: missing %q", contract)
		}
	}
}

func TestConversationOwnsScrollingSoComposerCannotCoverMessages(t *testing.T) {
	styles := servedBody(t, newHandler(t, "live"), "/assets/styles.css")
	if !strings.Contains(styles, "min-height: 100dvh; height: 100dvh; overflow: hidden;") {
		t.Fatal("desktop conversation shell can grow behind the sticky composer")
	}
}

func TestSuccessfulNewTurnClearsStaleRecoveryAndRevealsLatestQuestions(t *testing.T) {
	script := servedBody(t, newHandler(t, "live"), "/assets/app.js")
	start := strings.Index(script, "async function startSession()")
	end := strings.Index(script[start:], "async function submitClarification()")
	if start < 0 || end < 0 {
		t.Fatal("startSession function not found")
	}
	startBody := script[start : start+end]
	for _, contract := range []string{`clearSectionError("recovery")`, "ui.recoveryNotice.hidden = true"} {
		if !strings.Contains(startBody, contract) {
			t.Errorf("new session keeps stale recovery state: missing %q", contract)
		}
	}
	for _, contract := range []string{"function revealLatestConversation", "ui.threadViewport.scrollHeight", "preventScroll: true"} {
		if !strings.Contains(script, contract) {
			t.Errorf("latest clarification is not revealed: missing %q", contract)
		}
	}
}

func TestShortEmergencyTextIsNotBlockedByClientSideLengthValidation(t *testing.T) {
	script := servedBody(t, newHandler(t, "live"), "/assets/app.js")
	start := strings.Index(script, "async function startSession()")
	end := strings.Index(script[start:], "async function submitClarification()")
	if start < 0 || end < 0 {
		t.Fatal("startSession function not found")
	}
	startBody := script[start : start+end]
	if strings.Contains(startBody, "length < 20") || strings.Contains(startBody, "至少填写 20") {
		t.Fatal("client-side minimum length can block short emergency descriptions")
	}
	if !strings.Contains(startBody, "if (!input)") {
		t.Fatal("empty input validation is missing")
	}
}

func TestDocumentUsesCurrentVersionedSafetyAssets(t *testing.T) {
	document := servedBody(t, newHandler(t, "live"), "/")
	for _, asset := range []string{"/assets/styles.css?v=20260910ex", "/assets/session-state.js?v=20260910o", "/assets/network.js?v=20260908g", "/assets/app.js?v=20260910bd"} {
		if !strings.Contains(document, asset) {
			t.Fatalf("document does not reference current safety asset %q", asset)
		}
	}
}

func TestBrowserConsumesRealSSEProgress(t *testing.T) {
	script := servedAppScripts(t, newHandler(t, "live"))
	for _, marker := range []string{`headers.Accept = "text/event-stream"`, "readProgressStream", "parseSSEBlock", "updateStreamProgress", "duration_ms", "streamProgress: true"} {
		if !strings.Contains(script, marker) {
			t.Errorf("browser SSE contract missing %q", marker)
		}
	}
}

func TestDeviceBoundHistoryAndCrossDeviceRecoveryContract(t *testing.T) {
	handler := newHandler(t, "live")
	page := servedBody(t, handler, "/")
	script := servedAppScripts(t, handler)
	for _, marker := range []string{`id="device-access-dialog"`, `id="copy-device-key-button"`, `id="import-device-key-button"`} {
		if !strings.Contains(page, marker) {
			t.Errorf("page is missing device recovery control %q", marker)
		}
	}
	for _, marker := range []string{"crypto.getRandomValues", `"X-VisitReady-Device-Key"`, "hydrateServerHistory", "copyDeviceKey", "importDeviceKey"} {
		if !strings.Contains(script, marker) {
			t.Errorf("device history contract is missing %q", marker)
		}
	}
	if strings.Contains(script, "?device_key=") {
		t.Fatal("device key must not be placed in URLs")
	}
}

func TestWorkspaceTransitionsCleanPendingTimersAndHistoryRemovalMovesFocus(t *testing.T) {
	script := servedAppScripts(t, newHandler(t, "live"))

	for _, functionName := range []string{"loadHistoricalSession", "renderExpiredState", "resetWorkspace"} {
		start := strings.Index(script, "function "+functionName)
		if start < 0 {
			start = strings.Index(script, "async function "+functionName)
		}
		if start < 0 {
			t.Errorf("function %s not found", functionName)
			continue
		}
		end := strings.Index(script[start:], "\n}")
		if end < 0 || !strings.Contains(script[start:start+end], "clearActiveWork") {
			t.Errorf("%s does not clean pending work", functionName)
		}
	}
	if !strings.Contains(script, "ui.undoHistory.focus()") {
		t.Fatal("history removal does not move focus to its undo action")
	}
	if !strings.Contains(script, `if (document.activeElement === ui.undoHistory)`) {
		t.Fatal("expired history undo does not restore keyboard focus")
	}
}

func TestHistorySwitchResetsPendingActionState(t *testing.T) {
	script := servedAppScripts(t, newHandler(t, "live"))
	start := strings.Index(script, "async function loadHistoricalSession(sessionID)")
	end := strings.Index(script[start:], "async function restoreSession()")
	if start < 0 || end < 0 {
		t.Fatal("loadHistoricalSession function not found")
	}
	functionBody := script[start : start+end]
	for _, contract := range []string{
		"clearActiveWork({ abort: true })",
		"clearThreadState()",
		`renderComposerMode("hidden")`,
	} {
		if !strings.Contains(functionBody, contract) {
			t.Errorf("history switch does not reset pending state: missing %q", contract)
		}
	}
}

func TestApprovedUIProvidesIndependentErrorRegions(t *testing.T) {
	handler := newHandler(t, "live")
	page := servedBody(t, handler, "/")
	script := servedBody(t, handler, "/assets/app.js")

	for _, errorRegion := range []string{"input-error", "session-error", "recovery-error"} {
		if !strings.Contains(page, `id="`+errorRegion+`"`) || !strings.Contains(page, `role="alert"`) {
			t.Errorf("page does not expose %q as an alert region", errorRegion)
		}
	}
	if !strings.Contains(script, "showSectionError") || !strings.Contains(script, "clearSectionError") {
		t.Fatal("app.js does not render and clear errors by UI section")
	}
}

func TestApprovedUIRequiresIndividualFactVerification(t *testing.T) {
	script := servedBody(t, newHandler(t, "live"), "/assets/app.js")

	for _, contract := range []string{
		"function renderFacts(facts)",
		`verification.type = "checkbox"`,
		`verification.setAttribute("aria-label", `,
		"fact-verification",
		"verifiedFactIDs",
		"reviewFacts.every",
		"查看原文依据",
		"fact-quote-toggle",
	} {
		if !strings.Contains(script, contract) {
			t.Errorf("fact review does not enforce individual verification contract %q", contract)
		}
	}
}

func TestApprovedUIRequiresStructuredInsightVerification(t *testing.T) {
	handler := newHandler(t, "live")
	page := servedBody(t, handler, "/")
	script := servedBody(t, handler, "/assets/app.js")
	reviewScript := servedBody(t, handler, "/assets/review.js")

	for _, contract := range []string{
		`id="insights-check"`,
		`data-testid="insight-review"`,
		"画像与准备建议已核对",
	} {
		if !strings.Contains(page, contract) {
			t.Errorf("structured insight review UI is missing %q", contract)
		}
	}
	for _, contract := range []string{
		"insights_acknowledged",
		"review_digest",
		"computeReviewDigest",
	} {
		if !strings.Contains(script+reviewScript, contract) {
			t.Errorf("structured insight confirmation is missing %q", contract)
		}
	}
}

func TestAgentUIExposesStructuredClinicalIntelligence(t *testing.T) {
	handler := newHandler(t, "live")
	page := servedBody(t, handler, "/")
	script := servedBody(t, handler, "/assets/app.js")

	for _, contract := range []string{
		`data-testid="risk-signals"`,
		`data-testid="symptom-profiles"`,
		`data-testid="clinical-timeline"`,
		`data-testid="next-actions"`,
		`id="missing-context"`,
	} {
		if !strings.Contains(page, contract) {
			t.Errorf("structured intelligence UI is missing %q", contract)
		}
	}
	for _, contract := range []string{
		"function renderRiskSignals(riskSignals)",
		"function renderSymptomProfiles(profiles)",
		"function renderClinicalTimeline(timeline)",
		"function renderQuestions(questions)",
		"function renderActionItems(actionItems, questions)",
		"question.reason",
		"question.priority",
		`question.category === "visit_preparation"`,
		"session.action_items",
		"textContent",
	} {
		if !strings.Contains(script, contract) {
			t.Errorf("structured intelligence renderer is missing %q", contract)
		}
	}
}

func TestAgentUIHasDedicatedEmergencyTerminalState(t *testing.T) {
	handler := newHandler(t, "live")
	page := servedBody(t, handler, "/")
	script := servedBody(t, handler, "/assets/app.js")
	styles := servedBody(t, handler, "/assets/styles.css")

	for _, contract := range []string{`id="emergency-panel"`, `id="emergency-message"`, `id="emergency-export-button"`, `id="emergency-call-button"`, `id="emergency-evidence"`, "立即拨打当地急救 / 120"} {
		if !strings.Contains(page, contract) {
			t.Errorf("emergency UI is missing %q", contract)
		}
	}
	for _, contract := range []string{`session.status === "emergency"`, "session.emergency_message", "紧急安全处理"} {
		if !strings.Contains(script, contract) {
			t.Errorf("emergency renderer is missing %q", contract)
		}
	}
	if !strings.Contains(styles, ".emergency-terminal") {
		t.Fatal("emergency terminal styles are missing")
	}
}

func TestClarificationUIShowsAdaptiveInterviewContract(t *testing.T) {
	handler := newHandler(t, "live")
	page := servedBody(t, handler, "/")
	script := servedBody(t, handler, "/assets/app.js")
	for _, contract := range []string{"只追问当前最有价值的信息", "每轮只问 1–3 个重点", "补充其他信息"} {
		if !strings.Contains(page, contract) {
			t.Errorf("clarification UI is missing %q", contract)
		}
	}
	for _, contract := range []string{"questions.slice(0, 3)", "session.clarification_count", "回答完整时会提前结束", "renderContradictions", "renderUncertainties"} {
		if !strings.Contains(script, contract) {
			t.Errorf("clarification renderer is missing %q", contract)
		}
	}
}

func TestClarificationUIPreservesSuccessfulRoundsAndClearsComposer(t *testing.T) {
	handler := newHandler(t, "live")
	script := servedAppScripts(t, handler)
	reviewScript := servedBody(t, handler, "/assets/review.js")
	for _, contract := range []string{"archiveClarificationExchange", `ui.clarificationInput.value = ""`, ".archived-clarification"} {
		if !strings.Contains(script+reviewScript, contract) {
			t.Errorf("multi-round clarification UI is missing %q", contract)
		}
	}
}

func TestClarificationComposerStacksControlsOnNarrowScreens(t *testing.T) {
	handler := newHandler(t, "live")
	styles := servedBody(t, handler, "/assets/styles.css")
	for _, contract := range []string{
		`.clarification-composer .composer-tools { flex-direction: column; align-items: stretch; gap: 4px; }`,
		`.clarification-composer .composer-submit { width: 100%; justify-content: flex-end; }`,
		`.clarification-composer .add-context-button { margin-right: auto; }`,
	} {
		if !strings.Contains(styles, contract) {
			t.Errorf("narrow clarification composer is missing %q", contract)
		}
	}
}

func TestApprovedUISessionStoragePersistsOnlySessionIDAndRestoresIt(t *testing.T) {
	script := servedAppScripts(t, newHandler(t, "live"))

	if !strings.Contains(script, `sessionStorage.setItem("visitready.session_id", session.id)`) {
		t.Fatal("app.js does not persist the session ID")
	}
	if strings.Contains(script, "sessionStorage.setItem(") && strings.Contains(script, "JSON.stringify(session)") {
		t.Fatal("app.js persists the full session instead of its ID only")
	}
	for _, contract := range []string{
		`sessionStorage.getItem("visitready.session_id")`,
		"restoreSession",
		"request(`/api/v1/sessions/${sessionID}`",
		`method: "GET"`,
	} {
		if !strings.Contains(script, contract) {
			t.Errorf("app.js does not restore persisted session via GET: missing %q", contract)
		}
	}
}

func TestApprovedUIUsesServerExpiryAndClearsExpiredSession(t *testing.T) {
	script := servedAppScripts(t, newHandler(t, "live"))

	for _, contract := range []string{
		"session.expires_at",
		"scheduleSessionExpiry",
		"Date.parse(session.expires_at)",
		`sessionStorage.removeItem("visitready.session_id")`,
	} {
		if !strings.Contains(script, contract) {
			t.Errorf("app.js does not manage server-provided expiry: missing %q", contract)
		}
	}
}

func TestApprovedUIMapsEveryEventStatusToAnAccessibleState(t *testing.T) {
	script := servedBody(t, newHandler(t, "live"), "/assets/app.js")

	for status, label := range map[string]string{
		"running":   "进行中",
		"completed": "已完成",
		"waiting":   "等待输入",
		"degraded":  "已降级",
		"skipped":   "已跳过",
		"failed":    "失败",
	} {
		if !regexp.MustCompile(`(?s)["']` + status + `["'].*?["']` + label + `["']`).MatchString(script) {
			t.Errorf("event status %q does not have accessible label %q", status, label)
		}
	}
	if !strings.Contains(script, "item.dataset.status = event.status") {
		t.Fatal("event status is not exposed as a DOM state hook")
	}
}

func TestApprovedUIOffersInlineRecoveryForFailedAgentRuns(t *testing.T) {
	handler := newHandler(t, "live")
	page := servedBody(t, handler, "/")
	scripts := servedBody(t, handler, "/assets/session-state.js") + "\n" + servedBody(t, handler, "/assets/app.js")

	for _, marker := range []string{`id="failed-panel"`, `id="retry-run-button"`, `class="failed-head"`, "本次内容已保留"} {
		if !strings.Contains(page, marker) {
			t.Fatalf("page missing recovery marker %q", marker)
		}
	}
	for _, marker := range []string{"/retry`,", `session.status === "failed"`, "retryable", "agent_processing_failed", "未通过完整安全校验"} {
		if !strings.Contains(scripts, marker) {
			t.Fatalf("scripts missing recovery behavior %q", marker)
		}
	}
}

func TestFailedAndWorkingStatesKeepAUsableComposer(t *testing.T) {
	script := servedBody(t, newHandler(t, "live"), "/assets/app.js")
	for _, contract := range []string{
		"function composerModeForSession(session)",
		`case "failed":`,
		`return "hidden"`,
		`ui.inputForm.hidden = mode !== "initial"`,
		"renderComposerMode(composerModeForSession(currentSession))",
		"renderComposerMode(composerModeForSession(session))",
	} {
		if !strings.Contains(script, contract) {
			t.Fatalf("composer recovery is missing %q", contract)
		}
	}
}

func TestHandlerRejectsInvalidConstructionAndRoutes(t *testing.T) {
	if _, err := webui.New(nil, "demo"); err == nil {
		t.Fatal("New(nil) error = nil")
	}
	handler := newHandler(t, "live")
	for _, test := range []struct {
		method string
		path   string
		want   int
	}{
		{method: http.MethodPost, path: "/", want: http.StatusMethodNotAllowed},
		{method: http.MethodGet, path: "/missing", want: http.StatusNotFound},
		{method: http.MethodHead, path: "/", want: http.StatusOK},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != test.want {
			t.Fatalf("%s %s status = %d", test.method, test.path, response.Code)
		}
	}
}

func servedBody(t *testing.T, handler http.Handler, path string) string {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, body = %s", path, response.Code, response.Body.String())
	}
	return response.Body.String()
}

func servedAppScripts(t *testing.T, handler http.Handler) string {
	t.Helper()
	return servedBody(t, handler, "/assets/session-state.js") + "\n" + servedBody(t, handler, "/assets/network.js") + "\n" + servedBody(t, handler, "/assets/app.js")
}

func newHandler(t *testing.T, mode string) http.Handler {
	t.Helper()
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("api"))
	})
	handler, err := webui.New(api, mode)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return handler
}
