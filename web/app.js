/* ── State ─────────────────────────────────────────────────────── */

const state = {
  activeView: 'dashboard',
  helperOnline: false,
  proxyRunning: false,
  mode: 'rule',
  cidrs: [],

  // Traffic
  trafficHistory: [],
  totalUpload: 0,
  totalDownload: 0,
  lastUpload: 0,
  lastDownload: 0,

  // Proxy
  proxyGroup: null,       // { now, all: [] }
  proxyDelays: {},
  testingDelay: false,
  delaysReady: false,

  // Runtime
  runtimePresent: false,
  runtimeVersion: '',
  warpRegistered: false,

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
    // FABs are window-anchored: proxy toggle on dashboard, delay test on proxies
    $('#proxyToggle')?.classList.toggle('visible', view === 'dashboard');
    $('#delayTestFab')?.classList.toggle('visible', view === 'proxies');
  }, oldView ? 80 : 0);

  // Update nav highlighting immediately
  $$('.nav-item[data-view]').forEach(item => {
    item.classList.toggle('active', item.dataset.view === view);
  });
}

/* ── Proxy Toggle ─────────────────────────────────────────────── */

function setRunning(running) {
  state.proxyRunning = running;
  const fab = $('#proxyToggle');
  const icon = $('#toggleIcon');

  if (icon) icon.textContent = running ? 'stop' : 'play_arrow';
  if (fab) fab.label = running ? '停止免流' : '启动免流';
  fab?.classList.toggle('running', running);
}

function updateConnectionChip(online) {
  const chip = $('#connectionChip');
  const dot = $('#headerDot');
  const headerStatus = $('#headerStatus');
  if (!chip) return;
  const ready = online && state.runtimePresent;
  chip.classList.toggle('connected', ready);
  dot.classList.toggle('live', ready);
  if (!online) {
    headerStatus.textContent = '核心未连接';
  } else if (!state.runtimePresent) {
    headerStatus.textContent = '需要下载核心';
  } else {
    headerStatus.textContent = '核心已就绪';
  }
}

/* ── Mode Selector ────────────────────────────────────────────── */

function setMode(mode, persist = true, silent = false) {
  if (mode === state.mode && silent) return;
  state.mode = mode;
  // Sync settings page segmented chips (md-filter-chip)
  $$('#settingsModeGroup .setting-seg').forEach(seg => {
    seg.selected = seg.dataset.mode === mode;
  });
  if (!silent) addLog(`切换为${mode === 'rule' ? '规则' : '全局'}模式`);
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
    ctx.font = '12px "DM Sans", sans-serif';
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
  ctx.strokeStyle = secondary;
  ctx.lineWidth = 2;
  ctx.lineJoin = 'round';
  ctx.stroke();
  // Fill
  ctx.lineTo((TRAFFIC_POINTS - 1) * stepX, chartH);
  ctx.lineTo((TRAFFIC_POINTS - upVals.length) * stepX, chartH);
  ctx.closePath();
  ctx.fillStyle = toRGBA(secondary, 0.08);
  ctx.fill();

  // Draw download line
  ctx.beginPath();
  for (let i = 0; i < downVals.length; i++) {
    const x = (TRAFFIC_POINTS - downVals.length + i) * stepX;
    const y = chartH - (downVals[i] / maxVal) * (chartH - 8) - 2;
    if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
  }
  ctx.strokeStyle = primary;
  ctx.lineWidth = 2;
  ctx.lineJoin = 'round';
  ctx.stroke();
  // Fill
  ctx.lineTo((TRAFFIC_POINTS - 1) * stepX, chartH);
  ctx.lineTo((TRAFFIC_POINTS - downVals.length) * stepX, chartH);
  ctx.closePath();
  ctx.fillStyle = toRGBA(primary, 0.06);
  ctx.fill();
}

/* ── Stats Ring Chart ─────────────────────────────────────────── */

