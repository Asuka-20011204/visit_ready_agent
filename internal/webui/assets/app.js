"use strict";
const SESSION_STORAGE_KEY = "visitready.session_id";
const HISTORY_STORAGE_KEY = "visitready.session_ids";
const MAX_HISTORY_ITEMS = 8;
// Must exceed the server-side 45s workflow budget plus slack; otherwise the browser aborts the fetch first and the
// server reports a misleading upstream failure instead of its own timeout.
const REQUEST_TIMEOUT_MS = 60000;

const ui = {
  input: document.querySelector("#health-input"),
  inputForm: document.querySelector("#input-form"),
  inputCount: document.querySelector("#char-count"),
  search: document.querySelector("#search-toggle"),
  start: document.querySelector("#start-button"),
  clarificationForm: document.querySelector("#clarification-form"),
  clarificationInput: document.querySelector("#clarification-input"),
  clarificationCount: document.querySelector("#clarification-count"),
  clarificationHelp: document.querySelector("#clarification-help"),
  clarify: document.querySelector("#clarify-button"),
  inputError: document.querySelector("#input-error"),
  sessionError: document.querySelector("#session-error"),
  recoveryError: document.querySelector("#recovery-error"),
  empty: document.querySelector("#empty-state"),
  working: document.querySelector("#working-state"),
  workingStatus: document.querySelector("#working-status"),
  workingElapsed: document.querySelector("#working-elapsed"),
  cancelRun: document.querySelector("#cancel-run-button"),
  session: document.querySelector("#session-view"),
  assistantLead: document.querySelector("#assistant-lead"),
  initialUserTurn: document.querySelector("#initial-user-turn"),
  initialUserMessage: document.querySelector("#initial-user-message"),
  clarificationUserTurn: document.querySelector("#clarification-user-turn"),
  clarificationUserMessage: document.querySelector("#clarification-user-message"),
  clarificationPanel: document.querySelector("#clarification-panel"),
  clarificationQuestions: document.querySelector("#clarification-questions"),
  clarificationProgress: document.querySelector("#clarification-progress"),
  review: document.querySelector("#review-panel"),
  visitGoal: document.querySelector("#visit-goal"),
  facts: document.querySelector("#facts-list"),
  factCount: document.querySelector("#fact-count"),
  reviewProgress: document.querySelector("#review-progress"),
  reviewBadge: document.querySelector("#review-badge"),
  reviewActions: document.querySelector("#review-actions"),
  insights: document.querySelector("#insights-check"),
  riskSection: document.querySelector("#risk-section"), riskSignals: document.querySelector("#risk-signals"),
  profilesSection: document.querySelector("#symptom-profiles-section"), profiles: document.querySelector("#symptom-profiles"),
  timelineSection: document.querySelector("#clinical-timeline-section"), timeline: document.querySelector("#clinical-timeline"),
  missingSection: document.querySelector("#missing-section"), missingContext: document.querySelector("#missing-context"),
  nextActionsSection: document.querySelector("#next-actions-section"), nextActions: document.querySelector("#next-actions"),
  questions: document.querySelector("#questions-list"),
  sourcesSection: document.querySelector("#sources-section"),
  sources: document.querySelector("#sources-list"),
  confirm: document.querySelector("#confirm-button"),
  completed: document.querySelector("#completed-panel"),
  export: document.querySelector("#export-button"),
  emergency: document.querySelector("#emergency-panel"),
  emergencyMessage: document.querySelector("#emergency-message"),
  emergencyExport: document.querySelector("#emergency-export-button"),
  failed: document.querySelector("#failed-panel"),
  failedMessage: document.querySelector("#failed-message"),
  failedDetail: document.querySelector("#failed-detail"),
  retryRun: document.querySelector("#retry-run-button"),
  events: document.querySelector("#agent-events"),
  runSummary: document.querySelector("#run-summary"),
  recoveryNotice: document.querySelector("#recovery-notice"),
  expired: document.querySelector("#expired-state"),
  expiry: document.querySelector("#session-expiry"),
  historyList: document.querySelector("#history-list"),
  historyEmpty: document.querySelector("#history-empty"),
  historyToast: document.querySelector("#history-toast"),
	  historyToastMessage: document.querySelector("#history-toast-message"),
  undoHistory: document.querySelector("#undo-history-button"),
	  deleteSession: document.querySelector("#delete-session-button"),
	  deleteSessionDialog: document.querySelector("#delete-session-dialog"),
	  cancelDeleteSession: document.querySelector("#cancel-delete-session-button"),
	  confirmDeleteSession: document.querySelector("#confirm-delete-session-button"),
  threadTitle: document.querySelector("#thread-title"),
  threadViewport: document.querySelector("#conversation"),
  liveAnnouncer: document.querySelector("#live-announcer"),
  composerState: document.querySelector("#composer-state")
  ,conversationSummary: document.querySelector("#conversation-summary")
  ,summaryConfirmed: document.querySelector("#summary-confirmed")
  ,interviewMetrics: document.querySelector("#interview-metrics")
  ,interviewMeterBar: document.querySelector("#interview-meter-bar")
  ,uncertaintySection: document.querySelector("#uncertainty-section")
  ,uncertaintyList: document.querySelector("#uncertainty-list")
  ,contradictionSection: document.querySelector("#contradiction-section")
  ,contradictionList: document.querySelector("#contradiction-list")
  ,addContext: document.querySelector("#add-context-button")
	,deviceAccess: document.querySelector("#device-access-button")
	,deviceAccessDialog: document.querySelector("#device-access-dialog")
	,deviceAccessKey: document.querySelector("#device-access-key")
	,deviceKeyStatus: document.querySelector("#device-key-status")
	,copyDeviceKey: document.querySelector("#copy-device-key-button")
	,importDeviceKey: document.querySelector("#import-device-key-button")
	,accountButton: document.querySelector("#account-button")
	,accountEmail: document.querySelector("#account-email")
	,accountDialog: document.querySelector("#account-dialog")
	,accountForm: document.querySelector("#account-form")
	,accountEmailInput: document.querySelector("#account-email-input")
	,accountPasswordInput: document.querySelector("#account-password-input")
	,accountError: document.querySelector("#account-error")
	,accountModeButton: document.querySelector("#account-mode-button")
	,accountSubmitButton: document.querySelector("#account-submit-button")
	,accountMenuDialog: document.querySelector("#account-menu-dialog")
	,accountMenuEmail: document.querySelector("#account-menu-email")
	,accountLogout: document.querySelector("#account-logout-button")
	,accountMenuClose: document.querySelector("#account-menu-close-button")
};

