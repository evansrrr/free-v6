/* ── State ─────────────────────────────────────────────────────── */

const state = {
  activeView: 'dashboard',
  helperOnline: false,
  proxyRunning: false,
  mode: 'rule',
  cidrs: [],

  // Traffic
  trafficHistory: [],    // [{t, up, down}]  last 60 samples
  totalUpload: 0,
  totalDownload: 0,
  lastUpload: 0,
  lastDownload: 0,

  // Network
  egressIP: '--',
  localIPv4: '--',
  localIPv6: '--',

  // Logs
  logs: [],
};

const API_BASE = 'http://127.0.0.1:13335/api/v1';
const MAX_LOG = 100;
const TRAFFIC_POINTS = 60;

const $ = (s) => document.querySelector(s);
const $$ = (s) => [...document.querySelectorAll(s)];

/* ── Utility ──────────────────────────────────────────────────── */

function formatBytes(bytes) {
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1048576) return (bytes / 1024).toFixed(1) + ' KB';
  if (bytes < 1073741824) return (bytes / 1048576).toFixed(1) + ' MB';
  return (bytes / 1073741824).toFixed(2) + ' GB';
}

function formatSpeed(bytesPerSec) {
  if (bytesPerSec < 1024) return bytesPerSec + ' B/s';
  if (bytesPerSec < 1048576) return (bytesPerSec / 1024).toFixed(1) + ' KB/s';
  if (bytesPerSec < 1073741824) return (bytesPerSec / 1048576).toFixed(1) + ' MB/s';
  return (bytesPerSec / 1073741824).toFixed(2) + ' GB/s';
}

/* ── API ──────────────────────────────────────────────────────── */

async function api(path, options = {}) {
  const response = await fetch(`${API_BASE}${path}`, {
    ...options,
    headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
  });
  const payload = await response.json();
  if (!response.ok) throw new Error(payload.error || `HTTP ${response.status}`);
  return payload;
}

/* ── Logging ──────────────────────────────────────────────────── */

function addLog(message, isError = false) {
  const now = new Date().toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  state.logs.unshift({ time: now, msg: message, error: isError });
  if (state.logs.length > MAX_LOG) state.logs.length = MAX_LOG;
  renderLogs();
}

function renderLogs() {
  const el = $('#logList');
  if (!el) return;
  el.innerHTML = state.logs.map(l =>
    `<div class="log-entry${l.error ? ' error' : ''}"><span class="log-time">${l.time}</span><span class="log-msg">${l.msg}</span></div>`
  ).join('');
}

/* ── Navigation ───────────────────────────────────────────────── */

const VIEW_TITLES = { dashboard: '仪表盘', proxies: '代理', settings: '设置' };

function showView(view) {
  if (view === state.activeView) return;
  const oldView = $(`#${state.activeView}View`);
  const newView = $(`#${view}View`);
  if (!newView) return;

  // Exit animation on old view
  if (oldView) {
    oldView.classList.add('exiting');
    oldView.addEventListener('animationend', () => {
      oldView.classList.remove('active', 'exiting');
    }, { once: true });
  }

  // Enter new view after short delay
  setTimeout(() => {
    newView.classList.add('active');
    state.activeView = view;
    $('#pageTitle').textContent = VIEW_TITLES[view] || view;
  }, oldView ? 80 : 0);

  // Update nav highlighting immediately
  $$('.nav-item[data-view]').forEach(item => {
    item.classList.toggle('active', item.dataset.view === view);
  });
}

/* ── Proxy Toggle ─────────────────────────────────────────────── */

function setRunning(running) {
  state.proxyRunning = running;
  const card = $('#proxyToggle');
  const icon = $('#toggleIcon');
  const label = $('#toggleLabel');
  const status = $('#toggleStatus');
  const chip = $('#connectionChip');
  const dot = $('#headerDot');
  const headerStatus = $('#headerStatus');

  card.classList.toggle('running', running);
  icon.textContent = running ? '■' : '▶';
  label.textContent = running ? '停止免流' : '启动免流';
  status.textContent = running ? 'mihomo 运行中' : '点击启动 mihomo';
  chip.classList.toggle('connected', running);
  dot.classList.toggle('live', running);
  headerStatus.textContent = running ? '已连接' : '未连接';
}

