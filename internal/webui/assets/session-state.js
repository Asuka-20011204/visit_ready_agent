"use strict";

const DEVICE_KEY_STORAGE_KEY = "visitready.device_key.v1";
const ACCOUNT_SCOPE_STORAGE_KEY = "visitready.account_email.v1";
let ephemeralDeviceKey = "";

// Clears locally cached history and restore state when the signed-in account
// changes, so records from a previous account never stay visible or mix in.
function switchAccountScope(email) {
	const scope = String(email || "").trim().toLowerCase();
	let previous = null;
	try {
		previous = localStorage.getItem(ACCOUNT_SCOPE_STORAGE_KEY);
		localStorage.setItem(ACCOUNT_SCOPE_STORAGE_KEY, scope);
	} catch {
		// Storage may be unavailable; fall back to in-memory comparison only.
	}
	const changed = previous !== null && previous !== scope;
	if (!changed) return false;
	historyIDs = [];
	historySessions.clear();
	writeHistoryIDs();
	sessionStorage.removeItem(SESSION_STORAGE_KEY);
	renderHistory();
	return true;
}

function getDeviceKey() {
	if (ephemeralDeviceKey) return ephemeralDeviceKey;
	try {
		const stored = localStorage.getItem(DEVICE_KEY_STORAGE_KEY);
		if (isValidDeviceKey(stored)) {
			ephemeralDeviceKey = stored;
			return stored;
		}
	} catch {
		// A private browser may reject storage; the in-memory key still protects this tab.
	}
	const bytes = new Uint8Array(32);
	crypto.getRandomValues(bytes);
	ephemeralDeviceKey = base64URL(bytes);
	try {
		localStorage.setItem(DEVICE_KEY_STORAGE_KEY, ephemeralDeviceKey);
	} catch {
		ui?.liveAnnouncer && (ui.liveAnnouncer.textContent = "设备密钥仅在当前页面有效，关闭后无法恢复历史会话");
	}
	return ephemeralDeviceKey;
}

function isValidDeviceKey(value) {
	return typeof value === "string" && /^[A-Za-z0-9_-]{43}$/.test(value);
}

function base64URL(bytes) {
	let binary = "";
	bytes.forEach(value => { binary += String.fromCharCode(value); });
	return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
}

function deviceHeaders(extra = {}) {
	return { ...extra, "X-VisitReady-Device-Key": getDeviceKey() };
}

function renderRecoverableFailure(error) {
	if (!["agent_retryable_failure", "agent_recovery_exhausted", "agent_processing_failed"].includes(error.code) || error.data?.status !== "failed") return false;
  currentSession = error.data;
  persistSession(currentSession);
  renderSession(currentSession);
  return true;
}

function failureDetail(failure) {
  if (failure.retryable) return `已尝试 ${failure.attempts || 1} 次，重新尝试会继续使用当前上下文。`;
  if (failure.code === "agent_recovery_exhausted") return "已达到本次会话的自动恢复上限，可以开始新的诊前准备。";
  return "本次结果未通过完整安全校验，请开始新的诊前准备。";
}

async function retryFailedRun() {
  if (!currentSession || currentSession.status !== "failed" || !currentSession.failure?.retryable || ui.retryRun.disabled) return;
  const generation = workspaceGeneration;
  clearSectionError("session");
  ui.failed.hidden = true;
  setWorking(true, ui.retryRun);
  try {
    const session = await request(`/api/v1/sessions/${currentSession.id}/retry`, { method: "POST", streamProgress: true });
    if (generation !== workspaceGeneration) return;
	if (!ui.clarificationUserTurn.hidden) {
	  archiveClarificationExchange(ui.clarificationPanel, ui.clarificationUserTurn);
	  ui.clarificationUserTurn.hidden = true;
	}
    currentSession = session;
    persistSession(session);
    renderSession(session);
  } catch (error) {
    if (error.name !== "AbortError" && generation === workspaceGeneration && !renderRecoverableFailure(error)) {
      ui.failed.hidden = false;
      showSectionError("session", error.message);
    }
  } finally {
    if (generation === workspaceGeneration) setWorking(false, ui.retryRun);
  }
}

function persistSession(session) {
  sessionStorage.setItem("visitready.session_id", session.id);
  const nextHistoryIDs = [session.id, ...historyIDs.filter(id => id !== session.id)].slice(0, MAX_HISTORY_ITEMS);
  for (const cachedID of historySessions.keys()) {
    if (!nextHistoryIDs.includes(cachedID)) historySessions.delete(cachedID);
  }
  historyIDs = nextHistoryIDs;
  writeHistoryIDs();
  historySessions.set(session.id, historyMetadata(session));
  renderHistory();
}