const categoryLabels = {
  symptom: "症状", timeline: "时间变化", medication: "用药", allergy: "过敏",
  test: "检查", history: "既往情况", other: "其他"
};
const stepLabels = {
  extract: "提取事实", evidence: "原文校验", clarification: "补充信息", search: "可信检索",
  questions: "生成问题", validator: "边界验证", emergency: "紧急安全处理", guard: "安全检查", review: "人工确认"
};
const nodeLabels = {
  extract_facts: "提取明确事实", validate_extraction: "校验提取边界", verify_evidence: "核对原文证据",
  emergency_escalation: "检查紧急风险", trusted_search: "检索可信资料", generate_questions: "生成沟通建议",
  validate_questions: "验证问题边界", output_guard: "执行最终安全检查"
};
const eventStatusLabels = {
  "running": "进行中", "completed": "已完成", "waiting": "等待输入",
  "degraded": "已降级", "skipped": "已跳过", "failed": "失败"
};
const sessionStatusLabels = {
  waiting_clarification: "等待补充", waiting_review: "等待核对", completed: "已完成", emergency: "紧急安全处理", failed: "处理失败"
};
const priorityLabels = { urgent: "安全优先", high: "重点", normal: "常规" };
let currentSession = null;
let activeController = null;
let expiryTimer = null;
let workingTimer = null;
let workingStartedAt = 0;
let workingHeadersReceived = false;
let activeWorkingButton = null;
let pendingHistoryRemoval = null;
let historyUndoTimer = null;
let workspaceGeneration = 0;
let currentRawInput = "";
let historyIDs = readHistoryIDs();
const historySessions = new Map();
const verifiedFactIDs = new Set();
const accountRequired = document.body.dataset.mode === "live";
let accountMode = "login";
let currentAccount = null;