/* ── Mode Selector ────────────────────────────────────────────── */

function setMode(mode, persist = true) {
  state.mode = mode;
  $$('.mode-option').forEach(el => {
    el.classList.toggle('selected', el.dataset.mode === mode);
  });
  addLog(`切换为${mode === 'rule' ? '规则' : '全局'}模式`);
  if (persist) persistSettings();
}

function persistSettings() {
  if (!state.helperOnline) return;
  api('/settings', { method: 'PUT', body: JSON.stringify({ mode: state.mode, campusCidrs: state.cidrs }) })
    .then(() => addLog('设置已保存'))
    .catch(e => addLog(`保存设置失败: ${e.message}`, true));
}

/* ── Traffic Chart (Canvas) ───────────────────────────────────── */

let chartCtx = null;
let chartW = 0, chartH = 0;

function initChart() {
  const canvas = $('#trafficCanvas');
  if (!canvas) return;
  chartCtx = canvas.getContext('2d');
  resizeChart();
  window.addEventListener('resize', resizeChart);
}

function resizeChart() {
  const canvas = $('#trafficCanvas');
  if (!canvas) return;
  const rect = canvas.parentElement.getBoundingClientRect();
  const dpr = window.devicePixelRatio || 1;
  chartW = rect.width;
  chartH = rect.height;
  canvas.width = chartW * dpr;
  canvas.height = chartH * dpr;
  canvas.style.width = chartW + 'px';
  canvas.style.height = chartH + 'px';
  chartCtx.setTransform(dpr, 0, 0, dpr, 0, 0);
  drawChart();
}

function drawChart() {
  if (!chartCtx) return;
  const ctx = chartCtx;
  const data = state.trafficHistory;
  const len = data.length;

  ctx.clearRect(0, 0, chartW, chartH);

  if (len < 2) {
    // Empty state
    ctx.fillStyle = 'rgba(160,196,255,.08)';
    ctx.fillRect(0, 0, chartW, chartH);
    ctx.fillStyle = getComputedStyle(document.documentElement).getPropertyValue('--md-outline').trim() || '#8e9099';
    ctx.font = '13px DM Sans, sans-serif';
    ctx.textAlign = 'center';
    ctx.fillText('等待流量数据', chartW / 2, chartH / 2 + 4);
    return;
  }

  // Find max for scaling
  let maxVal = 1024; // minimum 1KB/s scale
  for (const d of data) {
    if (d.up > maxVal) maxVal = d.up;
    if (d.down > maxVal) maxVal = d.down;
  }
  maxVal *= 1.2; // headroom

  const stepX = chartW / (TRAFFIC_POINTS - 1);
  const getStyle = (name) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();

  // Draw filled area + line for each series
  function drawLine(values, color, alpha) {
    const len = values.length;
    if (len < 2) return;

    ctx.beginPath();
    for (let i = 0; i < len; i++) {
      const x = (TRAFFIC_POINTS - len + i) * stepX;
      const y = chartH - (values[i] / maxVal) * (chartH - 8);
      if (i === 0) ctx.moveTo(x, y);
      else ctx.lineTo(x, y);
    }
    ctx.strokeStyle = color;
    ctx.lineWidth = 2;
    ctx.lineJoin = 'round';
    ctx.stroke();

    // Fill area
    const lastX = (TRAFFIC_POINTS - 1) * stepX;
    const firstX = (TRAFFIC_POINTS - len) * stepX;
    ctx.lineTo(lastX, chartH);
    ctx.lineTo(firstX, chartH);
    ctx.closePath();
    ctx.fillStyle = color.replace('1)', alpha + ')').replace('rgb', 'rgba');
    ctx.fill();
  }

  const primary = getStyle('--md-primary') || '#a0c4ff';
  const secondary = getStyle('--md-secondary') || '#bac8db';

  // Convert to RGB for alpha
  function toRGBA(hex, a) {
    hex = hex.replace('#', '');
    if (hex.length === 3) hex = hex[0]+hex[0]+hex[1]+hex[1]+hex[2]+hex[2];
    const r = parseInt(hex.slice(0,2),16), g = parseInt(hex.slice(2,4),16), b = parseInt(hex.slice(4,6),16);
    return `rgba(${r},${g},${b},${a})`;
  }

  const upVals = data.map(d => d.up);
  const downVals = data.map(d => d.down);

  // Draw upload line
  ctx.beginPath();
  for (let i = 0; i < upVals.length; i++) {
    const x = (TRAFFIC_POINTS - upVals.length + i) * stepX;
    const y = chartH - (upVals[i] / maxVal) * (chartH - 8) - 2;
    if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
  }
  ctx.strokeStyle = primary;
  ctx.lineWidth = 2;
  ctx.lineJoin = 'round';
  ctx.stroke();
  // Fill
  ctx.lineTo((TRAFFIC_POINTS - 1) * stepX, chartH);
  ctx.lineTo((TRAFFIC_POINTS - upVals.length) * stepX, chartH);
  ctx.closePath();
  ctx.fillStyle = toRGBA(primary, 0.08);
  ctx.fill();

  // Draw download line
  ctx.beginPath();
  for (let i = 0; i < downVals.length; i++) {
    const x = (TRAFFIC_POINTS - downVals.length + i) * stepX;
    const y = chartH - (downVals[i] / maxVal) * (chartH - 8) - 2;
    if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
  }
  ctx.strokeStyle = secondary;
  ctx.lineWidth = 2;
  ctx.lineJoin = 'round';
  ctx.stroke();
  // Fill
  ctx.lineTo((TRAFFIC_POINTS - 1) * stepX, chartH);
  ctx.lineTo((TRAFFIC_POINTS - downVals.length) * stepX, chartH);
  ctx.closePath();
  ctx.fillStyle = toRGBA(secondary, 0.06);
  ctx.fill();
}

