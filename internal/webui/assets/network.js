async function request(url, options) {
  const controller = new AbortController();
  let timedOut = false;
  const timeout = window.setTimeout(() => { timedOut = true; controller.abort(); }, REQUEST_TIMEOUT_MS);
  activeController = controller;
  const headers = { "Content-Type": "application/json" };
  if (options.streamProgress) headers.Accept = "text/event-stream";
  const init = { method: options.method, headers: deviceHeaders(headers), signal: controller.signal };
  if (options.body) init.body = JSON.stringify(options.body);
  try {
    const response = await fetch(url, init);
    if (typeof options.onHeaders === "function") options.onHeaders(response);
    if (response.headers.get("Content-Type")?.startsWith("text/event-stream")) {
      return await readProgressStream(response);
    }
    const payload = await response.json().catch(() => null);
    if (!response.ok) {
      const error = new Error(payload?.error?.message || `请求失败（HTTP ${response.status}）`);
      error.status = response.status;
      error.code = payload?.error?.code || "request_failed";
      error.data = payload?.data || null;
      throw error;
    }
    return payload?.data ?? null;
  } catch (error) {
    if (timedOut) throw new Error("请求等待时间过长，请重试或开始新会话。");
    throw error;
  } finally {
    window.clearTimeout(timeout);
    if (activeController === controller) activeController = null;
  }
}

async function readProgressStream(response) {
  if (!response.body) throw new Error("浏览器未提供流式响应内容。");
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let finalResult = null;
  while (true) {
    const { value, done } = await reader.read();
    buffer += decoder.decode(value || new Uint8Array(), { stream: !done }).replaceAll("\r\n", "\n");
    let boundary;
    while ((boundary = buffer.indexOf("\n\n")) >= 0) {
      const block = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      const event = parseSSEBlock(block);
      if (event?.name === "progress") updateStreamProgress(event.data);
      if (event?.name === "result") finalResult = event.data;
    }
    if (done) break;
  }
  if (!finalResult) throw new Error("流式处理意外结束，请从往期会话检查结果或重试。");
  const payload = finalResult.payload;
  if (finalResult.status < 200 || finalResult.status >= 300) {
    const error = new Error(payload?.error?.message || `请求失败（HTTP ${finalResult.status}）`);
    error.status = finalResult.status;
    error.code = payload?.error?.code || "request_failed";
    error.data = payload?.data || null;
    throw error;
  }
  return payload?.data ?? null;
}

function parseSSEBlock(block) {
  let name = "message";
  const data = [];
  for (const line of block.split("\n")) {
    if (line.startsWith("event:")) name = line.slice(6).trim();
    if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
  }
  if (!data.length) return null;
  try { return { name, data: JSON.parse(data.join("\n")) }; }
  catch { return null; }
}

function updateStreamProgress(progress) {
  const label = nodeLabels[progress?.node] || "处理信息";
  if (progress?.status === "running") {
    ui.workingStatus.textContent = `正在${label}`;
  } else if (progress?.status === "completed") {
    ui.workingStatus.textContent = `${label}完成${Number.isFinite(progress.duration_ms) ? ` · ${progress.duration_ms} ms` : ""}`;
  } else if (progress?.status === "failed") {
    ui.workingStatus.textContent = `${label}未完成，正在安全收尾`;
  }
}

async function downloadSessionExport(event, filename) {
  event.preventDefault();
  if (!currentSession) return;
  const link = event.currentTarget;
  const previousBusy = link.getAttribute("aria-busy");
  link.setAttribute("aria-busy", "true");
  try {
    const response = await fetch(`/api/v1/sessions/${currentSession.id}/export`, {
      method: "GET",
      headers: deviceHeaders({ "Accept": "text/markdown" })
    });
    if (!response.ok) {
      const payload = await response.json().catch(() => null);
      throw new Error(payload?.error?.message || `下载失败（HTTP ${response.status}）`);
    }
    const objectURL = URL.createObjectURL(await response.blob());
    const download = document.createElement("a");
    download.href = objectURL;
    download.download = filename;
    download.click();
    window.setTimeout(() => URL.revokeObjectURL(objectURL), 0);
  } catch (error) {
    showSectionError("session", error.message || "暂时无法下载，请稍后重试。");
  } finally {
    if (previousBusy === null) link.removeAttribute("aria-busy");
    else link.setAttribute("aria-busy", previousBusy);
  }
}