ui.inputForm.addEventListener("submit", event => { event.preventDefault(); startSession(); });
ui.clarificationForm.addEventListener("submit", event => { event.preventDefault(); submitClarification(); });
ui.cancelRun.addEventListener("click", cancelActiveRequest);
ui.retryRun.addEventListener("click", retryFailedRun);
ui.undoHistory.addEventListener("click", undoHistoryRemoval);
ui.deleteSession.addEventListener("click", () => ui.deleteSessionDialog.showModal());
ui.cancelDeleteSession.addEventListener("click", () => ui.deleteSessionDialog.close());
ui.confirmDeleteSession.addEventListener("click", permanentlyDeleteCurrentSession);
ui.confirm.addEventListener("click", confirmSession);
ui.deviceAccess?.addEventListener("click", openDeviceAccessDialog);
ui.copyDeviceKey?.addEventListener("click", copyDeviceKey);
ui.importDeviceKey?.addEventListener("click", importDeviceKey);
ui.accountForm?.addEventListener("submit", event => { event.preventDefault(); submitAccountForm(); });
ui.accountModeButton?.addEventListener("click", toggleAccountMode);
ui.accountButton?.addEventListener("click", () => { ui.accountMenuEmail.textContent = currentAccount?.email || ""; ui.accountMenuDialog.showModal(); });
ui.accountLogout?.addEventListener("click", logoutAccount);
ui.accountMenuClose?.addEventListener("click", () => ui.accountMenuDialog.close());
ui.export.addEventListener("click", event => downloadSessionExport(event, "visit-ready.md"));
ui.emergencyExport.addEventListener("click", event => downloadSessionExport(event, "visit-ready-emergency.md"));
ui.insights.addEventListener("change", () => updateReviewProgress(currentSession?.facts || []));
ui.addContext.addEventListener("click", () => {
  ui.clarificationInput.value = ui.clarificationInput.value.trim() ? `${ui.clarificationInput.value.trim()}\n我还想补充：` : "我还想补充：";
  autoSize(ui.clarificationInput); updateClarificationCount(); ui.clarificationInput.focus();
});
ui.input.addEventListener("input", () => { autoSize(ui.input); updateCount(); });
ui.clarificationInput.addEventListener("input", () => { autoSize(ui.clarificationInput); updateClarificationCount(); });
ui.input.addEventListener("keydown", event => submitOnEnter(event, ui.inputForm));
ui.clarificationInput.addEventListener("keydown", event => submitOnEnter(event, ui.clarificationForm));
document.querySelector("#new-conversation-button").addEventListener("click", resetWorkspace);
document.querySelector("#expired-reset-button").addEventListener("click", resetWorkspace);
document.querySelector("#find-unreviewed-button").addEventListener("click", focusUnverifiedFact);
document.querySelector("#rail-toggle").addEventListener("click", toggleRail);
document.querySelectorAll("[data-prompt]").forEach(button => button.addEventListener("click", () => appendPrompt(button.dataset.prompt)));
window.addEventListener("DOMContentLoaded", initializeWorkspace);
window.addEventListener("storage", syncHistoryFromStorage);
renderHistory();
updateCount();

async function startSession() {
  if (ui.start.disabled) return;
  const generation = ++workspaceGeneration;
  if (activeController) activeController.abort();
  clearSectionError("input");
  clearSectionError("recovery");
  ui.recoveryNotice.hidden = true;
  const input = ui.input.value.trim();
	if (!input) {
	showSectionError("input", "请先填写需要整理的健康情况。");
    ui.input.focus();
    return;
  }

  currentRawInput = input;
  renderInitialUserTurn(input);
  setWorking(true, ui.start);
  try {
    const session = await request("/api/v1/sessions", {
      method: "POST",
      body: { input, allow_web_search: ui.search.checked },
	  onHeaders: markWorkingHeadersReceived,
	  streamProgress: true
    });
    if (generation !== workspaceGeneration) return;
    currentSession = session;
    persistSession(session);
    renderSession(session);
  } catch (error) {
    if (error.name !== "AbortError" && generation === workspaceGeneration) {
      if (!renderRecoverableFailure(error)) {
        showSectionError("input", error.message);
        renderComposerMode("initial");
      }
    }
  } finally {
    if (generation === workspaceGeneration) setWorking(false, ui.start);
  }
}

async function submitClarification() {
  if (ui.clarify.disabled) return;
  const generation = workspaceGeneration;
  clearSectionError("session");
  const answer = ui.clarificationInput.value.trim();
  if (!currentSession || !answer) {
    showSectionError("session", "请填写补充信息后再继续。");
    return;
  }
  renderClarificationUserTurn(answer);
  setWorking(true, ui.clarify);
  try {
    const session = await request(`/api/v1/sessions/${currentSession.id}/clarifications`, {
	  method: "POST", body: { answer }, streamProgress: true
    });
    if (generation !== workspaceGeneration) return;
    archiveClarificationExchange(ui.clarificationPanel, ui.clarificationUserTurn);
    ui.clarificationUserTurn.hidden = true;
    ui.clarificationInput.value = "";
    autoSize(ui.clarificationInput);
    updateClarificationCount();
    currentSession = session;
    persistSession(session);
    renderSession(session);
  } catch (error) {
    if (error.name !== "AbortError" && generation === workspaceGeneration && !renderRecoverableFailure(error)) {
      showSectionError("session", error.message);
    }
  } finally {
    if (generation === workspaceGeneration) setWorking(false, ui.clarify);
  }
}

async function confirmSession() {
  if (ui.confirm.disabled) return;
  const generation = workspaceGeneration;
  clearSectionError("session");
  if (!currentSession) return;
  setWorking(true, ui.confirm);
  try {
    const [factsDigest, reviewDigest] = await Promise.all([
      computeFactsDigest(currentSession.facts || []),
      computeReviewDigest(currentSession)
    ]);
    const session = await request(`/api/v1/sessions/${currentSession.id}/confirm`, {
      method: "POST",
      body: {
        visit_goal: ui.visitGoal.value,
        facts_acknowledged: true,
        facts_digest: factsDigest,
        insights_acknowledged: true,
        review_digest: reviewDigest
      }
    });
    if (generation !== workspaceGeneration) return;
    currentSession = session;
    persistSession(session);
    renderSession(session);
  } catch (error) {
    if (error.name !== "AbortError" && generation === workspaceGeneration) showSectionError("session", error.message);
  } finally {
    if (generation === workspaceGeneration) setWorking(false, ui.confirm);
  }
}

