/* ── State ─────────────────────────────────────────────────────── */

const state = {
  activeView: 'dashboard',
  helperOnline: false,
  proxyRunning: false,
  mode: 'rule',
  cidrs: [],
  devMode: false,
  autoStart: false,
  // Appearance (persisted in localStorage, like the theme)
  dynamicColor: false,
  pureBlack: false,
  dynamicSeed: null,

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
  runtimeChecked: false,
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

/* Open a URL in the system browser (Tauri opener plugin), or a new tab when
   the UI runs standalone in a normal browser (dev server). */
async function openExternal(url) {
  try {
    if (window.__TAURI_INTERNALS__?.invoke) {
      await window.__TAURI_INTERNALS__.invoke('plugin:opener|open_url', { url });
      return;
    }
  } catch (_) { /* fall through to the browser fallback */ }
  window.open(url, '_blank', 'noopener,noreferrer');
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

const VIEW_TITLES = { dashboard: '仪表盘', proxies: '接入点', settings: '设置' };

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

/* ── IPv6 gate (启动免流前检测) ─────────────────────────────── */

// Campus prefixes allowed to run 免流, as /32 first-two-hextet fingerprints:
// 2001:da8::/32 (CERNET2) and 2001:250::/32.
const MIULIU_IPV6_PREFIX32 = [[0x2001, 0x0da8], [0x2001, 0x0250]];

// Expand an IPv6 literal to its 8 hextets, or null when unparsable.
function expandIPv6(ip) {
  const cleaned = String(ip).trim().split('%')[0].toLowerCase();
  if (!/^[0-9a-f:]+$/.test(cleaned) || !cleaned.includes(':')) return null;
  const halves = cleaned.split('::');
  if (halves.length > 2) return null;
  const head = halves[0] ? halves[0].split(':') : [];
  const tail = halves.length === 2 && halves[1] ? halves[1].split(':') : [];
  let groups;
  if (halves.length === 2) {
    const fill = 8 - head.length - tail.length;
    if (fill < 1) return null; // '::' must stand for at least one group
    groups = [...head, ...Array(fill).fill('0'), ...tail];
  } else {
    groups = head;
    if (groups.length !== 8) return null;
  }
  if (groups.length !== 8) return null;
  const nums = groups.map((g) => (/^[0-9a-f]{1,4}$/.test(g) ? parseInt(g, 16) : NaN));
  return nums.some(Number.isNaN) ? null : nums;
}

function isInCampusIPv6(ip) {
  const groups = expandIPv6(ip);
  if (!groups) return false;
  return MIULIU_IPV6_PREFIX32.some(([a, b]) => groups[0] === a && groups[1] === b);
}

// Ask a public echo service for this machine's IPv6 (CORS: * verified).
async function detectPublicIPv6(timeoutMs = 8000) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const resp = await fetch('https://api-ipv6.ip.sb/ip', { signal: controller.signal });
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    return (await resp.text()).trim();
  } finally {
    clearTimeout(timer);
  }
}

function showNetGateDialog() {
  $('#netDialogScrim')?.classList.add('open');
}

function closeNetGateDialog() {
  $('#netDialogScrim')?.classList.remove('open');
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
  if (!state.helperOnline) {
    addLog('helper 未连接，设置未保存', true);
    return Promise.resolve(false);
  }
  return api('/settings', { method: 'PUT', body: JSON.stringify({ mode: state.mode, campusCidrs: state.cidrs, devMode: state.devMode, autoStart: state.autoStart }) })
    .then(() => { addLog('设置已保存'); return true; })
    .catch(e => { addLog(`保存设置失败: ${e.message}`, true); return false; });
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
    if (typeof status.settings?.devMode === 'boolean') state.devMode = status.settings.devMode;
    if (typeof status.settings?.autoStart === 'boolean') state.autoStart = status.settings.autoStart;

    setMode(state.mode, false, true);
    setRunning(Boolean(status.proxy?.running));

    // Runtime info — must complete before updating chip/settings UI
    try {
      const runtime = await api('/runtime');
      state.runtimePresent = Boolean(runtime.present);
      state.runtimeChecked = true;
    } catch (_) { /* ignore */ }
    if (state.dynamicColor && !state.dynamicSeed) refreshDynamicColor(true);

    updateConnectionChip(true);

    state.warpRegistered = status.warp?.registered || false;
    updateSettingsUI();

    addLog('已连接 freev6 helper');
  } catch (error) {
    state.helperOnline = false;
    state.runtimePresent = false;
    state.runtimeChecked = false;
    updateConnectionChip(false);
    updateSettingsUI();
    addLog(`helper 不可用: ${error.message}`, true);
  }
}