async function initializeWorkspace() {
	if (accountRequired && !(await ensureAccountSession())) return;
	await hydrateServerHistory();
	await restoreSession();
	await hydrateHistory();
}

async function ensureAccountSession() {
	try {
		const response = await fetch("/api/v1/auth/me", { method: "GET", credentials: "same-origin" });
		if (!response.ok) {
			ui.accountButton.hidden = false;
			ui.accountEmail.textContent = "登录账户";
			ui.accountDialog.showModal();
			return false;
		}
		const payload = await response.json();
		switchAccountScope(payload?.data?.email);
		showAccount(payload.data);
		return true;
	} catch {
		showAccountError("暂时无法验证账户，请稍后重试。");
		ui.accountDialog.showModal();
		return false;
	}
}

async function hydrateServerHistory() {
	try {
		const response = await fetch("/api/v1/sessions", { method: "GET", headers: deviceHeaders({ "Accept": "application/json" }) });
		if (!response.ok) return;
		const payload = await response.json();
		const sessions = Array.isArray(payload?.data) ? payload.data : [];
		for (const session of sessions) {
			if (session?.id) historySessions.set(session.id, historyMetadata(session));
		}
		historyIDs = [...sessions.map(session => session.id), ...historyIDs].filter((id, index, values) => values.indexOf(id) === index).slice(0, MAX_HISTORY_ITEMS);
		writeHistoryIDs();
		renderHistory();
	} catch {
		// The local list remains available when server history is temporarily unavailable.
	}
}

function openDeviceAccessDialog() {
	ui.deviceAccessKey.value = "";
	ui.deviceKeyStatus.textContent = "";
	ui.deviceAccessDialog.showModal();
}

async function copyDeviceKey() {
	try {
		await navigator.clipboard.writeText(getDeviceKey());
		ui.deviceKeyStatus.textContent = "访问密钥已复制";
	} catch {
		ui.deviceAccessKey.value = getDeviceKey();
		ui.deviceAccessKey.select();
		ui.deviceKeyStatus.textContent = "请手动复制已选中的访问密钥";
	}
}

async function importDeviceKey() {
	const key = ui.deviceAccessKey.value.trim();
	if (!isValidDeviceKey(key)) {
		ui.deviceKeyStatus.textContent = "访问密钥格式不正确";
		return;
	}
	ephemeralDeviceKey = key;
	try {
		localStorage.setItem(DEVICE_KEY_STORAGE_KEY, key);
	} catch {
		ui.deviceKeyStatus.textContent = "浏览器未允许保存访问密钥";
		return;
	}
	historyIDs = [];
	historySessions.clear();
	writeHistoryIDs();
	sessionStorage.removeItem(SESSION_STORAGE_KEY);
	ui.deviceAccessDialog.close();
	resetWorkspace();
	await hydrateServerHistory();
	renderHistory();
	ui.liveAnnouncer.textContent = historyIDs.length ? `已恢复 ${historyIDs.length} 次往期会话` : "此访问密钥下暂无有效会话";
}

async function hydrateHistory() {
	for (const sessionID of [...historyIDs]) {
		if (historySessions.has(sessionID)) continue;
		try {
			const response = await fetch(`/api/v1/sessions/${sessionID}`, {
				method: "GET",
				headers: deviceHeaders({ "Accept": "application/json" })
			});
			if (response.status === 404 || response.status === 410) {
				removeHistoryID(sessionID);
				continue;
			}
			if (!response.ok) continue;
			const payload = await response.json();
			if (payload?.data) historySessions.set(sessionID, historyMetadata(payload.data));
		} catch {
			// History hydration is best-effort and must not block the active conversation.
		}
	}
	renderHistory();
}

function readHistoryIDs() {
  try {
    const value = JSON.parse(localStorage.getItem(HISTORY_STORAGE_KEY) || "[]");
    return Array.isArray(value) ? value.filter(id => typeof id === "string").slice(0, MAX_HISTORY_ITEMS) : [];
  } catch {
    return [];
  }
}

function writeHistoryIDs() {
  try {
    localStorage.setItem(HISTORY_STORAGE_KEY, JSON.stringify(historyIDs));
  } catch {
    ui.liveAnnouncer.textContent = "浏览器未允许保存最近会话编号";
  }
}