function renderSession(session, options = {}) {
  clearSectionError("session");
  ui.empty.hidden = true;
  ui.working.hidden = true;
  ui.expired.hidden = true;
  ui.session.hidden = false;
  ui.threadTitle.textContent = sessionTitle(session);
	ui.deleteSession.hidden = false;
  const emergency = session.status === "emergency";
  const failed = session.status === "failed";
  ui.visitGoal.disabled = session.status === "completed" || emergency;
  renderEvents(session.events || []);
  if (!scheduleSessionExpiry(session)) return;

  const waiting = session.status === "waiting_clarification";
  const reviewing = session.status === "waiting_review" || session.status === "completed";
  ui.emergency.hidden = !emergency;
  ui.failed.hidden = !failed;
  ui.clarificationPanel.hidden = !waiting;
  ui.review.hidden = !reviewing;
  ui.completed.hidden = session.status !== "completed";
  ui.reviewBadge.textContent = session.status === "completed" ? "已确认" : "等待确认";
  ui.reviewActions.hidden = session.status === "completed";

  if (emergency) {
    ui.assistantLead.textContent = "已停止后续模型、检索和追问步骤，并进入紧急安全处理。";
    ui.emergencyMessage.textContent = session.emergency_message || "这些表现正在发生或明显加重，请立即联系当地急救服务或前往急诊。";
    ui.emergencyExport.href = `/api/v1/sessions/${session.id}/export`;
    ui.emergency.scrollIntoView({ behavior: reducedMotion() ? "auto" : "smooth", block: "nearest" });
  } else if (failed) {
    const failure = session.failure || {};
    ui.assistantLead.textContent = "处理在完成前中断，但这次会话和必要上下文仍然保留。";
    ui.failedMessage.textContent = failure.message || "本次内容已保留，可以从中断处重新尝试，无需再次填写。";
    ui.failedDetail.textContent = failureDetail(failure);
    ui.retryRun.hidden = !failure.retryable;
    ui.retryRun.disabled = !failure.retryable;
    ui.failed.scrollIntoView({ behavior: reducedMotion() ? "auto" : "smooth", block: "nearest" });
  } else {
    ui.assistantLead.textContent = "我已完成第一轮整理。先看看处理记录，再核对结果。";
  }

  if (waiting) {
    const state = session.interview_state || {};
    const maxTurns = state.max_turns || 4;
    ui.clarificationHelp.textContent = `第 ${Math.min((session.clarification_count || 0) + 1, maxTurns)} / ${maxTurns} 轮 · 回答完整时会提前结束`;
    ui.clarificationProgress.textContent = `${state.confirmed_count || 0} 个细节已明确 · ${state.open_count || (session.missing_fields || []).length} 个重点待补充`;
    renderClarificationQuestions(session.clarification_prompts || session.clarification_questions || []);
  }
  if (reviewing) {
    if (options.restored || session.status !== "completed") verifiedFactIDs.clear();
    if (session.status === "completed") (session.facts || []).forEach((fact, index) => verifiedFactIDs.add(factID(fact, index)));
    ui.insights.checked = session.status === "completed";
    ui.insights.disabled = session.status === "completed";
    ui.visitGoal.value = session.visit_goal || "";
    renderRiskSignals(session.risk_signals || []);
    renderSymptomProfiles(session.symptom_profiles || []);
    renderClinicalTimeline(session.timeline || []);
    renderMissingContext(session.missing_fields || []);
    renderConversationSummary(session.conversation_summary || {}, session.interview_state || {});
    renderUncertainties(session.uncertainties || []);
    renderContradictions(session.contradictions || []);
    renderFacts(session.facts || []);
    renderQuestions(session.questions || []);
    renderActionItems(session.action_items || [], session.questions || []);
    renderSources(session.sources || []);
    ui.confirm.hidden = session.status === "completed";
  }
  if (session.status === "completed") {
    ui.export.href = `/api/v1/sessions/${session.id}/export`;
    ui.completed.scrollIntoView({ behavior: reducedMotion() ? "auto" : "smooth", block: "nearest" });
  }
  renderComposerMode(emergency ? "emergency" : failed ? "failed" : waiting ? "clarification" : session.status === "waiting_review" ? "review" : session.status === "completed" ? "completed" : "hidden");
  if (waiting) revealLatestConversation();
  ui.liveAnnouncer.textContent = emergency ? "已进入紧急安全处理，请立即联系当地急救服务或前往急诊。" : failed ? "处理暂时中断，本次内容已保留。" : session.status === "completed" ? "诊前清单已经准备好。" : waiting ? "Agent 需要你补充一轮信息。" : "Agent 已完成整理，等待你核对事实。";
  renderHistory();
}

