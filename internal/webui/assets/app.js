"use strict";

const ui = {
  input: document.querySelector("#health-input"),
  count: document.querySelector("#char-count"),
  privacy: document.querySelector("#privacy-check"),
  search: document.querySelector("#search-toggle"),
  start: document.querySelector("#start-button"),
  sample: document.querySelector("#sample-button"),
  reset: document.querySelector("#reset-button"),
  error: document.querySelector("#error-message"),
  empty: document.querySelector("#empty-state"),
  working: document.querySelector("#working-state"),
  session: document.querySelector("#session-view"),
  events: document.querySelector("#agent-events"),
  clarificationPanel: document.querySelector("#clarification-panel"),
  clarificationQuestions: document.querySelector("#clarification-questions"),
  clarificationInput: document.querySelector("#clarification-input"),
  clarify: document.querySelector("#clarify-button"),
  review: document.querySelector("#review-panel"),
  visitGoal: document.querySelector("#visit-goal"),
  facts: document.querySelector("#facts-list"),
  factCount: document.querySelector("#fact-count"),
  questions: document.querySelector("#questions-list"),
  sourcesSection: document.querySelector("#sources-section"),
  sources: document.querySelector("#sources-list"),
  confirm: document.querySelector("#confirm-button"),
  completed: document.querySelector("#completed-panel"),
  export: document.querySelector("#export-button")
};

let currentSession = null;
let activeController = null;
let workspaceGeneration = 0;

const categoryLabels = {
  symptom: "症状",
  timeline: "时间变化",
  medication: "用药",
  allergy: "过敏",
  test: "检查",
  history: "既往情况",
  other: "其他"
};

const stepLabels = {
  extract: "提取事实",
  evidence: "原文校验",
  clarification: "补充信息",
  search: "可信检索",
  questions: "生成问题",
  guard: "安全检查",
  review: "人工确认"
};

ui.input.addEventListener("input", updateCount);
ui.sample.addEventListener("click", () => {
  ui.input.value = "三天前开始咳嗽，白天较轻，晚上躺下后更明显。目前没有服用止咳药，也没有已知药物过敏。";
  ui.privacy.checked = true;
  updateCount();
  ui.input.focus();
});
ui.start.addEventListener("click", startSession);
ui.clarify.addEventListener("click", submitClarification);
ui.confirm.addEventListener("click", confirmSession);
ui.reset.addEventListener("click", resetWorkspace);
updateCount();

async function startSession() {
  const generation = workspaceGeneration;
  clearError();
  if (!ui.privacy.checked) {
    showError("请先确认已移除可以直接识别个人身份的信息。");
    ui.privacy.focus();
    return;
  }
  if ([...ui.input.value.trim()].length < 20) {
    showError("请至少填写 20 个字符，让 Agent 有足够信息进行整理。");
    ui.input.focus();
    return;
  }

  setWorking(true, ui.start);
  try {
    const session = await request("/api/v1/sessions", {
      method: "POST",
      body: {
        input: ui.input.value,
        allow_web_search: ui.search.checked,
        privacy_confirmed: ui.privacy.checked
      }
    });
    if (generation !== workspaceGeneration) return;
    currentSession = session;
    renderSession(session);
  } catch (error) {
    if (error.name !== "AbortError" && generation === workspaceGeneration) {
      showError(error.message);
      showInitial();
    }
  } finally {
    if (generation === workspaceGeneration) setWorking(false, ui.start);
  }
}

async function submitClarification() {
  const generation = workspaceGeneration;
  clearError();
  if (!currentSession || !ui.clarificationInput.value.trim()) {
    showError("请填写补充信息后再继续。");
    return;
  }
  setWorking(true, ui.clarify);
  try {
    const session = await request(`/api/v1/sessions/${currentSession.id}/clarifications`, {
      method: "POST",
      body: { answer: ui.clarificationInput.value }
    });
    if (generation !== workspaceGeneration) return;
    currentSession = session;
    renderSession(session);
  } catch (error) {
    if (error.name !== "AbortError" && generation === workspaceGeneration) showError(error.message);
  } finally {
    if (generation === workspaceGeneration) setWorking(false, ui.clarify);
  }
}

async function confirmSession() {
  const generation = workspaceGeneration;
  clearError();
  if (!currentSession) return;
  setWorking(true, ui.confirm);
  try {
    const session = await request(`/api/v1/sessions/${currentSession.id}/confirm`, {
      method: "POST",
      body: { visit_goal: ui.visitGoal.value }
    });
    if (generation !== workspaceGeneration) return;
    currentSession = session;
    renderSession(session);
  } catch (error) {
    if (error.name !== "AbortError" && generation === workspaceGeneration) showError(error.message);
  } finally {
    if (generation === workspaceGeneration) setWorking(false, ui.confirm);
  }
}

