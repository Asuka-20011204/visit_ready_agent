"use strict";

async function computeFactsDigest(facts) {
  const canonical = facts.map(fact => [fact.category || "", fact.content || "", fact.source_quote || "", fact.time_label || ""].join("\u001f") + "\u001e").join("");
  return sha256Hex(canonical);
}

function factID(fact, index) {
  return `${fact.category || "fact"}-${index}`;
}

function focusUnverifiedFact() {
  const first = [...document.querySelectorAll(".fact-row")].find(row => !row.querySelector("input")?.checked);
  const target = first || (!ui.insights.checked ? ui.insights.closest("fieldset") : null);
  target?.scrollIntoView({ behavior: reducedMotion() ? "auto" : "smooth", block: "center" });
  (first?.querySelector("input") || ui.insights)?.focus();
}

function profileEvidenceText(profile) {
  const quotes = [profile.source_quote, ...(profile.evidence_quotes || [])].filter((quote, index, values) => quote && values.indexOf(quote) === index);
  return `依据：${quotes.join("；补充：")}`;
}

function archiveClarificationExchange(panel, turn) {
  const archived = [panel.cloneNode(true), turn.cloneNode(true)];
  archived.forEach(item => {
    item.classList.add("archived-clarification");
    item.hidden = false;
    item.removeAttribute("id");
    item.querySelectorAll("[id]").forEach(child => child.removeAttribute("id"));
  });
  panel.before(...archived);
}

async function computeReviewDigest(session) {
  const values = ["visitready-review-v1"];
  appendReviewRecords(values, "symptom_profiles", session.symptom_profiles || [], profile => [
    profile.name, profile.onset, profile.duration, profile.frequency, profile.severity,
    profile.pattern, profile.trigger, profile.relieving_factors, profile.source_quote,
    String((profile.associated_symptoms || []).length), ...(profile.associated_symptoms || []),
    String((profile.evidence_quotes || []).length), ...(profile.evidence_quotes || [])
  ]);
  appendReviewRecords(values, "timeline", session.timeline || [], event => [event.time_label, event.event, event.source_quote]);
  appendReviewRecords(values, "risk_signals", session.risk_signals || [], signal => [signal.priority, signal.title, signal.evidence, signal.guidance, signal.source_quote]);
  appendReviewStrings(values, "missing_fields", session.missing_fields || []);
  appendReviewRecords(values, "uncertainties", session.uncertainties || [], item => [item.topic, item.detail, item.why, item.status, item.priority]);
  appendReviewRecords(values, "contradictions", session.contradictions || [], item => [item.topic, item.first_evidence, item.second_evidence, item.clarifying_question, item.priority]);
  const summary = session.conversation_summary || {};
  values.push(summary.headline || "", summary.intent || "");
  appendReviewStrings(values, "summary_confirmed", summary.confirmed || []);
  appendReviewStrings(values, "summary_open_threads", summary.open_threads || []);
  const interview = session.interview_state || {};
  values.push("interview_state", String(interview.turn_count || 0), String(interview.max_turns || 0), String(interview.confirmed_count || 0), String(interview.open_count || 0), interview.completion_reason || "");
  appendReviewRecords(values, "questions", session.questions || [], question => [question.text, question.source_url, question.reason, question.priority, question.category]);
  appendReviewRecords(values, "action_items", session.action_items || [], item => [item.title, item.detail, item.reason, item.priority, item.category]);
  appendReviewRecords(values, "sources", session.sources || [], source => [source.title, source.url, source.domain, source.snippet]);
  return sha256Hex(values.map(lengthPrefixed).join(""));
}

function appendReviewRecords(values, name, records, fields) {
  values.push(name, String(records.length));
  records.forEach(record => values.push(...fields(record).map(value => value || "")));
}

function appendReviewStrings(values, name, strings) {
  values.push(name, String(strings.length), ...strings);
}

function lengthPrefixed(value) {
  const text = String(value || "");
  return `${new TextEncoder().encode(text).length}:${text}`;
}

async function sha256Hex(value) {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  return [...new Uint8Array(digest)].map(byte => byte.toString(16).padStart(2, "0")).join("");
}