function renderHistory() {
  ui.historyEmpty.hidden = historyIDs.length > 0;
  ui.historyList.replaceChildren(...historyIDs.map(id => {
    const item = document.createElement("li");
    item.className = "history-item";
    const button = document.createElement("button");
    button.className = "history-button";
    button.type = "button";
    button.setAttribute("aria-current", currentSession?.id === id ? "page" : "false");
    const session = historySessions.get(id);
    const title = document.createElement("strong");
    title.textContent = session ? sessionTitle(session) : "临时诊前准备";
    const status = document.createElement("small");
    status.textContent = session ? sessionStatusLabels[session.status] || "已记录" : "点击恢复";
    button.append(title, status);
    button.addEventListener("click", () => loadHistoricalSession(id));
    const remove = document.createElement("button");
    remove.className = "history-remove-button";
    remove.type = "button";
    remove.textContent = "×";
	remove.setAttribute("aria-label", `仅从本浏览器隐藏 ${title.textContent}`);
	remove.title = "仅从本浏览器隐藏，不删除服务器数据";
    remove.addEventListener("click", event => {
      event.stopPropagation();
      removeHistoryID(id, { undoable: true });
    });
    item.append(button, remove);
    return item;
  }));
}

async function loadHistoricalSession(sessionID) {
  const generation = ++workspaceGeneration;
  clearActiveWork({ abort: true });
  clearThreadState();
  currentSession = null;
  currentRawInput = "";
  verifiedFactIDs.clear();
  ui.clarificationInput.value = "";
  renderComposerMode("hidden");
  ui.threadViewport.setAttribute("aria-busy", "true");
  ui.threadTitle.textContent = "正在恢复会话";
  try {
    const session = await request(`/api/v1/sessions/${sessionID}`, { method: "GET" });
    if (generation !== workspaceGeneration) return;
    currentSession = session;
    ui.initialUserTurn.hidden = false;
    ui.initialUserMessage.textContent = "初始描述已提交。服务器不返回完整输入，只显示核对所需的证据片段。";
    persistSession(session);
    renderSession(session, { restored: true });
    ui.recoveryNotice.hidden = false;
  } catch (error) {
    if (generation !== workspaceGeneration) return;
    const expired = error.status === 404 || error.status === 410 || error.code === "session_not_found" || error.code === "session_expired";
    if (expired) removeHistoryID(sessionID);
    showSectionError("recovery", expired ? "该临时会话已过期，已从往期列表移除。" : "暂时无法恢复该会话，请稍后重试。");
    renderComposerMode("initial");
  } finally {
    if (generation === workspaceGeneration) ui.threadViewport.setAttribute("aria-busy", "false");
  }
}

async function restoreSession() {
  const sessionID = sessionStorage.getItem("visitready.session_id");
  if (!sessionID) return;
  const generation = workspaceGeneration;
  try {
    const session = await request(`/api/v1/sessions/${sessionID}`, { method: "GET" });
    if (generation !== workspaceGeneration) return;
    currentSession = session;
    ui.initialUserTurn.hidden = false;
    ui.initialUserMessage.textContent = "初始描述已提交。服务器不返回完整输入，只显示核对所需的证据片段。";
    persistSession(session);
    renderSession(session, { restored: true });
    ui.recoveryNotice.hidden = false;
  } catch (error) {
    if (generation !== workspaceGeneration) return;
    const expired = error.status === 404 || error.status === 410 || error.code === "session_not_found" || error.code === "session_expired";
    if (expired) {
      sessionStorage.removeItem("visitready.session_id");
      removeHistoryID(sessionID);
    }
    showSectionError("recovery", expired ? "上次会话已过期，请开始新的诊前准备。" : "暂时无法恢复上次会话，稍后可从往期列表重试。");
  }
}

function scheduleSessionExpiry(session) {
  if (expiryTimer) window.clearTimeout(expiryTimer);
  if (!session.expires_at) return true;
  const expiresAt = Date.parse(session.expires_at);
  if (!Number.isFinite(expiresAt)) return true;
  const remaining = expiresAt - Date.now();
  if (remaining <= 0) {
    renderExpiredState(session.id);
    return false;
  }
  ui.expiry.hidden = false;
  ui.expiry.textContent = formatRemainingTime(remaining);
  expiryTimer = window.setTimeout(() => {
    if (currentSession?.id === session.id) scheduleSessionExpiry(session);
  }, Math.min(remaining, 60000));
  return true;
}

function formatRemainingTime(remainingMs) {
  const minutes = Math.max(1, Math.ceil(remainingMs / 60000));
  if (minutes < 60) return `约 ${minutes} 分钟后清除`;
  const hours = Math.ceil(minutes / 60);
  if (hours < 24) return `约 ${hours} 小时后清除`;
  return `约 ${Math.ceil(hours / 24)} 天后清除`;
}

function renderExpiredState(sessionID) {
  if (currentSession?.id !== sessionID) return;
  workspaceGeneration += 1;
  clearActiveWork({ abort: true });
  sessionStorage.removeItem("visitready.session_id");
  removeHistoryID(sessionID);
  currentSession = null;
  verifiedFactIDs.clear();
  clearThreadState();
  ui.expired.hidden = false;
  ui.expiry.hidden = true;
  ui.threadTitle.textContent = "会话已清除";
  renderComposerMode("hidden");
  ui.composerState.textContent = "临时上下文已清除";
  ui.expired.focus();
}