/* ── Stats Ring Chart ─────────────────────────────────────────── */

function updateStatsRing() {
  const total = state.totalUpload + state.totalDownload;
  const uploadPct = total > 0 ? state.totalUpload / total : 0;
  const downloadPct = total > 0 ? state.totalDownload / total : 0;

  const uploadRing = $('#ringUpload');
  const downloadRing = $('#ringDownload');

  // outer circle circumference = 2 * PI * 15.5 ≈ 97.4
  // inner circle circumference = 2 * PI * 12 ≈ 75.4
  if (uploadRing) {
    uploadRing.setAttribute('stroke-dashoffset', String(97.4 * (1 - uploadPct)));
  }
  if (downloadRing) {
    downloadRing.setAttribute('stroke-dashoffset', String(75.4 * (1 - downloadPct)));
  }

  $('#totalUp').textContent = formatBytes(state.totalUpload);
  $('#totalDown').textContent = formatBytes(state.totalDownload);
}

/* ── Backend State Refresh ────────────────────────────────────── */

async function refreshBackendState() {
  try {
    const status = await api('/status');
    state.helperOnline = true;
    state.mode = status.settings?.mode || state.mode;
    state.cidrs = status.settings?.campusCidrs || state.cidrs;

    setMode(state.mode, false);
    setRunning(Boolean(status.proxy?.running));

    // Runtime info (optional, for future settings page)
    try {
      const runtime = await api('/runtime');
      state.runtimePresent = runtime.present;
    } catch (_) { /* ignore */ }

    addLog('已连接 freev6 helper');
  } catch (error) {
    state.helperOnline = false;
    addLog(`helper 不可用: ${error.message}`, true);
  }
}

/* ── Traffic Polling ──────────────────────────────────────────── */

