const $ = (s) => document.querySelector(s);

async function api(path, opts) {
  const res = await fetch(path, opts);
  if (!res.ok) throw new Error("HTTP " + res.status);
  return res.json();
}

const typeLabel = { research: "研究", report: "报告", devops: "运维", maintenance: "维护", custom: "自定义" };
const statusLabel = { running: "运行中", completed: "已完成", queued: "排队中", stopped: "已停止", failed: "失败" };

function fmtUptime(sec) {
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = sec % 60;
  if (h > 0) return `运行中 ${h}h ${m}m`;
  if (m > 0) return `运行中 ${m}m ${s}s`;
  return `运行中 ${s}s`;
}

function fmtTime(t) {
  return new Date(t).toLocaleTimeString("zh-CN", { hour12: false });
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

// ============ Tab 切换 ============
document.querySelectorAll(".tab").forEach((btn) => {
  btn.onclick = () => {
    document.querySelectorAll(".tab").forEach((b) => b.classList.remove("active"));
    document.querySelectorAll(".view").forEach((v) => v.classList.add("hidden"));
    btn.classList.add("active");
    const tab = btn.dataset.tab;
    $("#view-" + tab).classList.remove("hidden");
    if (tab === "ops") { loadOpsStatus(); runCheck(); loadOpsLog(); }
    if (tab === "settings") loadSettings();
  };
});

// ============ 仪表盘刷新 ============
async function refresh() {
  try {
    const [ov, tasks, skills, memory, logs, metrics] = await Promise.all([
      api("/api/overview"), api("/api/tasks"), api("/api/skills"),
      api("/api/memory"), api("/api/logs"), api("/api/metrics"),
    ]);
    renderOverview(ov);
    renderTasks(tasks);
    renderSkills(skills);
    renderMemory(memory);
    renderLogs(logs);
    renderChart(metrics);
    if (!$("#view-ops").classList.contains("hidden")) loadOpsLog();
  } catch (e) {
    console.error("刷新失败", e);
  }
}

function renderOverview(ov) {
  const a = ov.agent;
  const m = ov.latestMetric || {};
  const badge = $("#agentStatus");
  badge.textContent = "● " + (a.status || "online").toUpperCase();
  badge.className = "badge " + (a.status === "online" ? "online" : "offline");
  $("#uptime").textContent = fmtUptime(ov.uptimeSeconds);
  $("#overviewCards").innerHTML = `
    <div class="card accent">
      <div class="label">活动任务</div>
      <div class="value">${ov.tasks.active}</div>
      <div class="sub">队列共 ${ov.tasks.total} 个</div>
    </div>
    <div class="card">
      <div class="label">已完成</div>
      <div class="value">${ov.tasks.completed}</div>
      <div class="sub">排队中 ${ov.tasks.queued}</div>
    </div>
    <div class="card">
      <div class="label">已启用技能</div>
      <div class="value">${ov.skills.enabled}<span style="font-size:16px;color:var(--muted)">/${ov.skills.total}</span></div>
      <div class="sub">${a.model}</div>
    </div>
    <div class="card">
      <div class="label">长期记忆</div>
      <div class="value">${ov.memory}</div>
      <div class="sub">条目</div>
    </div>
    <div class="card">
      <div class="label">CPU</div>
      <div class="value">${(m.cpu || 0).toFixed(0)}<span style="font-size:16px">%</span></div>
      <div class="sub">内存 ${(m.mem || 0).toFixed(0)} MB</div>
    </div>
    <div class="card">
      <div class="label">任务吞吐</div>
      <div class="value">${(m.tasks || 0).toFixed(0)}</div>
      <div class="sub">个 / 分钟</div>
    </div>`;
}

function renderTasks(tasks) {
  const sorted = [...tasks].sort((a, b) => b.UpdatedAt.localeCompare(a.UpdatedAt));
  $("#taskList").innerHTML = sorted.map((t) => `
    <div class="task">
      <div class="task-top">
        <span class="task-title">${escapeHtml(t.Title)}</span>
        <span class="tag ${t.Status}">${statusLabel[t.Status] || t.Status}</span>
      </div>
      <div class="task-meta">类型 ${typeLabel[t.Type] || t.Type} · 执行者 ${t.Agent} · 更新 ${fmtTime(t.UpdatedAt)}</div>
      <div class="bar"><i style="width:${t.Progress}%"></i></div>
      <div class="task-foot">
        <span class="tag">${t.Progress}%</span>
        ${t.Status === "running"
          ? `<button class="btn" data-stop="${t.ID}">停止</button>`
          : `<span class="tag">${t.ID}</span>`}
      </div>
    </div>`).join("");
  document.querySelectorAll("[data-stop]").forEach((b) => {
    b.onclick = async () => {
      await api(`/api/tasks/${b.dataset.stop}/stop`, { method: "POST" });
      refresh();
    };
  });
}

function renderSkills(skills) {
  $("#skillList").innerHTML = skills.map((s) => `
    <div class="row">
      <div>
        <div style="font-weight:600;font-size:13px">${s.Name}</div>
        <div class="meta">${s.Description} · 调用 ${s.Calls} 次</div>
      </div>
      <label class="switch">
        <input type="checkbox" ${s.Enabled ? "checked" : ""} data-skill="${s.Name}">
        <span class="slider"></span>
      </label>
    </div>`).join("");
  document.querySelectorAll("[data-skill]").forEach((el) => {
    el.onchange = async () => {
      await api(`/api/skills/${el.dataset.skill}/toggle`, { method: "POST" });
      refresh();
    };
  });
}

function renderMemory(mem) {
  $("#memoryList").innerHTML = mem.map((m) => `
    <div class="row">
      <div>
        <div class="mem-key">${m.Key}</div>
        <div class="mem-val">${escapeHtml(m.Value)}</div>
      </div>
      <span class="chip">${m.Category}</span>
    </div>`).join("");
}

function renderLogs(logs) {
  $("#logCount").textContent = logs.length + " 条";
  $("#logList").innerHTML = [...logs].reverse().slice(0, 80).map((l) => `
    <div class="log">
      <span class="t">${fmtTime(l.Time)}</span>
      <span class="lvl ${l.Level}">${l.Level.toUpperCase()}</span>
      <span class="src">[${l.Source}]</span>
      <span class="msg">${escapeHtml(l.Message)}</span>
    </div>`).join("");
}

function renderChart(metrics) {
  const canvas = $("#metricChart");
  const dpr = window.devicePixelRatio || 1;
  const w = canvas.clientWidth, h = 180;
  canvas.width = w * dpr; canvas.height = h * dpr;
  const ctx = canvas.getContext("2d");
  ctx.scale(dpr, dpr);
  ctx.clearRect(0, 0, w, h);
  if (!metrics.length) return;
  const pad = { l: 36, r: 10, t: 10, b: 18 };
  const iw = w - pad.l - pad.r, ih = h - pad.t - pad.b;
  const n = metrics.length;
  const tVals = metrics.map((m) => m.Tasks);
  const cVals = metrics.map((m) => m.CPU);
  const mVals = metrics.map((m) => m.Mem);
  const maxT = Math.max(...tVals, 1);
  const maxC = 100;
  const maxM = Math.max(...mVals, 1) * 1.1;
  ctx.strokeStyle = "rgba(255,255,255,.06)";
  ctx.lineWidth = 1;
  for (let i = 0; i <= 4; i++) {
    const y = pad.t + (ih / 4) * i;
    ctx.beginPath(); ctx.moveTo(pad.l, y); ctx.lineTo(pad.l + iw, y); ctx.stroke();
  }
  const drawLine = (vals, max, color) => {
    ctx.strokeStyle = color; ctx.lineWidth = 2; ctx.beginPath();
    vals.forEach((v, i) => {
      const x = pad.l + (iw / (n - 1)) * i;
      const y = pad.t + ih - (Math.min(v, max) / max) * ih;
      i === 0 ? ctx.moveTo(x, y) : ctx.lineTo(x, y);
    });
    ctx.stroke();
    ctx.lineTo(pad.l + iw, pad.t + ih);
    ctx.lineTo(pad.l, pad.t + ih);
    ctx.closePath();
    ctx.fillStyle = color.replace("rgb", "rgba").replace(")", ",0.08)");
    ctx.fill();
  };
  drawLine(tVals, maxT, "rgb(124,92,255)");
  drawLine(cVals, maxC, "rgb(45,212,191)");
  drawLine(mVals, maxM, "rgb(210,153,34)");
}

// ============ 系统运维 ============
async function loadOpsStatus() {
  try {
    const s = await api("/api/ops/status");
    const running = s.coreStatus === "running";
    $("#opsCards").innerHTML = `
      <div class="card ${running ? "accent" : ""}">
        <div class="label">核心状态</div>
        <div class="value" style="font-size:24px;color:${running ? "var(--green)" : "var(--red)"}">${running ? "运行中" : "已停止"}</div>
        <div class="sub">v${s.version} · PID ${s.pid}</div>
      </div>
      <div class="card">
        <div class="label">运行端口</div>
        <div class="value" style="font-size:24px">${s.port}</div>
        <div class="sub">当前模型 ${s.model || "—"}</div>
      </div>
      <div class="card">
        <div class="label">运行时长</div>
        <div class="value" style="font-size:24px">${running ? fmtUptime(s.uptimeSeconds) : "—"}</div>
        <div class="sub">启动于 ${running ? fmtTime(s.startedAt) : "—"}</div>
      </div>
      <div class="card">
        <div class="label">任务</div>
        <div class="value" style="font-size:24px">${s.runningTasks}<span style="font-size:14px;color:var(--muted)"> 运行</span> / ${s.queuedTasks}<span style="font-size:14px;color:var(--muted)"> 排队</span></div>
        <div class="sub">调度${running ? "正常" : "已暂停"}</div>
      </div>`;
    $("#coreStatusBox").innerHTML = running
      ? `<div class="core-on">● 核心服务运行中</div>`
      : `<div class="core-off">● 核心服务已停止</div><p class="hint">任务调度已暂停，启动后恢复。</p>`;
    $("#startBtn").disabled = running;
    $("#stopBtn").disabled = !running;
    $("#restartBtn").disabled = !running;
  } catch (e) { console.error(e); }
}

async function loadOpsLog() {
  try {
    const logs = await api("/api/logs");
    const focus = ["core", "ops", "settings"];
    const items = [...logs].reverse().filter((l) => focus.includes(l.Source)).slice(0, 60);
    $("#opsLog").innerHTML = items.length
      ? items.map((l) => `
        <div class="log">
          <span class="t">${fmtTime(l.Time)}</span>
          <span class="lvl ${l.Level}">${l.Level.toUpperCase()}</span>
          <span class="src">[${l.Source}]</span>
          <span class="msg">${escapeHtml(l.Message)}</span>
        </div>`).join("")
      : `<p class="hint">暂无运维日志。</p>`;
  } catch (e) { console.error(e); }
}

async function runCheck() {
  $("#checkList").innerHTML = `<p class="hint">正在检测…</p>`;
  try {
    const results = await api("/api/ops/check");
    const fails = results.filter((r) => !r.ok).length;
    $("#checkList").innerHTML = `
      <p class="hint" style="margin-top:0">共 ${results.length} 项，异常 <b style="color:${fails ? "var(--red)" : "var(--green)"}">${fails}</b> 项</p>
      ${results.map((r) => `
        <div class="row">
          <div>
            <div style="font-weight:600;font-size:13px">${r.name}</div>
            <div class="meta">${escapeHtml(r.detail)}</div>
          </div>
          <span class="tag ${r.ok ? "completed" : "stopped"}">${r.ok ? "正常" : "异常"}</span>
        </div>`).join("")}`;
  } catch (e) {
    $("#checkList").innerHTML = `<p class="hint">检测失败：${e.message}</p>`;
  }
}

$("#startBtn").onclick = async () => { await api("/api/ops/start", { method: "POST" }); loadOpsStatus(); loadOpsLog(); };
$("#stopBtn").onclick = async () => { await api("/api/ops/stop", { method: "POST" }); loadOpsStatus(); loadOpsLog(); };
$("#restartBtn").onclick = async () => { await api("/api/ops/restart", { method: "POST" }); loadOpsStatus(); loadOpsLog(); };
$("#checkBtn").onclick = runCheck;

// ============ 设置 ============
async function loadSettings() {
  try {
    const s = await api("/api/settings");
    $("#modelProvider").value = s.model.provider || "openai";
    $("#modelBaseURL").value = s.model.baseURL || "";
    $("#modelName").value = s.model.model || "";
    $("#keyHint").textContent = s.model.keySet ? `当前已配置（${s.model.keyMask}）` : "尚未配置";
    $("#wxEnabled").checked = !!s.wechat.enabled;
    $("#wxAppId").value = s.wechat.appId || "";
    $("#wxWebhook").value = s.wechat.webhookUrl || "";
    $("#wxToken").placeholder = s.wechat.tokenSet ? "已配置（留空不修改）" : "公众号 Token";
  } catch (e) { console.error(e); }
}

$("#saveModelBtn").onclick = async () => {
  const body = {
    model: {
      provider: $("#modelProvider").value,
      apiKey: $("#modelKey").value,
      baseURL: $("#modelBaseURL").value,
      model: $("#modelName").value,
    },
  };
  await api("/api/settings", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  $("#modelKey").value = "";
  loadSettings();
  alert("模型设置已保存");
};

$("#saveWxBtn").onclick = async () => {
  const body = {
    wechat: {
      enabled: $("#wxEnabled").checked,
      token: $("#wxToken").value,
      appId: $("#wxAppId").value,
      appSecret: $("#wxAppSecret").value,
      webhookUrl: $("#wxWebhook").value,
    },
  };
  await api("/api/settings", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  $("#wxToken").value = "";
  $("#wxAppSecret").value = "";
  loadSettings();
  alert("微信接入设置已保存");
};

// ============ 新建任务弹窗 ============
$("#newTaskBtn").onclick = () => $("#modal").classList.remove("hidden");
$("#cancelBtn").onclick = () => $("#modal").classList.add("hidden");
$("#submitBtn").onclick = async () => {
  const title = $("#taskTitle").value.trim();
  const type = $("#taskType").value;
  if (!title) { $("#taskTitle").focus(); return; }
  await api("/api/tasks", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ title, type }) });
  $("#taskTitle").value = "";
  $("#modal").classList.add("hidden");
  refresh();
};

$("#refreshBtn").onclick = refresh;

setInterval(() => {
  $("#clock").textContent = new Date().toLocaleString("zh-CN", { hour12: false });
}, 1000);

refresh();
setInterval(refresh, 2500);