function updateStatsRing() {
  const total = state.totalUpload + state.totalDownload;
  const circumference = 2 * Math.PI * 13.5; // ≈ 84.8
  const uploadLen = total > 0 ? (state.totalUpload / total) * circumference : 0;
  const downloadLen = total > 0 ? (state.totalDownload / total) * circumference : 0;

  const uploadRing = $('#ringUpload');
  const downloadRing = $('#ringDownload');

  if (uploadRing) {
    uploadRing.setAttribute('stroke-dasharray', `${uploadLen} ${circumference - uploadLen}`);
    uploadRing.setAttribute('transform', `rotate(-90 18 18)`);
  }
  if (downloadRing) {
    // Download starts where upload ends
    const offset = -uploadLen;
    downloadRing.setAttribute('stroke-dasharray', `${downloadLen} ${circumference - downloadLen}`);
    downloadRing.setAttribute('transform', `rotate(${-90 + (uploadLen / circumference) * 360} 18 18)`);
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

    setMode(state.mode, false, true);
    setRunning(Boolean(status.proxy?.running));

    // Runtime info — must complete before updating chip
    try {
      const runtime = await api('/runtime');
      state.runtimePresent = runtime.present;
      state.runtimeVersion = runtime.version || '';
    } catch (_) { /* ignore */ }

    updateConnectionChip(true);

    state.warpRegistered = status.warp?.registered || false;
    updateSettingsUI();

    addLog('已连接 freev6 helper');
  } catch (error) {
    state.helperOnline = false;
    state.runtimePresent = false;
    updateConnectionChip(false);
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
      // Check runtime presence on first reconnect
      try {
        const runtime = await api('/runtime');
        state.runtimePresent = runtime.present;
        state.runtimeVersion = runtime.version || '';
      } catch (_) {}
      updateConnectionChip(true);
      addLog('已连接 freev6 helper');
    }
    const wasRunning = state.proxyRunning;
    const isRunning = Boolean(status.proxy?.running);
    if (wasRunning !== isRunning) {
      setRunning(isRunning);
      addLog(isRunning ? '免流模式已启动' : '免流模式已停止');
      if (isRunning) autoDelayTestAfterStart();
    }
    state.mode = status.settings?.mode || state.mode;
    setMode(state.mode, false, true);
  } catch (_) {
    if (state.helperOnline) {
      state.helperOnline = false;
      updateConnectionChip(false);
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

  // Proxy toggle FAB
  $('#proxyToggle')?.addEventListener('click', async () => {
    if (!state.helperOnline) {
      addLog('helper 未连接，无法操作', true);
      return;
    }
    const fab = $('#proxyToggle');
    fab.style.pointerEvents = 'none';
    fab.style.opacity = '0.6';
    try {
      if (state.proxyRunning) {
        await api('/proxy/stop', { method: 'POST' });
        setRunning(false);
        addLog('免流模式已停止');
      } else {
        await api('/proxy/start', { method: 'POST', body: JSON.stringify({ mode: state.mode, campusCidrs: state.cidrs }) });
        setRunning(true);
        addLog('免流模式已启动');
        autoDelayTestAfterStart();
      }
    } catch (e) {
      addLog(`操作失败: ${e.message}`, true);
    } finally {
      fab.style.pointerEvents = '';
      fab.style.opacity = '';
    }
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
  $('#refreshButton')?.addEventListener('click', () => {
    addLog('手动刷新');
    refreshBackendState();
    fetchProxies();
  });
}

/* ── Proxy Page (simplified: auto-select group only) ──────────── */

const AUTO_SELECT_GROUP = '♻️ 自动选择';

async function fetchProxies() {
  if (!state.helperOnline) return;
  try {
    const resp = await fetch('http://127.0.0.1:9090/proxies');
    if (!resp.ok) return;
    const data = await resp.json();
    const info = data.proxies?.[AUTO_SELECT_GROUP];
    if (!info) return;
    state.proxyGroup = {
      now: info.now || '',
      all: info.all || [],
    };
    renderNodeGrid();
  } catch (_) { /* mihomo not reachable */ }
}

function renderNodeGrid() {
  const el = $('#nodeGrid');
  if (!el) return;

  const group = state.proxyGroup;
  if (!group || !group.all.length) {
    el.innerHTML = '<div class="empty-state"><md-icon class="empty-state-icon">cloud_off</md-icon><span>请先启动免流模式以加载节点列表</span></div>';
    return;
  }

  let nodes = [...group.all];

  // Auto-sort by delay after test
  if (state.delaysReady) {
    nodes.sort((a, b) => {
      const da = state.proxyDelays[a] ?? Infinity;
      const db = state.proxyDelays[b] ?? Infinity;
      return da - db;
    });
  }

  el.innerHTML = nodes.map(name => {
    const selected = name === group.now;
    const delay = state.proxyDelays[name];
    const testing = state.testingDelay && delay === undefined;
    let latencyClass = 'unknown';
    let latencyText = '未测试';
    if (testing) {
      latencyClass = 'testing';
      latencyText = '<md-circular-progress class="node-spinner" indeterminate></md-circular-progress>测试中';
    } else if (delay !== undefined) {
      if (delay < 100) { latencyClass = 'good'; latencyText = delay + ' ms'; }
      else if (delay < 500) { latencyClass = 'medium'; latencyText = delay + ' ms'; }
      else { latencyClass = 'bad'; latencyText = delay >= 5000 ? 'Timeout' : delay + ' ms'; }
    }

    return `<div class="node-card${selected ? ' selected' : ''}${testing ? ' testing' : ''}" data-name="${name}">
      <div class="node-name" title="${name}">${name}</div>
      <div class="node-latency ${latencyClass}">${latencyText}</div>
    </div>`;
  }).join('');

  el.querySelectorAll('.node-card').forEach(card => {
    card.addEventListener('click', () => switchProxy(card.dataset.name));
  });
}

async function switchProxy(name) {
  if (!state.proxyRunning || !AUTO_SELECT_GROUP) return;
  try {
    await fetch(`http://127.0.0.1:9090/proxies/${encodeURIComponent(AUTO_SELECT_GROUP)}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name }),
    });
    if (state.proxyGroup) state.proxyGroup.now = name;
    renderNodeGrid();
    addLog(`切换节点: ${name}`);
  } catch (e) {
    addLog(`切换节点失败: ${e.message}`, true);
  }
}

async function runDelayTest() {
  if (state.testingDelay) return;
  state.testingDelay = true;
  state.proxyDelays = {};
  state.delaysReady = false;

  const fab = $('#delayTestFab');
  const fabIcon = $('#fabIcon');
  const fabProgress = $('#fabProgress');
  if (fab) fab.classList.add('loading');
  if (fabIcon) fabIcon.hidden = true;
  if (fabProgress) fabProgress.hidden = false;
  if (fab) fab.label = '测试中…';
  renderNodeGrid();

  const group = state.proxyGroup;
  if (!group) { resetFab(); return; }

  const nodes = group.all;
  let completed = 0;
  const CONCURRENCY = 6;
  const queue = [...nodes];

  async function testNext() {
    while (queue.length) {
      const name = queue.shift();
      try {
        const resp = await fetch(`http://127.0.0.1:9090/proxies/${encodeURIComponent(name)}/delay?timeout=5000&url=http://www.gstatic.com/generate_204`);
        if (resp.ok) {
          const data = await resp.json();
          state.proxyDelays[name] = data.delay || 5000;
        } else {
          state.proxyDelays[name] = 5000;
        }
      } catch (_) {
        state.proxyDelays[name] = 5000;
      }
      completed++;
      if (fab) fab.label = `测试中 ${completed}/${nodes.length}`;
      renderNodeGrid();
    }
  }

  const workers = [];
  for (let i = 0; i < Math.min(CONCURRENCY, nodes.length); i++) {
    workers.push(testNext());
  }
  await Promise.all(workers);

  state.testingDelay = false;
  state.delaysReady = true;
  resetFab();
  renderNodeGrid(); // re-render with auto-sort
  addLog(`延迟测试完成: ${nodes.length} 个节点`);
}

function resetFab() {
  const fab = $('#delayTestFab');
  const fabIcon = $('#fabIcon');
  const fabProgress = $('#fabProgress');
  if (fab) {
    fab.classList.remove('loading');
    fab.label = '测试延迟';
  }
  if (fabIcon) fabIcon.hidden = false;
  if (fabProgress) fabProgress.hidden = true;
}

function wireProxyEvents() {
  $('#delayTestFab')?.addEventListener('click', runDelayTest);
}

/* Auto-run one delay test after 免流模式 starts */
async function autoDelayTestAfterStart() {
  addLog('启动完成，自动进行延迟测试');
  // mihomo may need a moment before the proxy list is exposed
  for (let i = 0; i < 10 && (!state.proxyGroup || !state.proxyGroup.all.length); i++) {
    await new Promise(r => setTimeout(r, 1000));
    if (!state.proxyRunning) return;
    await fetchProxies();
  }
  if (state.testingDelay) return;
  if (state.proxyGroup && state.proxyGroup.all.length) {
    runDelayTest();
  } else {
    addLog('自动延迟测试跳过: 节点列表未就绪', true);
  }
}

/* ── Settings Page ────────────────────────────────────────────── */

function renderCidrs() {
  const desc = $('#cidrDesc');
  if (desc) desc.textContent = state.cidrs.length
    ? `${state.cidrs.length} 个自定义网段`
    : '管理绕过 WARP 的 CIDR 网段';

  const list = $('#cidrList');
  if (!list) return;
  if (!state.cidrs.length) {
    list.innerHTML = '<div class="empty-state" style="padding:16px"><span>还没有自定义网段</span></div>';
    return;
  }
  list.innerHTML = state.cidrs.map((cidr, i) =>
    `<div class="cidr-item"><span>${cidr}</span><md-icon-button class="cidr-remove" data-idx="${i}" aria-label="删除"><md-icon>close</md-icon></md-icon-button></div>`
  ).join('');

  list.querySelectorAll('.cidr-remove').forEach(btn => {
    btn.addEventListener('click', () => {
      const idx = Number(btn.dataset.idx);
      const removed = state.cidrs.splice(idx, 1)[0];
      renderCidrs();
      persistSettings();
      addLog(`移除网段: ${removed}`);
    });
  });
}

function updateSettingsUI() {
  // Mode segmented chips (md-filter-chip)
  $$('#settingsModeGroup .setting-seg').forEach(seg => {
    seg.selected = seg.dataset.mode === state.mode;
  });

  // CIDR desc
  renderCidrs();

  // Runtime pill
  const pill = $('#runtimePill');
  if (pill) {
    if (state.runtimePresent) {
      pill.textContent = '已安装';
      pill.className = 'setting-pill ready';
    } else {
      pill.textContent = '未安装';
      pill.className = 'setting-pill warn';
    }
  }
  const ver = $('#runtimeVersion');
  if (ver) ver.textContent = state.runtimePresent && state.runtimeVersion ? state.runtimeVersion : (state.runtimePresent ? 'mihomo Alpha 已就绪' : '未发现核心');

  // Download button
  const dlBtn = $('#downloadCore');
  if (dlBtn) {
    if (state.runtimePresent) {
      dlBtn.textContent = '已安装';
      dlBtn.disabled = true;
    } else {
      dlBtn.textContent = '下载';
      dlBtn.disabled = false;
    }
  }

  // WARP status
  const warp = $('#warpStatus');
  if (warp) warp.textContent = state.warpRegistered ? '设备已注册' : '首次使用前请注册';
}

function wireSettingsEvents() {
  // Theme select
  $('#themeSelect')?.addEventListener('change', (e) => {
    const theme = e.target.value;
    applyTheme(theme);
    addLog(`切换主题: ${theme}`);
  });

  // Mode segmented chips in settings
  $$('#settingsModeGroup .setting-seg').forEach(seg => {
    seg.addEventListener('click', () => {
      if (!seg.dataset.mode) return;
      if (seg.dataset.mode !== state.mode) setMode(seg.dataset.mode);
      // Filter chips toggle themselves on click — re-sync selection from state
      updateSettingsUI();
    });
  });

  // CIDR sub-page
  $('#cidrSettingItem')?.addEventListener('click', () => {
    const page = $('#cidrPage');
    const groups = $('.settings-groups');
    if (page && groups) {
      groups.style.display = 'none';
      page.style.display = 'block';
    }
  });
  $('#cidrBack')?.addEventListener('click', () => {
    const page = $('#cidrPage');
    const groups = $('.settings-groups');
    if (page && groups) {
      page.style.display = 'none';
      groups.style.display = '';
    }
  });

  // Add CIDR
  $('#addCidr')?.addEventListener('click', addCidr);
  $('#cidrInput')?.addEventListener('keydown', (e) => { if (e.key === 'Enter') addCidr(); });

  // Download core
  $('#downloadCore')?.addEventListener('click', async () => {
    if (!state.helperOnline) { addLog('helper 未连接', true); return; }
    const btn = $('#downloadCore');
    if (btn) { btn.disabled = true; btn.textContent = '下载中…'; }
    try {
      const result = await api('/runtime/download', { method: 'POST' });
      state.runtimePresent = true;
      state.runtimeVersion = result.version || '';
      updateSettingsUI();
      updateConnectionChip(true);
      addLog(`mihomo 下载完成: ${result.version}`);
    } catch (e) {
      addLog(`下载失败: ${e.message}`, true);
    } finally {
      if (btn && !state.runtimePresent) { btn.disabled = false; btn.textContent = '下载'; }
    }
  });

  // Register WARP
  $('#registerWarp')?.addEventListener('click', async () => {
    if (!state.helperOnline) { addLog('helper 未连接', true); return; }
    try {
      await api('/warp/register', { method: 'POST', body: JSON.stringify({ name: 'freev6-windows' }) });
      state.warpRegistered = true;
      updateSettingsUI();
      addLog('WARP 注册成功');
    } catch (e) {
      addLog(`WARP 注册失败: ${e.message}`, true);
    }
  });
}

function addCidr() {
  const input = $('#cidrInput');
  if (!input) return;
  const value = input.value.trim();
  if (!value) return;
  if (!value.includes('/')) {
    addLog(`无效 CIDR: ${value}`, true);
    return;
  }
  if (!state.cidrs.includes(value)) {
    state.cidrs.push(value);
    renderCidrs();
    persistSettings();
    addLog(`添加网段: ${value}`);
  }
  input.value = '';
}

function applyTheme(theme) {
  document.documentElement.setAttribute('data-theme', theme);
  try { localStorage.setItem('freev6-theme', theme); } catch (_) {}
  // Listen for system changes when in system mode
  if (theme === 'system') {
    if (!state._systemThemeListener) {
      state._systemThemeListener = window.matchMedia('(prefers-color-scheme: light)');
      state._systemThemeListener.addEventListener('change', () => {
        // CSS variables auto-switch via [data-theme="system"] + media query
        // No JS action needed, but we can log
      });
    }
  }
}

function loadSavedTheme() {
  try {
    const saved = localStorage.getItem('freev6-theme');
    if (saved) {
      applyTheme(saved);
      const select = $('#themeSelect');
      if (select) select.value = saved;
    }
  } catch (_) {}
}

/* ── Init ─────────────────────────────────────────────────────── */

function init() {
  loadSavedTheme();
  wireEvents();
  wireProxyEvents();
  wireSettingsEvents();
  renderLogs();
  initChart();
  refreshBackendState();

  // Show proxy FAB on initial dashboard view
  $('#proxyToggle')?.classList.add('visible');

  setInterval(pollStatus, 3000);
  setInterval(pollTraffic, 1000);
  setInterval(fetchProxies, 5000);
}

document.addEventListener('DOMContentLoaded', init);