function revealLatestConversation() {
  requestAnimationFrame(() => {
    ui.threadViewport.scrollTo({
      top: ui.threadViewport.scrollHeight,
      behavior: reducedMotion() ? "auto" : "smooth"
    });
  });
}

function renderConversationSummary(summary, interview) {
  ui.conversationSummary.textContent = summary.headline || summary.intent || "Agent 已根据原文建立本次诊前沟通脉络。";
  const confirmed = summary.confirmed || [];
  ui.summaryConfirmed.replaceChildren(...confirmed.slice(0, 5).map(value => {
    const item = document.createElement("li"); item.textContent = value; return item;
  }));
  const confirmedCount = interview.confirmed_count || confirmed.length;
  const openCount = interview.open_count || (summary.open_threads || []).length;
  const total = confirmedCount + openCount;
  ui.interviewMetrics.textContent = `${confirmedCount} 已明确 · ${openCount} 待处理`;
  ui.interviewMeterBar.style.transform = `scaleX(${total ? Math.max(.08, confirmedCount / total) : 0})`;
}

function renderUncertainties(items) {
  ui.uncertaintySection.hidden = items.length === 0;
  ui.uncertaintyList.replaceChildren(...items.map(item => {
    const row = document.createElement("article");
    const heading = document.createElement("strong"); heading.textContent = item.topic || "待确认信息";
    const detail = document.createElement("p"); detail.textContent = item.detail;
    const why = document.createElement("small"); why.textContent = item.why;
    row.append(heading, detail, why); return row;
  }));
}

function renderContradictions(items) {
  ui.contradictionSection.hidden = items.length === 0;
  ui.contradictionList.replaceChildren(...items.map(item => {
    const row = document.createElement("article");
    const heading = document.createElement("strong"); heading.textContent = item.topic;
    const evidence = document.createElement("div"); evidence.className = "evidence-compare";
    [item.first_evidence, item.second_evidence].forEach(value => { const quote = document.createElement("q"); quote.textContent = value; evidence.append(quote); });
    const question = document.createElement("p"); question.textContent = item.clarifying_question;
    row.append(heading, evidence, question); return row;
  }));
}
function renderInitialUserTurn(message) {
  ui.empty.hidden = true;
  ui.initialUserTurn.hidden = false;
  ui.initialUserMessage.textContent = message;
}
function renderClarificationUserTurn(message) {
  ui.clarificationUserTurn.hidden = false;
  ui.clarificationUserMessage.textContent = message;
}
function renderClarificationQuestions(questions) {
  ui.clarificationQuestions.replaceChildren(...questions.slice(0, 3).map(question => {
    const item = document.createElement("li");
    const prompt = typeof question === "string" ? { text: question } : question;
    const line = document.createElement("span");
    line.textContent = prompt.text;
    item.append(priorityBadge(prompt.priority), line);
    if (prompt.reason) {
      const reason = document.createElement("small");
      reason.textContent = `为什么问：${prompt.reason}`;
      item.append(reason);
    }
    return item;
  }));
}

function renderEvents(events) {
  ui.runSummary.textContent = `${events.length} 条记录`;
  ui.events.replaceChildren(...events.map(event => {
    const item = document.createElement("li");
    item.dataset.status = event.status;
    const title = document.createElement("strong");
    title.textContent = `${stepLabels[event.step] || event.step} · ${eventStatusLabels[event.status] || "已记录"}`;
    const message = document.createElement("span");
    message.textContent = event.message || "该步骤未提供附加说明";
    const time = document.createElement("small");
    time.textContent = event.created_at ? formatTime(event.created_at) : "";
    item.setAttribute("aria-label", `${title.textContent}：${message.textContent}`);
    item.append(title, message, time);
    return item;
  }));
}