function removeHistoryID(sessionID, { undoable = false } = {}) {
  const index = historyIDs.indexOf(sessionID);
  const metadata = historySessions.get(sessionID);
  historyIDs = historyIDs.filter(id => id !== sessionID);
  historySessions.delete(sessionID);
  writeHistoryIDs();
  renderHistory();
	if (undoable && index >= 0) showHistoryUndo({ id: sessionID, index, metadata });
}

function showHistoryUndo(removal) {
  pendingHistoryRemoval = removal;
  if (historyUndoTimer) window.clearTimeout(historyUndoTimer);
	ui.historyToast.hidden = false;
	ui.historyToastMessage.textContent = "已仅从本浏览器隐藏，服务器内容仍保留至过期";
  ui.undoHistory.focus();
  historyUndoTimer = window.setTimeout(hideHistoryUndo, 8000);
}

async function permanentlyDeleteCurrentSession() {
	if (!currentSession) return;
	const deletedID = currentSession.id;
	ui.confirmDeleteSession.disabled = true;
	try {
		await request(`/api/v1/sessions/${deletedID}`, { method: "DELETE" });
		ui.deleteSessionDialog.close();
		removeHistoryID(deletedID);
		sessionStorage.removeItem(SESSION_STORAGE_KEY);
		resetWorkspace();
		ui.historyToastMessage.textContent = "这次会话已永久删除";
		ui.historyToast.hidden = false;
		ui.liveAnnouncer.textContent = "这次会话已永久删除";
	} catch (error) {
		ui.deleteSessionDialog.close();
		showSectionError("session", error.message || "暂时无法删除这次会话，请稍后重试。");
	} finally {
		ui.confirmDeleteSession.disabled = false;
	}
}

function hideHistoryUndo() {
  if (historyUndoTimer) window.clearTimeout(historyUndoTimer);
  historyUndoTimer = null;
  pendingHistoryRemoval = null;
  ui.historyToast.hidden = true;
  if (document.activeElement === ui.undoHistory) {
    (ui.historyList.querySelector(".history-button") || document.querySelector("#new-conversation-button")).focus();
  }
}

function undoHistoryRemoval() {
  if (!pendingHistoryRemoval) return;
  const { id, index, metadata } = pendingHistoryRemoval;
  historyIDs = historyIDs.filter(historyID => historyID !== id);
  historyIDs.splice(Math.min(index, historyIDs.length), 0, id);
  historyIDs = historyIDs.slice(0, MAX_HISTORY_ITEMS);
  if (metadata) historySessions.set(id, metadata);
  writeHistoryIDs();
  renderHistory();
  const restoredButton = ui.historyList.querySelectorAll(".history-button")[Math.min(index, historyIDs.length - 1)];
  hideHistoryUndo();
  restoredButton?.focus();
  ui.liveAnnouncer.textContent = "已恢复最近会话入口";
}

function syncHistoryFromStorage(event) {
  if (event.key !== HISTORY_STORAGE_KEY) return;
  historyIDs = readHistoryIDs();
  renderHistory();
}

function resetWorkspace() {
  workspaceGeneration += 1;
  clearActiveWork({ abort: true });
  if (expiryTimer) window.clearTimeout(expiryTimer);
  expiryTimer = null;
  currentSession = null;
  currentRawInput = "";
  verifiedFactIDs.clear();
  sessionStorage.removeItem("visitready.session_id");
  clearThreadState();
  ui.input.value = "";
  ui.clarificationInput.value = "";
  ui.search.checked = false;
  ui.visitGoal.disabled = false;
  ui.insights.checked = false;
  ui.insights.disabled = false;
  ui.threadTitle.textContent = "新的诊前准备";
	ui.deleteSession.hidden = true;
  ui.empty.hidden = false;
  ui.expiry.hidden = true;
  renderComposerMode("initial");
  clearAllErrors();
  updateCount();
  renderHistory();
  ui.input.focus();
}

function clearThreadState() {
  document.querySelectorAll(".archived-clarification").forEach(turn => turn.remove());
  ui.empty.hidden = true;
  ui.working.hidden = true;
  ui.session.hidden = true;
  ui.emergency.hidden = true;
  ui.failed.hidden = true;
  ui.expired.hidden = true;
  ui.initialUserTurn.hidden = true;
  ui.clarificationUserTurn.hidden = true;
  ui.recoveryNotice.hidden = true;
}

function sessionTitle(session) {
  const time = session.created_at ? formatTime(session.created_at) : "刚刚";
  return `诊前准备 · ${time}`;
}

function historyMetadata(session) {
  return { id: session.id, status: session.status, created_at: session.created_at, expires_at: session.expires_at };
}

function formatTime(value) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "" : date.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" });
}