/* ── Traffic Polling ──────────────────────────────────────────── */

async function pollTraffic() {
  if (!state.proxyRunning) {
    // Stopped: zero the chart history, the totals baseline and the speed readout
    resetTrafficDisplay(true);
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
    // mihomo not reachable: show zeros but keep the totals baseline
    resetTrafficDisplay(false);
  }
}

function resetTrafficDisplay(clearCounters) {
  state.trafficHistory.push({ t: Date.now(), up: 0, down: 0 });
  if (state.trafficHistory.length > TRAFFIC_POINTS) state.trafficHistory.shift();
  if (clearCounters) {
    state.lastUpload = 0;
    state.lastDownload = 0;
  }
  $('#speedUp').textContent = formatSpeed(0);
  $('#speedDown').textContent = formatSpeed(0);
  drawChart();
}

/* ── Status Polling ───────────────────────────────────────────── */

async function pollStatus() {
  try {
    const status = await api('/status');
    const firstConnect = !state.helperOnline;
    state.helperOnline = true;
    // Core detection is a cheap local stat — keep polling it so the settings
    // row flips between “已安装” and the download button with no manual refresh
    try {
      const runtime = await api('/runtime');
      const present = Boolean(runtime.present);
      if (firstConnect || !state.runtimeChecked || state.runtimePresent !== present) {
        state.runtimePresent = present;
        state.runtimeChecked = true;
        updateConnectionChip(true);
        updateSettingsUI();
      }
    } catch (_) {
      if (firstConnect) updateConnectionChip(true);
    }
    if (firstConnect) {
      addLog('已连接 freev6 helper');
      // Helper is up: retry a dynamic-color seed fetch that failed at load
      if (state.dynamicColor && !state.dynamicSeed) refreshDynamicColor(true);
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
    // Sync developer mode / auto-start when they changed on the backend
    const nextDevMode = typeof status.settings?.devMode === 'boolean' ? status.settings.devMode : state.devMode;
    const nextAutoStart = typeof status.settings?.autoStart === 'boolean' ? status.settings.autoStart : state.autoStart;
    if (nextDevMode !== state.devMode || nextAutoStart !== state.autoStart) {
      state.devMode = nextDevMode;
      state.autoStart = nextAutoStart;
      updateSettingsUI();
    }
  } catch (_) {
    if (state.helperOnline) {
      state.helperOnline = false;
      state.runtimePresent = false;
      state.runtimeChecked = false;
      updateConnectionChip(false);
      updateSettingsUI();
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
        // IPv6 gate: 免流 only works on campus IPv6. Developer mode skips it.
        if (!state.devMode) {
          fab.label = '检测中…';
          addLog('启动前检测本机 IPv6…');
          let ip = '';
          let reason = '';
          try {
            ip = await detectPublicIPv6();
          } catch (e) {
            reason = `（检测失败: ${e.message}）`;
          }
          if (!isInCampusIPv6(ip)) {
            showNetGateDialog();
            addLog(`启动已取消：当前网络不支持免流${reason}`, true);
            return;
          }
          addLog(`IPv6 检测通过: ${ip}`);
        } else {
          addLog('开发者模式：跳过 IPv6 检测');
        }
        await api('/proxy/start', { method: 'POST', body: JSON.stringify({ mode: state.mode, campusCidrs: state.cidrs, devMode: state.devMode }) });
        setRunning(true);
        addLog('免流模式已启动');
        autoDelayTestAfterStart();
      }
    } catch (e) {
      addLog(`操作失败: ${e.message}`, true);
    } finally {
      fab.style.pointerEvents = '';
      fab.style.opacity = '';
      if (!state.proxyRunning) fab.label = '启动免流';
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

  // Unsupported-network dialog: dismiss via button, backdrop or Escape
  $('#netDialogOk')?.addEventListener('click', closeNetGateDialog);
  $('#netDialogScrim')?.addEventListener('click', (e) => {
    if (e.target?.id === 'netDialogScrim') closeNetGateDialog();
  });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') closeNetGateDialog();
  });
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
    ? `${state.cidrs.length} 个网段/域名`
    : '管理绕过 WARP 的网段与域名';

  const list = $('#cidrList');
  if (!list) return;
  if (!state.cidrs.length) {
    list.innerHTML = '<div class="empty-state" style="padding:16px"><span>还没有网段或域名</span></div>';
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
      addLog(`移除条目: ${removed}`);
    });
  });
}