function renderFacts(facts) {
  ui.factCount.textContent = `${facts.length} 项`;
  const normalizedFacts = facts.map((fact, index) => ({ ...fact, id: factID(fact, index) }));
  ui.facts.replaceChildren(...normalizedFacts.map(fact => {
    const row = document.createElement("div");
    row.className = "fact-row";
    row.dataset.factId = fact.id;
    const category = document.createElement("span");
    category.className = "fact-category";
    category.textContent = categoryLabels[fact.category] || "其他";
    const content = document.createElement("div");
    const value = document.createElement("p");
    value.className = "fact-content";
    value.textContent = fact.content;
    const quote = document.createElement("p");
    quote.className = "fact-quote";
    quote.textContent = `原文依据：${fact.source_quote}`;
    content.append(value, quote);
    const verification = document.createElement("label");
    verification.className = "fact-verification";
    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.id = `fact-verification-${fact.id}`;
    checkbox.setAttribute("aria-label", `核对事实：${fact.content}`);
    checkbox.checked = verifiedFactIDs.has(fact.id);
    checkbox.disabled = currentSession?.status === "completed";
    checkbox.addEventListener("change", () => {
      if (checkbox.checked) verifiedFactIDs.add(fact.id); else verifiedFactIDs.delete(fact.id);
      updateReviewProgress(normalizedFacts);
    });
    const label = document.createElement("span");
    label.textContent = "已核对";
    verification.append(checkbox, label);
    row.append(category, content, verification);
    return row;
  }));
  updateReviewProgress(normalizedFacts);
}
function updateReviewProgress(facts) {
  const reviewFacts = facts.map((fact, index) => ({ ...fact, id: fact.id || factID(fact, index) }));
  const count = reviewFacts.filter(fact => verifiedFactIDs.has(fact.id)).length;
  const allFactsVerified = reviewFacts.length > 0 && reviewFacts.every(fact => verifiedFactIDs.has(fact.id));
  ui.reviewProgress.textContent = `已核对 ${count} / ${reviewFacts.length}`;
  ui.confirm.disabled = currentSession?.status === "completed" || !allFactsVerified || !ui.insights.checked;
}

function renderRiskSignals(riskSignals) {
  ui.riskSection.hidden = riskSignals.length === 0;
  ui.riskSignals.replaceChildren(...riskSignals.map(signal => {
    const row = document.createElement("article");
    row.className = "risk-signal";
    const heading = document.createElement("div");
    const title = document.createElement("strong");
    title.textContent = signal.title || "需要优先说明的表现";
    heading.append(priorityBadge(signal.priority), title);
    const guidance = document.createElement("p");
    guidance.textContent = signal.guidance;
    const evidence = document.createElement("small");
    evidence.textContent = `原文依据：${signal.source_quote || signal.evidence}`;
    row.append(heading, guidance, evidence);
    return row;
  }));
}
function renderSymptomProfiles(profiles) {
  ui.profilesSection.hidden = profiles.length === 0;
  const fields = [["onset", "开始"], ["duration", "持续"], ["frequency", "频率"], ["severity", "程度"], ["pattern", "规律"], ["trigger", "诱因"], ["relieving_factors", "缓解"]];
  ui.profiles.replaceChildren(...profiles.map(profile => {
    const record = document.createElement("article");
    record.className = "profile-record";
    const title = document.createElement("h4");
    title.textContent = profile.name;
    const details = document.createElement("dl");
    fields.filter(([key]) => profile[key]).forEach(([key, label]) => details.append(detailPair(label, profile[key])));
    if (profile.associated_symptoms?.length) details.append(detailPair("伴随", profile.associated_symptoms.join("、")));
    const quote = document.createElement("small");
    quote.textContent = profileEvidenceText(profile);
    record.append(title, details, quote);
    return record;
  }));
}
function renderClinicalTimeline(timeline) {
  ui.timelineSection.hidden = timeline.length === 0;
  ui.timeline.replaceChildren(...timeline.map(event => {
    const item = document.createElement("li");
    const time = document.createElement("time");
    time.textContent = event.time_label || "时间待补充";
    const text = document.createElement("p");
    text.textContent = event.event;
    item.append(time, text);
    return item;
  }));
}
function renderMissingContext(fields) {
  ui.missingSection.hidden = fields.length === 0;
  ui.missingContext.replaceChildren(...fields.map(field => {
    const item = document.createElement("li");
    item.textContent = field;
    return item;
  }));
}
function renderQuestions(questions) {
  ui.questions.replaceChildren(...questions.map(question => {
    const item = document.createElement("li");
    const label = document.createElement("label");
    label.className = "question-row";
    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    const content = document.createElement("span");
    const text = document.createElement("strong");
    text.textContent = question.text;
    content.append(priorityBadge(question.priority), text);
    if (question.reason) {
      const reason = document.createElement("small");
      reason.textContent = `沟通目的：${question.reason}`;
      content.append(reason);
    }
    label.append(checkbox, content);
    item.append(label);
    return item;
  }));
}
function renderActionItems(actionItems, questions) {
  const fallback = questions.filter(question => question.category === "visit_preparation").map(question => ({ title: question.text, reason: question.reason, priority: question.priority }));
  const items = actionItems.length ? actionItems : fallback;
  ui.nextActionsSection.hidden = items.length === 0;
  ui.nextActions.replaceChildren(...items.map((action, index) => {
    const item = document.createElement("li");
    const order = document.createElement("span");
    order.textContent = String(index + 1).padStart(2, "0");
    const content = document.createElement("div");
    const title = document.createElement("strong");
    title.textContent = action.title;
    const detail = document.createElement("p");
    detail.textContent = action.detail || action.reason || "";
    content.append(title, detail);
    if (action.reason && action.detail) {
      const reason = document.createElement("small");
      reason.textContent = `作用：${action.reason}`;
      content.append(reason);
    }
    item.append(order, content, priorityBadge(action.priority));
    return item;
  }));
}
function priorityBadge(priority) {
  const badge = document.createElement("span");
  const normalized = priorityLabels[priority] ? priority : "normal";
  badge.className = `priority-badge priority-${normalized}`;
  badge.textContent = priorityLabels[normalized];
  return badge;
}
function detailPair(label, value) {
  const pair = document.createDocumentFragment();
  const term = document.createElement("dt");
  const detail = document.createElement("dd");
  term.textContent = label;
  detail.textContent = value;
  pair.append(term, detail);
  return pair;
}