async function request(url, options) {
  const controller = new AbortController();
  activeController = controller;
  const init = { method: options.method, headers: { "Content-Type": "application/json" }, signal: controller.signal };
  if (options.body) init.body = JSON.stringify(options.body);
  try {
    const response = await fetch(url, init);
    const payload = await response.json().catch(() => null);
    if (!response.ok) {
      throw new Error(payload?.error?.message || `请求失败（HTTP ${response.status}）`);
    }
    return payload.data;
  } finally {
    if (activeController === controller) activeController = null;
  }
}

function renderSession(session) {
  ui.empty.hidden = true;
  ui.working.hidden = true;
  ui.session.hidden = false;
  ui.reset.hidden = false;
  renderEvents(session.events || []);

  const waiting = session.status === "waiting_clarification";
  const reviewing = session.status === "waiting_review" || session.status === "completed";
  ui.clarificationPanel.hidden = !waiting;
  ui.review.hidden = !reviewing;
  ui.completed.hidden = session.status !== "completed";

  if (waiting) {
    ui.clarificationQuestions.replaceChildren(...(session.clarification_questions || []).map(question => {
      const paragraph = document.createElement("p");
      paragraph.textContent = question;
      return paragraph;
    }));
    ui.clarificationInput.value = "";
    ui.clarificationInput.focus();
  }

  if (reviewing) {
    ui.visitGoal.value = session.visit_goal || "";
    renderFacts(session.facts || []);
    renderQuestions(session.questions || []);
    renderSources(session.sources || []);
    ui.confirm.hidden = session.status === "completed";
  }

  if (session.status === "completed") {
    ui.visitGoal.disabled = true;
    ui.export.href = `/api/v1/sessions/${session.id}/export`;
    ui.completed.scrollIntoView({ behavior: reducedMotion() ? "auto" : "smooth", block: "nearest" });
  }
}

function renderEvents(events) {
	const latestByStep = new Map();
	for (const event of events) latestByStep.set(event.step, event);
	ui.events.replaceChildren(...[...latestByStep.values()].map(event => {
    const item = document.createElement("li");
    item.dataset.status = event.status;
    const title = document.createElement("strong");
    title.textContent = stepLabels[event.step] || event.step;
    title.title = event.message || "";
    const status = document.createElement("span");
    status.textContent = event.status === "degraded" ? "已降级" : event.status === "waiting" ? "等待输入" : "已完成";
    item.append(title, status);
    return item;
  }));
}

function renderFacts(facts) {
  ui.factCount.textContent = `${facts.length} 项`;
  ui.facts.replaceChildren(...facts.map(fact => {
    const row = document.createElement("div");
    row.className = "fact-row";
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
    row.append(category, content);
    return row;
  }));
}

function renderQuestions(questions) {
  ui.questions.replaceChildren(...questions.map(question => {
    const label = document.createElement("label");
    label.className = "question-row";
    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    const text = document.createElement("span");
    text.textContent = question.text;
    label.append(checkbox, text);
    return label;
  }));
}

function renderSources(sources) {
  ui.sourcesSection.hidden = sources.length === 0;
  ui.sources.replaceChildren(...sources.map(source => {
    const row = document.createElement("div");
    row.className = "source-row";
    const link = document.createElement("a");
    link.href = source.url;
    link.target = "_blank";
    link.rel = "noopener noreferrer";
    link.textContent = `${source.title || source.domain} · ${source.domain}`;
    const snippet = document.createElement("p");
    snippet.textContent = source.snippet || "可信来源链接";
    row.append(link, snippet);
    return row;
  }));
}

function setWorking(active, button) {
  button.disabled = active;
  if (button === ui.start) {
	ui.working.hidden = !active;
	if (active) ui.empty.hidden = true;
  }
}

function showInitial() {
  if (!currentSession) {
    ui.empty.hidden = false;
    ui.working.hidden = true;
    ui.session.hidden = true;
  }
}

function resetWorkspace() {
  workspaceGeneration += 1;
  if (activeController) activeController.abort();
  activeController = null;
  currentSession = null;
  ui.input.value = "";
  ui.privacy.checked = false;
  ui.clarificationInput.value = "";
  ui.visitGoal.disabled = false;
  ui.session.hidden = true;
  ui.reset.hidden = true;
  ui.empty.hidden = false;
  setWorking(false, ui.start);
  setWorking(false, ui.clarify);
  setWorking(false, ui.confirm);
  clearError();
  updateCount();
  ui.input.focus();
}

function updateCount() {
  ui.count.textContent = `${[...ui.input.value].length} / 6000`;
}

function showError(message) {
  ui.error.textContent = message;
  ui.error.hidden = false;
}

function clearError() {
  ui.error.hidden = true;
  ui.error.textContent = "";
}

function reducedMotion() {
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}