function updateSettingsUI() {
  // Mode segmented chips (md-filter-chip)
  $$('#settingsModeGroup .setting-seg').forEach(seg => {
    seg.selected = seg.dataset.mode === state.mode;
  });

  // CIDR list, developer-mode + auto-start switches
  renderCidrs();
  const devSwitch = $('#devModeSwitch');
  if (devSwitch) devSwitch.selected = state.devMode;
  const autoSwitch = $('#autoStartSwitch');
  if (autoSwitch) autoSwitch.selected = state.autoStart;
  const dynSwitch = $('#dynamicColorSwitch');
  if (dynSwitch) dynSwitch.selected = state.dynamicColor;
  const oledSwitch = $('#pureBlackSwitch');
  if (oledSwitch) oledSwitch.selected = state.pureBlack;

  // mihomo core row (status + download merged): pill when installed,
  // download button when the core was not detected, '--' while unknown
  const pill = $('#runtimePill');
  const dlBtn = $('#downloadCore');
  const checked = state.helperOnline && state.runtimeChecked;
  if (pill) {
    if (checked && !state.runtimePresent) {
      pill.hidden = true;
    } else {
      pill.hidden = false;
      pill.textContent = checked ? '已安装' : '--';
      pill.className = checked ? 'setting-pill ready' : 'setting-pill';
    }
  }
  if (dlBtn) {
    dlBtn.hidden = !(checked && !state.runtimePresent);
    if (!dlBtn.hidden) {
      dlBtn.disabled = false;
      dlBtn.textContent = '下载';
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

  // GitHub project — opens in the system browser
  $('#githubItem')?.addEventListener('click', () => {
    openExternal('https://github.com/evansrrr/free-v6');
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

  // Settings sub-page: whitelist (the blacklist lives in settings.json only)
  const wireSubPage = (itemSelector, pageSelector, backSelector) => {
    $(itemSelector)?.addEventListener('click', () => {
      const page = $(pageSelector);
      const groups = $('.settings-groups');
      if (page && groups) {
        groups.style.display = 'none';
        page.style.display = 'block';
      }
    });
    $(backSelector)?.addEventListener('click', () => {
      const page = $(pageSelector);
      const groups = $('.settings-groups');
      if (page && groups) {
        page.style.display = 'none';
        groups.style.display = '';
      }
    });
  };
  wireSubPage('#cidrSettingItem', '#cidrPage', '#cidrBack');

  // Add whitelist entries
  $('#addCidr')?.addEventListener('click', addCidr);
  $('#cidrInput')?.addEventListener('keydown', (e) => { if (e.key === 'Enter') addCidr(); });

  // Developer mode — when on, the blacklist is not applied. Only announce it
  // after the helper confirmed the save; otherwise revert the switch so the
  // UI never claims a state that did not persist (e.g. helper offline).
  $('#devModeSwitch')?.addEventListener('change', async (e) => {
    const next = e.target.selected;
    const previous = state.devMode;
    state.devMode = next;
    const saved = await persistSettings();
    if (!saved) {
      state.devMode = previous;
      e.target.selected = previous;
      return;
    }
    addLog(next ? '开发者模式已开启，不阻断黑名单域名' : '开发者模式已关闭，恢复阻断黑名单域名');
  });

  // Auto-start with Windows — helper writes the HKCU Run entry (launches the
  // exe with --minimized → tray only). Same contract as developer mode:
  // announce only after the helper confirmed, revert the switch on failure.
  $('#autoStartSwitch')?.addEventListener('change', async (e) => {
    const next = e.target.selected;
    const previous = state.autoStart;
    state.autoStart = next;
    const saved = await persistSettings();
    if (!saved) {
      state.autoStart = previous;
      e.target.selected = previous;
      return;
    }
    addLog(next ? '已开启开机自启动（启动后仅驻留托盘）' : '已关闭开机自启动');
  });

  // Dynamic color — seed from the wallpaper (system accent as fallback) via
  // the helper; persists in localStorage and applies immediately.
  $('#dynamicColorSwitch')?.addEventListener('change', async (e) => {
    state.dynamicColor = e.target.selected;
    try { localStorage.setItem('freev6-dynamic-color', state.dynamicColor ? '1' : '0'); } catch (_) {}
    if (state.dynamicColor) {
      await refreshDynamicColor(false);
    } else {
      state.dynamicSeed = null;
      applyDynamicPalette();
      addLog('动态取色已关闭');
    }
  });

  // Pure black background (OLED) — surfaces go #000, dark themes only
  $('#pureBlackSwitch')?.addEventListener('change', (e) => {
    state.pureBlack = e.target.selected;
    try { localStorage.setItem('freev6-pure-black', state.pureBlack ? '1' : '0'); } catch (_) {}
    applyPureBlack(state.pureBlack);
    addLog(state.pureBlack ? '纯黑背景已开启（深色主题下生效）' : '纯黑背景已关闭');
  });

  // Download core
  $('#downloadCore')?.addEventListener('click', async () => {
    if (!state.helperOnline) { addLog('helper 未连接', true); return; }
    const btn = $('#downloadCore');
    if (btn) { btn.disabled = true; btn.textContent = '下载中…'; }
    try {
      const result = await api('/runtime/download', { method: 'POST' });
      state.runtimePresent = true;
      state.runtimeChecked = true;
      addLog(`mihomo 核心下载完成${result.version ? `: ${result.version}` : ''}`);
    } catch (e) {
      addLog(`下载失败: ${e.message}`, true);
    } finally {
      updateSettingsUI();
      updateConnectionChip(state.helperOnline);
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

// CIDR (10.0.0.0/8) or bare domain (pku.edu.cn); the helper validates precisely
const CIDR_ENTRY_RE = /^[0-9A-Fa-f:.]+\/\d{1,3}$/;
const DOMAIN_ENTRY_RE = /^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$/;

function addCidr() {
  const input = $('#cidrInput');
  if (!input) return;
  const value = input.value.trim().toLowerCase();
  if (!value) return;
  const valid = value.includes('/')
    ? CIDR_ENTRY_RE.test(value)
    : DOMAIN_ENTRY_RE.test(value);
  if (!valid) {
    addLog(`无效网段或域名: ${value}`, true);
    return;
  }
  if (!state.cidrs.includes(value)) {
    state.cidrs.push(value);
    renderCidrs();
    persistSettings();
    addLog(`添加条目: ${value}`);
  }
  input.value = '';
}

function applyTheme(theme) {
  document.documentElement.setAttribute('data-theme', theme);
  try { localStorage.setItem('freev6-theme', theme); } catch (_) {}
  // The derived palette's lightness depends on dark vs light — re-derive
  if (state.dynamicColor && state.dynamicSeed) applyDynamicPalette();
  // Listen for system changes when in system mode
  if (theme === 'system') {
    if (!state._systemThemeListener) {
      state._systemThemeListener = window.matchMedia('(prefers-color-scheme: light)');
      state._systemThemeListener.addEventListener('change', () => {
        // CSS variables auto-switch via [data-theme="system"] + media query;
        // the derived palette needs a manual re-derive for the new scheme
        applyDynamicPalette();
      });
    }
  }
}

function loadSavedTheme() {
  let saved = null;
  try { saved = localStorage.getItem('freev6-theme'); } catch (_) {}
  // Default to following the system color scheme when the user never picked one
  const theme = saved || 'system';
  applyTheme(theme);
  const select = $('#themeSelect');
  if (select) select.value = theme;
}

/* ── Appearance: dynamic color + OLED black ────────────────────── */

const DYNAMIC_PALETTE_VARS = [
  '--md-primary', '--md-on-primary', '--md-primary-container', '--md-on-primary-container',
  '--md-secondary', '--md-on-secondary', '--md-secondary-container', '--md-on-secondary-container',
  '--md-tertiary', '--md-on-tertiary', '--md-tertiary-container', '--md-on-tertiary-container',
];

function clamp(v, lo, hi) { return Math.min(hi, Math.max(lo, v)); }

function hexToHsl(hex) {
  const value = parseInt(hex.replace('#', ''), 16);
  const r = (value >> 16 & 255) / 255;
  const g = (value >> 8 & 255) / 255;
  const b = (value & 255) / 255;
  const max = Math.max(r, g, b);
  const min = Math.min(r, g, b);
  const l = (max + min) / 2;
  if (max === min) return { h: 0, s: 0, l };
  const d = max - min;
  const s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
  let h;
  if (max === r) h = (g - b) / d + (g < b ? 6 : 0);
  else if (max === g) h = (b - r) / d + 2;
  else h = (r - g) / d + 4;
  return { h: h * 60, s, l };
}

function hslToHex(h, s, l) {
  h = ((h % 360) + 360) % 360;
  const c = (1 - Math.abs(2 * l - 1)) * s;
  const x = c * (1 - Math.abs((h / 60) % 2 - 1));
  const m = l - c / 2;
  const [r, g, b] =
    h < 60 ? [c, x, 0] : h < 120 ? [x, c, 0] : h < 180 ? [0, c, x]
    : h < 240 ? [0, x, c] : h < 300 ? [x, 0, c] : [c, 0, x];
  const to = (v) => Math.round((v + m) * 255).toString(16).padStart(2, '0');
  return `#${to(r)}${to(g)}${to(b)}`;
}

function isDarkTheme() {
  const theme = document.documentElement.getAttribute('data-theme');
  if (theme === 'light') return false;
  if (theme === 'dark') return true;
  return window.matchMedia('(prefers-color-scheme: dark)').matches;
}

// Material-You-ish three-family palette derived from one seed color.
// Returns hex values (canvas/utils parse hex only).
function derivePalette(seedHex, dark) {
  const { h, s } = hexToHsl(seedHex);
  // Gray seeds (wallpaper without a dominant hue) get a sensible chroma
  const S = clamp(s < 0.12 ? 0.45 : s, 0.30, 0.90);
  const hex = (hh, ss, ll) => hslToHex(hh, clamp(ss, 0, 1), clamp(ll, 0, 1));
  const T = h + 60; // tertiary hue rotation (M3-ish)
  if (dark) {
    return {
      '--md-primary': hex(h, S, 0.68),
      '--md-on-primary': hex(h, Math.min(S, 0.45), 0.12),
      '--md-primary-container': hex(h, S * 0.45, 0.30),
      '--md-on-primary-container': hex(h, S, 0.86),
      '--md-secondary': hex(h, S * 0.35, 0.74),
      '--md-on-secondary': hex(h, 0.20, 0.12),
      '--md-secondary-container': hex(h, S * 0.25, 0.30),
      '--md-on-secondary-container': hex(h, S * 0.50, 0.86),
      '--md-tertiary': hex(T, S * 0.50, 0.72),
      '--md-on-tertiary': hex(T, 0.30, 0.12),
      '--md-tertiary-container': hex(T, S * 0.35, 0.30),
      '--md-on-tertiary-container': hex(T, S * 0.60, 0.86),
    };
  }
  return {
    '--md-primary': hex(h, S, 0.42),
    '--md-on-primary': hex(h, Math.min(S, 0.6), 0.99),
    '--md-primary-container': hex(h, S * 0.40, 0.90),
    '--md-on-primary-container': hex(h, S, 0.18),
    '--md-secondary': hex(h, S * 0.30, 0.45),
    '--md-on-secondary': hex(h, 0.30, 0.99),
    '--md-secondary-container': hex(h, S * 0.25, 0.90),
    '--md-on-secondary-container': hex(h, S * 0.50, 0.18),
    '--md-tertiary': hex(T, S * 0.45, 0.45),
    '--md-on-tertiary': hex(T, 0.35, 0.99),
    '--md-tertiary-container': hex(T, S * 0.30, 0.90),
    '--md-on-tertiary-container': hex(T, S * 0.50, 0.18),
  };
}

function applyDynamicPalette() {
  const root = document.documentElement;
  DYNAMIC_PALETTE_VARS.forEach(name => root.style.removeProperty(name));
  if (!state.dynamicColor || !state.dynamicSeed) return;
  const palette = derivePalette(state.dynamicSeed, isDarkTheme());
  Object.keys(palette).forEach(name => root.style.setProperty(name, palette[name]));
}

async function refreshDynamicColor(silent = false) {
  if (!state.dynamicColor) return;
  try {
    const res = await api('/appearance');
    if (!res.color) {
      if (!silent) addLog('动态取色：壁纸与系统强调色均不可用', true);
      return;
    }
    state.dynamicSeed = res.color;
    applyDynamicPalette();
    if (!silent) {
      addLog(res.source === 'wallpaper'
        ? `动态取色已应用（壁纸主色 ${res.color}）`
        : `动态取色已应用（系统强调色 ${res.color}）`);
    }
  } catch (e) {
    if (!silent) addLog(`动态取色失败: ${e.message}`, true);
    // silent = background retry; the seed is fetched again on helper (re)connect
  }
}

function applyPureBlack(on) {
  if (on) document.documentElement.setAttribute('data-oled', '1');
  else document.documentElement.removeAttribute('data-oled');
}

function loadAppearancePrefs() {
  try {
    state.dynamicColor = localStorage.getItem('freev6-dynamic-color') === '1';
    state.pureBlack = localStorage.getItem('freev6-pure-black') === '1';
  } catch (_) {}
  applyPureBlack(state.pureBlack);
  if (state.dynamicColor) refreshDynamicColor(true); // helper may still be connecting
}

/* ── Window Controls (immersive titlebar) ─────────────────────── */

const tauriInvoke = (cmd, args) => {
  const bridge = window.__TAURI__?.core || window.__TAURI_INTERNALS__;
  return bridge ? bridge.invoke(cmd, args) : Promise.resolve(undefined);
};

function wireTitlebar() {
  const bar = $('#titlebar');
  if (!bar) return;
  const maxIcon = $('#winMaximizeIcon');
  const setMaxIcon = (maximized) => {
    if (maxIcon) maxIcon.textContent = maximized ? 'filter_none' : 'crop_square';
  };
  const refreshMax = () => tauriInvoke('window_maximized').then(setMaxIcon);
  refreshMax();

  $('#winMinimize')?.addEventListener('click', () => tauriInvoke('window_minimize'));
  $('#winMaximize')?.addEventListener('click', () =>
    tauriInvoke('window_toggle_maximize').then(setMaxIcon));
  $('#winClose')?.addEventListener('click', () => tauriInvoke('window_close'));
  // Re-sync when the state may have changed elsewhere (e.g. Win+Arrow snap)
  $('#winMaximize')?.addEventListener('mouseenter', refreshMax);

  // Drag anywhere on the bar (except buttons) to move; double-click toggles maximize
  bar.addEventListener('mousedown', (e) => {
    if (e.button !== 0) return;
    if (e.target.closest('.window-controls')) return;
    tauriInvoke('window_start_dragging');
  });
  bar.addEventListener('dblclick', (e) => {
    if (e.target.closest('.window-controls')) return;
    tauriInvoke('window_toggle_maximize').then(setMaxIcon);
  });
}

/* ── Init ─────────────────────────────────────────────────────── */

function init() {
  loadSavedTheme();
  loadAppearancePrefs();
  wireTitlebar();
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