function renderSources(sources) {
  ui.sourcesSection.hidden = sources.length === 0;
  ui.sources.replaceChildren(...sources.map(source => {
    const row = document.createElement("div");
    row.className = "source-row";
    const link = document.createElement("a");
    link.href = safeSourceURL(source.url);
    link.target = "_blank";
    link.rel = "noopener noreferrer";
    link.textContent = `${source.title || source.domain} · ${source.domain}`;
    const snippet = document.createElement("p");
    snippet.textContent = source.snippet || "可信来源链接";
    row.append(link, snippet);
    return row;
  }));
}
function renderComposerMode(mode) {
  ui.inputForm.hidden = mode !== "initial";
  ui.clarificationForm.hidden = mode !== "clarification";
  const labels = {
    initial: "等待你的描述",
    clarification: "等待补充信息",
    review: "等待事实核对",
    completed: "本次整理已完成",
    emergency: "已停止 Agent 流程，请立即寻求紧急帮助",
    failed: "处理已暂停，可以重新尝试",
    hidden: "Agent 上下文已暂停"
  };
  ui.composerState.textContent = labels[mode] || labels.hidden;
  if (mode === "clarification") requestAnimationFrame(() => ui.clarificationInput.focus({ preventScroll: true }));
}

function setWorking(active, button) {
  button.disabled = active;
  button.setAttribute("aria-busy", String(active));
  ui.threadViewport.setAttribute("aria-busy", String(active));
  ui.working.hidden = !active;
  if (active) {
    activeWorkingButton = button;
    ui.composerState.textContent = "Agent 正在处理";
    startWorkingTimer();
    renderComposerMode("hidden");
  } else {
    stopWorkingTimer();
    if (activeWorkingButton === button) activeWorkingButton = null;
  }
  if (!active && currentSession) {
    const mode = currentSession.status === "emergency" ? "emergency" : currentSession.status === "failed" ? "failed" : currentSession.status === "waiting_clarification" ? "clarification" : currentSession.status === "waiting_review" ? "review" : currentSession.status === "completed" ? "completed" : "hidden";
    ui.composerState.textContent = ({ emergency: "已停止 Agent 流程，请立即寻求紧急帮助", failed: "处理已暂停，可以重新尝试", clarification: "等待补充信息", review: "等待事实核对", completed: "本次整理已完成", hidden: "Agent 上下文已暂停" })[mode];
  }
}

function startWorkingTimer() {
  stopWorkingTimer();
  workingStartedAt = Date.now();
  workingHeadersReceived = false;
  ui.workingStatus.textContent = "请求已发送，等待 AI 生成结果";
  updateWorkingTimer();
  workingTimer = window.setInterval(updateWorkingTimer, 1000);
}

function markWorkingHeadersReceived() {
  workingHeadersReceived = true;
  ui.workingStatus.textContent = "AI 已接收请求，正在生成结构化结果";
}

function updateWorkingTimer() {
  const elapsedSeconds = Math.max(0, Math.floor((Date.now() - workingStartedAt) / 1000));
  ui.workingElapsed.textContent = `已等待 ${elapsedSeconds} 秒`;
  if (elapsedSeconds >= 45 && workingHeadersReceived) {
    ui.workingStatus.textContent = "AI 仍在生成，复杂描述可能需要更久";
  }
}

function stopWorkingTimer() {
  if (workingTimer) window.clearInterval(workingTimer);
  workingTimer = null;
}

function cancelActiveRequest() {
  if (!activeController) return;
  const canceledButton = activeWorkingButton;
  workspaceGeneration += 1;
  clearActiveWork({ abort: true });
  if (currentSession && canceledButton === ui.clarify) {
    ui.clarificationUserTurn.hidden = true;
    renderComposerMode("clarification");
    ui.clarificationInput.focus();
  } else if (currentSession && canceledButton === ui.confirm) {
    renderComposerMode("review");
    ui.confirm.focus();
  } else if (currentSession && canceledButton === ui.retryRun) {
    renderSession(currentSession);
    ui.retryRun.focus();
  } else {
    clearThreadState();
    ui.empty.hidden = false;
    renderComposerMode("initial");
    ui.input.focus();
  }
  ui.composerState.textContent = "已停止等待，可以修改后重试";
  ui.liveAnnouncer.textContent = "已停止等待，输入内容仍保留";
}