async function pollTraffic() {
  if (!state.proxyRunning) {
    // When not running, push zero samples
    state.trafficHistory.push({ t: Date.now(), up: 0, down: 0 });
    if (state.trafficHistory.length > TRAFFIC_POINTS) state.trafficHistory.shift();
    drawChart();
    return;
  }

  try {
    // mihomo controller connections API
    const resp = await fetch('http://127.0.0.1:9090/connections');
    if (!resp.ok) return;
    const data = await resp.json();

    let upTotal = 0, downTotal = 0;
    for (const conn of (data.connections || [])) {
      upTotal += conn.upload || 0;
      downTotal += conn.download || 0;
    }

    const deltaUp = Math.max(0, upTotal - state.lastUpload);
    const deltaDown = Math.max(0, downTotal - state.lastDownload);

    if (state.lastUpload > 0) {
      state.trafficHistory.push({ t: Date.now(), up: deltaUp, down: deltaDown });
      if (state.trafficHistory.length > TRAFFIC_POINTS) state.trafficHistory.shift();
      state.totalUpload += deltaUp;
      state.totalDownload += deltaDown;
    }

    state.lastUpload = upTotal;
    state.lastDownload = downTotal;

    // Update speed display
    $('#speedUp').textContent = formatSpeed(deltaUp);
    $('#speedDown').textContent = formatSpeed(deltaDown);

    // Update stats
    updateStatsRing();

    // Redraw chart
    drawChart();
  } catch (_) {
    // mihomo not reachable, push zero
    state.trafficHistory.push({ t: Date.now(), up: 0, down: 0 });
    if (state.trafficHistory.length > TRAFFIC_POINTS) state.trafficHistory.shift();
    drawChart();
  }
}

/* ── Status Polling ───────────────────────────────────────────── */

async function pollStatus() {
  try {
    const status = await api('/status');
    if (!state.helperOnline) {
      state.helperOnline = true;
      addLog('已连接 freev6 helper');
    }
    const wasRunning = state.proxyRunning;
    const isRunning = Boolean(status.proxy?.running);
    if (wasRunning !== isRunning) {
      setRunning(isRunning);
      addLog(isRunning ? '免流模式已启动' : '免流模式已停止');
    }
    state.mode = status.settings?.mode || state.mode;
    setMode(state.mode, false);
  } catch (_) {
    if (state.helperOnline) {
      state.helperOnline = false;
      setRunning(false);
      addLog('helper 连接断开', true);
    }
  }
}

/* ── Event Wiring ─────────────────────────────────────────────── */

function wireEvents() {
  // Navigation
  $$('.nav-item[data-view]').forEach(item => {
    item.addEventListener('click', () => showView(item.dataset.view));
  });

  // Proxy toggle
  $('#proxyToggle')?.addEventListener('click', async () => {
    if (!state.helperOnline) {
      addLog('helper 未连接，无法操作', true);
      return;
    }
    const card = $('#proxyToggle');
    card.style.pointerEvents = 'none';
    card.style.opacity = '0.6';
    try {
      if (state.proxyRunning) {
        await api('/proxy/stop', { method: 'POST' });
        setRunning(false);
        addLog('免流模式已停止');
      } else {
        await api('/proxy/start', { method: 'POST', body: JSON.stringify({ mode: state.mode, campusCidrs: state.cidrs }) });
        setRunning(true);
        addLog('免流模式已启动');
      }
    } catch (e) {
      addLog(`操作失败: ${e.message}`, true);
    } finally {
      card.style.pointerEvents = '';
      card.style.opacity = '';
    }
  });

  // Mode options
  $$('.mode-option').forEach(el => {
    el.addEventListener('click', () => {
      if (el.dataset.mode && el.dataset.mode !== state.mode) {
        setMode(el.dataset.mode);
      }
    });
  });

  // Log drawer
  $('#logsButton')?.addEventListener('click', () => {
    $('#logDrawer')?.classList.add('open');
    $('#scrim')?.classList.add('open');
  });
  const closeLogs = () => {
    $('#logDrawer')?.classList.remove('open');
    $('#scrim')?.classList.remove('open');
  };
  $('#closeLogs')?.addEventListener('click', closeLogs);
  $('#scrim')?.addEventListener('click', closeLogs);

  // Refresh
  $('#refreshButton')?.addEventListener('click', refreshBackendState);
}

/* ── Init ─────────────────────────────────────────────────────── */

function init() {
  wireEvents();
  renderLogs();
  initChart();
  refreshBackendState();

  // Polling intervals
  setInterval(pollStatus, 3000);
  setInterval(pollTraffic, 1000);
}

document.addEventListener('DOMContentLoaded', init);