function clearActiveWork({ abort = false } = {}) {
  if (abort && activeController) activeController.abort();
  activeController = null;
  activeWorkingButton = null;
  stopWorkingTimer();
  resetButtonState(ui.start);
  resetButtonState(ui.clarify);
  resetButtonState(ui.confirm);
  resetButtonState(ui.retryRun);
  ui.threadViewport.setAttribute("aria-busy", "false");
  ui.working.hidden = true;
}

function resetButtonState(button) {
  button.disabled = false;
  button.setAttribute("aria-busy", "false");
}

function showSectionError(section, message) {
  const target = section === "input" ? ui.inputError : section === "recovery" ? ui.recoveryError : ui.sessionError;
  target.textContent = message;
  target.hidden = false;
  target.focus();
}

function clearSectionError(section) {
  const target = section === "input" ? ui.inputError : section === "recovery" ? ui.recoveryError : ui.sessionError;
  target.textContent = "";
  target.hidden = true;
}

function clearAllErrors() {
  clearSectionError("input");
  clearSectionError("session");
  clearSectionError("recovery");
}

function updateCount() {
  ui.inputCount.textContent = `${[...ui.input.value].length} / 6000`;
  if (ui.input.value.trim()) clearSectionError("input");
}

function updateClarificationCount() {
  ui.clarificationCount.textContent = `${[...ui.clarificationInput.value].length} / 2000`;
}

function appendPrompt(prompt) {
  ui.input.value = ui.input.value.trim() ? `${ui.input.value.trim()}\n${prompt}` : prompt;
  autoSize(ui.input);
  updateCount();
  ui.input.focus();
}

function submitOnEnter(event, form) {
  if (event.key !== "Enter" || event.shiftKey || event.isComposing) return;
  if (form.querySelector("button[type='submit']")?.disabled) return;
  event.preventDefault();
  form.requestSubmit();
}

function autoSize(textarea) {
  textarea.style.height = "auto";
  textarea.style.height = `${Math.min(textarea.scrollHeight, 150)}px`;
}

function toggleRail() {
  const collapsed = document.body.classList.toggle("rail-collapsed");
  const button = document.querySelector("#rail-toggle");
  button.setAttribute("aria-expanded", String(!collapsed));
  button.setAttribute("aria-label", collapsed ? "展开往期会话" : "收起往期会话");
  button.textContent = collapsed ? "›" : "‹";
}

function safeSourceURL(value) {
  try {
    const url = new URL(value);
    return url.protocol === "https:" ? url.href : "about:blank";
  } catch {
    return "about:blank";
  }
}

function reducedMotion() {
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

function showAccount(account) {
  currentAccount = account;
  ui.accountButton.hidden = false;
  ui.accountEmail.textContent = account?.email || "账户";
}

function toggleAccountMode() {
  accountMode = accountMode === "login" ? "register" : "login";
  ui.accountSubmitButton.textContent = accountMode === "login" ? "登录" : "创建账户";
  ui.accountModeButton.textContent = accountMode === "login" ? "创建账户" : "返回登录";
  ui.accountPasswordInput.autocomplete = accountMode === "login" ? "current-password" : "new-password";
  ui.accountError.hidden = true;
}

async function submitAccountForm() {
  const email = ui.accountEmailInput.value.trim();
  const password = ui.accountPasswordInput.value;
  if (!email || password.length < 12) { showAccountError("请输入邮箱和至少 12 位密码。"); return; }
  ui.accountSubmitButton.disabled = true;
  try {
    const endpoint = accountMode === "login" ? "login" : "register";
    const response = await fetch(`/api/v1/auth/${endpoint}`, {
      method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ email, password })
    });
    const payload = await response.json();
    if (!response.ok) throw new Error(payload?.error?.message || "账户操作失败。");
    showAccount(payload.data);
    ui.accountDialog.close();
    await hydrateServerHistory();
  } catch (error) {
    showAccountError(error.message || "账户操作失败，请稍后重试。");
  } finally { ui.accountSubmitButton.disabled = false; }
}

function showAccountError(message) {
  ui.accountError.textContent = message;
  ui.accountError.hidden = false;
}

async function logoutAccount() {
  await fetch("/api/v1/auth/logout", { method: "POST", credentials: "same-origin" });
  ui.accountMenuDialog.close();
  currentAccount = null;
  ui.accountButton.hidden = true;
  resetWorkspace();
  ui.accountDialog.showModal();
}
