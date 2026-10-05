/* ── State ── */

const state = {
  activeView: 'dashboard',
  helperOnline: false,
  proxyRunning: false,
  mode: 'rule',
  cidrs: [],
  devMode: false,
  autoStart: false,
  silentStart: false,
  autoRunProxy: false,
  hotkeyEnabled: false,
  // Update
  updateInfo: null,
  updatePhase: null,
  updateError: '',
  // Appearance
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
  warpRegistering: false,
  warpAutoTried: false,

  // Logs
  logs: [],
};

const API_BASE = 'http://127.0.0.1:13335/api/v1';
const MAX_LOG = 100;
const TRAFFIC_POINTS = 60;

const $ = (s) => document.querySelector(s);
const $$ = (s) => [...document.querySelectorAll(s)];

/* ── Utility ── */

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

/* ── API ── */

async function api(path, options = {}) {
  const response = await fetch(`${API_BASE}${path}`, {
    ...options,
    headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
  });
  const payload = await response.json();
  if (!response.ok) throw new Error(payload.error || `HTTP ${response.status}`);
  return payload;
}

// Invoke a Tauri host command through __TAURI_INTERNALS__ (the same channel
// the custom window controls use). Returns false outside the Tauri webview
// so callers can fall back to plain browser navigation.
async function invokeHost(cmd) {
  try {
    const tauri = window.__TAURI_INTERNALS__;
    if (tauri && typeof tauri.invoke === 'function') {
      await tauri.invoke(cmd);
      return true;
    }
  } catch (e) {
    addLog(`调用 ${cmd} 失败: ${e}`, true);
  }
  return false;
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

/* ── Logging ── */

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

/* ── Navigation ── */

const VIEW_TITLES = { dashboard: '仪表盘', proxies: '接入点', settings: '设置' };
const MODE_LABELS = { rule: '规则', global: '全局', direct: '直连' };

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

/* ── Proxy Toggle ── */

// FAB icons (hand-drawn, single-color currentColor so they follow the theme
// and dynamic color): rounded play triangle / rounded square.
const ICON_PLAY = '<svg viewBox="0 0 24 24" aria-hidden="true"><polygon points="9.1,5.6 17.9,12 9.1,18.4" fill="currentColor" stroke="currentColor" stroke-width="3.4" stroke-linejoin="round"/></svg>';
const ICON_STOP = '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="6" y="6" width="12" height="12" rx="3.5"/></svg>';

function setRunning(running) {
  state.proxyRunning = running;
  const fab = $('#proxyToggle');
  const icon = $('#toggleIcon');

  if (icon) icon.innerHTML = running ? ICON_STOP : ICON_PLAY;
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

/* ── start ── */

// core detect → IPv6 gatekeeper → /proxy/start。
// output { ok: true } or { ok: false, reason, message, detail }：
//   core    no runtime core detected
//   offline no internet
//   net     not cernet2 IPv6
//   busy    last start still in progress
//   api     /proxy/start failed
// auto=true
let startFlowBusy = false;
async function startProxyFlow(auto = false) {
  if (startFlowBusy) return { ok: false, reason: 'busy', message: '启动流程正在进行' };
  if (!state.helperOnline) return { ok: false, reason: 'helper', message: 'helper 未连接' };
  startFlowBusy = true;
  try {
    // core detect: fehlen → hint download
    try {
      const runtime = await api('/runtime');
      state.runtimePresent = Boolean(runtime.present);
      state.runtimeChecked = true;
    } catch (_) { /* 保持上次已知状态 */ }
    updateConnectionChip(state.helperOnline);
    if (!state.runtimePresent) return { ok: false, reason: 'core', message: '未检测到运行核心' };

    // IPv6 gate: only works on campus IPv6. Developer mode skips it.
    if (!state.devMode) {
      if (!auto) {
        $('#proxyToggle').label = '检测中…';
        addLog('启动前检测本机 IPv6…');
      }
      let ip = '';
      let detail = '';
      try {
        ip = await detectPublicIPv6();
      } catch (e) {
        detail = `（检测失败: ${e.message}）`;
      }
      if (!ip) {
        return { ok: false, reason: 'offline', message: `IPv6 检测失败${detail}`, detail };
      }
      if (!isInCampusIPv6(ip)) {
        return { ok: false, reason: 'net', message: `当前网络不支持免流${detail}`, detail };
      }
      if (!auto) addLog(`IPv6 检测通过: ${ip}`);
    } else if (!auto) {
      addLog('开发者模式：跳过 IPv6 检测');
    }

    try {
      await api('/proxy/start', { method: 'POST', body: JSON.stringify({ mode: state.mode, campusCidrs: state.cidrs, devMode: state.devMode }) });
    } catch (e) {
      return { ok: false, reason: 'api', message: e.message };
    }
    setRunning(true);
    addLog(auto ? '自动启动免流成功' : '免流模式已启动');
    autoDelayTestAfterStart();
    return { ok: true };
  } finally {
    startFlowBusy = false;
  }
}

// 手动启动失败：按原因给出引导弹窗与日志（自动运行的失败见 autoRunFail）
function reportStartFailure(res) {
  if (res.reason === 'core') {
    showCoreDialog();
    addLog('启动已取消：未检测到运行核心，请先下载', true);
  } else if (res.reason === 'net' || res.reason === 'offline') {
    showNetGateDialog();
    addLog(`启动已取消：当前网络不支持免流${res.detail || ''}`, true);
  } else {
    addLog(`操作失败: ${res.message}`, true);
  }
}

/* ── 自动运行免流 (设置 → 通用) ──────────────────────────────── */

// 开关打开后：软件被打开（helper 首次连上）即自动尝试启动免流。开机自启动
// 常落在网络就绪之前，因此离线 / IPv6 探测失败 / 接入凭据未注册都视为
// “时机未到”，监听 online 事件并按间隔重试；核心缺失、非校园网、启动接口
// 报错等确定性失败才弹出主窗口交给用户处理。
// 每个会话只走一轮：启动成功、失败或用户手动接管（FAB 启动/停止）后不再自动尝试。
const autoRun = { armed: false, done: false, startupHandled: false, tries: 0, offlineTries: 0, regTries: 0, timer: null, lastWhy: '' };
const AUTO_RUN_RETRY_MS = 5000;
const AUTO_RUN_MAX_TRIES = 60;       // 联网但未就绪的重试上限（≈5 分钟，覆盖开机后 IPv6 缓慢获取）
const AUTO_RUN_MAX_OFFLINE = 120;    // 离线等待上限（≈10 分钟，开机自启等网络恢复）
const AUTO_RUN_MAX_REG_TRIES = 3;    // WARP 凭据自动补注册次数

function disarmAutoRun() {
  autoRun.armed = false;
  if (autoRun.timer) {
    clearTimeout(autoRun.timer);
    autoRun.timer = null;
  }
}

function scheduleAutoRun(delay) {
  if (!autoRun.armed) return;
  if (autoRun.timer) clearTimeout(autoRun.timer);
  autoRun.timer = setTimeout(() => {
    autoRun.timer = null;
    runAutoStart().catch((e) => autoRunFail({ reason: 'api', message: e.message }));
  }, delay);
}

// helper 首次连上（即“软件被打开”）时判断一次：开关已开且免流未运行则排队自动启动。
// 本次会话只认这一次（startupHandled）——中途打开开关不立即启动，按需求要等到
// 下次打开软件才生效；helper 晚些才连上也没问题，首次连上时仍会排队。
function maybeArmAutoRun() {
  if (autoRun.startupHandled) return;
  if (!state.helperOnline) return; // helper 未就绪，等下一次连上再判断
  autoRun.startupHandled = true;
  if (!state.autoRunProxy) return;
  if (state.proxyRunning) { autoRun.done = true; return; }
  if (autoRun.armed || autoRun.done) return;
  autoRun.armed = true;
  autoRun.tries = 0;
  autoRun.offlineTries = 0;
  autoRun.regTries = 0;
  autoRun.lastWhy = '';
  addLog('自动运行免流已就绪，稍后尝试启动');
  scheduleAutoRun(1200);
}

function autoRunRetry(why) {
  if (navigator.onLine) {
    autoRun.tries += 1;
    if (autoRun.tries > AUTO_RUN_MAX_TRIES) {
      autoRunFail({ reason: 'timeout', message: `自动启动免流超时：${why}` });
      return;
    }
  } else {
    // 离线等待单独计数（开机自启动常要等网络就绪），online 事件会立即重试
    autoRun.tries = 0;
    autoRun.offlineTries += 1;
    if (autoRun.offlineTries > AUTO_RUN_MAX_OFFLINE) {
      autoRunFail({ reason: 'timeout', message: '自动启动免流超时：网络长时间未恢复' });
      return;
    }
  }
  // 只在原因变化或每 6 次（≈30 秒）记一条，避免刷屏
  if (why !== autoRun.lastWhy || (autoRun.tries > 0 && autoRun.tries % 6 === 0)) {
    autoRun.lastWhy = why;
    addLog(`自动运行免流：${why}，稍后重试`);
  }
  scheduleAutoRun(AUTO_RUN_RETRY_MS);
}

// 确定性失败：记日志、弹出主窗口，并给出能解释原因的界面
async function autoRunFail(res) {
  disarmAutoRun();
  autoRun.done = true;
  if (!state.autoRunProxy) return; // 期间被用户关掉 → 不再打扰
  addLog(`自动启动免流失败：${res.message}`, true);
  try { await tauriInvoke('window_show'); } catch (_) { /* 浏览器预览无宿主 */ }
  if (res.reason === 'core') {
    showCoreDialog();
  } else if (res.reason === 'net') {
    showNetGateDialog();
  } else {
    // 没有专用弹窗：直接展开日志，让用户看到失败原因
    $('#logDrawer')?.classList.add('open');
    $('#scrim')?.classList.add('open');
  }
}

async function runAutoStart() {
  if (!autoRun.armed || !state.autoRunProxy) { disarmAutoRun(); return; }
  if (state.proxyRunning) { disarmAutoRun(); autoRun.done = true; return; }
  if (!state.helperOnline) { autoRunRetry('helper 未连接'); return; }
  if (!navigator.onLine) { autoRunRetry('等待网络恢复'); return; }
  // /proxy/start 依赖 state/warp.json：凭据注册失败（多因网络未就绪）时按重试兜底，
  // 但只自动补注册有限次，避免反复打 Cloudflare
  if (!state.warpRegistered) {
    if (!state.warpRegistering && autoRun.regTries < AUTO_RUN_MAX_REG_TRIES) {
      autoRun.regTries += 1;
      registerWarp(true); // 不阻塞等待，成败交给下一轮
    }
    // 注册在途也计入重试，保证整轮自动启动有上限（不会无限等下去）
    autoRunRetry(state.warpRegistering ? '接入凭据注册中' : '接入凭据未就绪');
    return;
  }
  const res = await startProxyFlow(true);
  if (res.ok) {
    disarmAutoRun();
    autoRun.done = true;
    return;
  }
  if (res.reason === 'offline' || res.reason === 'busy' || res.reason === 'helper') {
    autoRunRetry(res.message);
    return;
  }
  await autoRunFail(res);
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

/* ── Missing core (启动免流前的下载引导) ────────────────────── */

function showCoreDialog() {
  const dl = $('#coreDialogDownload');
  if (dl) { dl.disabled = false; dl.textContent = '下载'; }
  $('#coreDialogScrim')?.classList.add('open');
}

function closeCoreDialog() {
  $('#coreDialogScrim')?.classList.remove('open');
}

/* ── Quit confirm (tray 退出) ──────────────────────────────── */

function openQuitDialog() {
  $('#quitDialogScrim')?.classList.add('open');
}

function closeQuitDialog() {
  $('#quitDialogScrim')?.classList.remove('open');
}

function openDevModeDialog() {
  $('#devModeDialogScrim')?.classList.add('open');
}

function closeDevModeDialog() {
  $('#devModeDialogScrim')?.classList.remove('open');
}

async function applyDevMode(next) {
  const sw = $('#devModeSwitch');
  const previous = state.devMode;
  state.devMode = next;
  if (sw) sw.selected = next;
  const saved = await persistSettings();
  if (!saved) {
    state.devMode = previous;
    if (sw) sw.selected = previous;
    return;
  }
  updateSettingsUI();
  addLog(next ? '开发者模式已开启' : '开发者模式已关闭');
  notifyEffectiveNextStart(saved); // 运行中改动要重启免流才生效
}

// Stop 免流 first, then quit — a failed stop keeps the app open so the
// error stays visible instead of quitting over a broken state.
async function stopAndQuit() {
  closeQuitDialog();
  try {
    await api('/proxy/stop', { method: 'POST' });
    setRunning(false);
    addLog('免流模式已停止');
  } catch (e) {
    addLog(`停止免流失败: ${e.message}`, true);
    return;
  }
  await tauriInvoke('app_quit');
}

function quitDirectly() {
  closeQuitDialog();
  tauriInvoke('app_quit').then(() => {
    if (!(window.__TAURI__?.core || window.__TAURI_INTERNALS__)) {
      addLog('浏览器预览环境无法退出应用', true);
    }
  });
}

// Called by main.rs (webview.eval) when tray 退出 is clicked — works with
// the window hidden. 免流 running → un-hide and ask: mihomo is an
// independent TUN process that keeps接管流量 after the app exits.
async function handleTrayQuit() {
  if (state.proxyRunning) {
    await tauriInvoke('window_show');
    openQuitDialog();
    return;
  }
  quitDirectly();
}
window.handleTrayQuit = handleTrayQuit;

/* ── First-run guide (欢迎引导) ─────────────────────────────── */

const GUIDE_DONE_KEY = 'freev6-guide-done';
const GUIDE_PAGE_COUNT = 2;
// 标题显示在 hero 里，随页切换
const GUIDE_TITLES = ['欢迎使用 FreeV6', '使用说明与反馈'];
let guidePage = 0;

// 老版本（引导功能上线前）每次启动都会写 freev6-theme / freev6-skip-version 等键。
// 本常量在 app.js 解析时求值，早于 init() → loadSavedTheme() 的写入，因此：
//   有旧痕迹 ⇒ 覆盖安装的老用户 ⇒ 不再显示引导（并补写 GUIDE_DONE_KEY）；
//   无痕迹  ⇒ 全新安装 ⇒ 显示引导。升级安装不会重弹。
const HAD_PRIOR_STORAGE = (() => {
  try {
    return ['freev6-theme', 'freev6-skip-version', 'freev6-dynamic-color', 'freev6-pure-black']
      .some((key) => localStorage.getItem(key) !== null);
  } catch (_) { return false; }
})();

function renderGuide() {
  // 横向轨道：每页占轨道 50%，位移 -50% 即前进一页
  const track = $('#guideTrack');
  if (track) track.style.transform = `translateX(-${guidePage * 50}%)`;
  const title = $('#guideHeroTitle');
  if (title) title.textContent = GUIDE_TITLES[guidePage] || GUIDE_TITLES[0];
  $$('#guideDots .guide-dot').forEach((d, i) => d.classList.toggle('active', i === guidePage));
  // 最后一页：翻页按钮变成「开始使用」
  const next = $('#guideNext');
  if (next) next.textContent = guidePage >= GUIDE_PAGE_COUNT - 1 ? '开始使用' : '继续';
}

function openGuide() {
  guidePage = 0;
  renderGuide();
  $('#guideScrim')?.classList.add('open');
}

function closeGuide() {
  $('#guideScrim')?.classList.remove('open');
}

// 翻页；到最后一页再点 → 记录已读并关闭（条款只能通过按钮同意）
function advanceGuide() {
  if (guidePage < GUIDE_PAGE_COUNT - 1) {
    guidePage += 1;
    renderGuide();
    return;
  }
  try { localStorage.setItem(GUIDE_DONE_KEY, '1'); } catch (_) { /* ignore */ }
  closeGuide();
}

function guideSeen() {
  try {
    if (localStorage.getItem(GUIDE_DONE_KEY) === '1') return true;
    // 升级安装（老用户）：直接补写标记，之后统一走 GUIDE_DONE_KEY
    if (HAD_PRIOR_STORAGE) {
      localStorage.setItem(GUIDE_DONE_KEY, '1');
      return true;
    }
  } catch (_) { /* ignore */ }
  return false;
}

// URL 带 ?guide 或 #guide 时强制重看，便于预览/演示
function guideForced() {
  return new URLSearchParams(location.search).has('guide') || location.hash === '#guide';
}

/* ── Update (GitHub release check + in-app install) ──────────── */

const UPDATE_OWNER_REPO = 'evansrrr/free-v6';
const SKIP_VERSION_KEY = 'freev6-skip-version';
const GITHUB_PROXY = 'https://api.gitproxy.dev/';

// 大陆访问加速：请求前部拼接代理前缀；已是代理地址则不重复拼接。
function proxiedGitHub(url) {
  return url.startsWith(GITHUB_PROXY) ? url : GITHUB_PROXY + url;
}

// Version source: the 关于 row (kept in sync with tauri.conf.json / Cargo.toml
// by the release workflow's tag-version guard).
function currentAppVersion() {
  const m = /v(\d+\.\d+\.\d+)/.exec($('#versionValue')?.textContent || '');
  return m ? m[1] : '';
}

function cmpVersion(a, b) {
  const pa = a.split('.').map((n) => parseInt(n, 10) || 0);
  const pb = b.split('.').map((n) => parseInt(n, 10) || 0);
  for (let i = 0; i < 3; i++) {
    if ((pa[i] || 0) !== (pb[i] || 0)) return (pa[i] || 0) - (pb[i] || 0);
  }
  return 0;
}

function renderUpdateBadge() {
  const badge = $('#updateBadge');
  if (!badge) return;
  if (state.updateInfo) {
    badge.textContent = `有新版本 v${state.updateInfo.version}`;
    badge.hidden = false;
    $('#versionItem')?.classList.add('clickable');
  } else {
    badge.hidden = true;
    $('#versionItem')?.classList.remove('clickable');
  }
}

// 版本信息来自 release 里的 version.json（稳定地址 releases/latest/download，
// 只命中正式版），由 helper 代理获取——完全绕开 api.github.com（共享配额会
// 间歇403），且 helper 侧无 CORS 问题。所有失败静默：离线、私有仓库(404)、
// 代理/直连都不可用都不阻塞启动。
async function checkForUpdate(autoOpen = true) {
  try {
    const res = await api('/update/latest');
    if (!res?.update) {
      state.updateInfo = null;
      renderUpdateBadge();
      return null;
    }
    const remote = String(res.version || '').trim();
    const current = currentAppVersion();
    if (!/^\d+\.\d+\.\d+/.test(remote) || !current || cmpVersion(remote, current) <= 0) {
      state.updateInfo = null;
      renderUpdateBadge();
      return null;
    }
    state.updateInfo = {
      version: remote,
      url: res.download,
      sha: /^[0-9a-f]{64}$/i.test(res.sha256 || '') ? res.sha256 : '',
      notes: res.notes || '',
      htmlUrl: `https://github.com/${UPDATE_OWNER_REPO}/releases/tag/${encodeURIComponent(res.tag || `v${remote}`)}`,
    };
    renderUpdateBadge();
    if (autoOpen && localStorage.getItem(SKIP_VERSION_KEY) !== remote) openUpdateDialog();
    return state.updateInfo;
  } catch (_) {
    return null;
  }
}

function updateBusy() {
  return ['downloading', 'stopping', 'applying'].includes(state.updatePhase);
}

function openUpdateDialog() {
  const info = state.updateInfo;
  if (!info || updateBusy()) return;
  $('#updateTitle').textContent = `发现新版本 v${info.version}`;
  $('#updateCurrent').textContent = `当前版本 v${currentAppVersion() || '-'} → v${info.version}`;
  const section = (info.notes.split(/\n---\n/)[0] || '')
    .replace(/^##\s*\[[^\]]+\][^\n]*\n?/, '')
    .trim();
  $('#updateNotes').textContent = section || '打开 Release 页面查看更新内容。';
  $('#updateStopHint').hidden = !state.proxyRunning;
  setUpdatePhase('ready');
  $('#updateDialogScrim').classList.add('open');
}

function closeUpdateDialog() {
  if (updateBusy()) return; // 下载/安装进行中不允许关闭
  state.updatePhase = null;
  $('#updateDialogScrim').classList.remove('open');
}

function setUpdatePhase(phase) {
  state.updatePhase = phase === 'ready' ? null : phase;
  $('#updateActions').hidden = phase !== 'ready';
  $('#updateErrorActions').hidden = phase !== 'error';
  $('#updateProgress').hidden = phase !== 'downloading';
  $('#updateStatus').hidden = phase === 'ready';
}

function setUpdateStatus(text) {
  const el = $('#updateStatus');
  if (el) el.textContent = text;
}

function updateFailed(msg) {
  setUpdatePhase('error');
  setUpdateStatus(msg);
  addLog(`更新失败: ${msg}`, true);
}

// Poll the helper's download progress while the request runs in background.
async function pollUpdateProgress() {
  for (let i = 0; i < 2000; i++) { // up to ~10 min
    await new Promise((r) => setTimeout(r, 300));
    let p;
    try {
      p = await api('/update/progress');
    } catch (_) {
      continue;
    }
    if (p.state === 'downloading') {
      const done = p.done || 0;
      const total = p.total > 0 ? p.total : 0;
      if (total > 0) {
        const pct = Math.min(100, Math.floor((done * 100) / total));
        const spin = $('#updateSpinner');
        if (spin) spin.value = pct;
        $('#updatePercentText').textContent = `${pct}%`;
      } else {
        $('#updatePercentText').textContent = `${(done / 1048576).toFixed(1)} MB`;
      }
      continue;
    }
    if (p.state === 'done' && p.path) return p;
    state.updateError = p.state === 'error' ? (p.error || '下载失败') : '下载状态异常';
    return null;
  }
  state.updateError = '下载超时';
  return null;
}

// 立即更新：下载 → (自动)停止兔流 → 干净退出并交给静默安装器。
async function startUpdate() {
  const info = state.updateInfo;
  if (!info || updateBusy()) return;
  if (!state.helperOnline) {
    updateFailed('helper 未连接，无法更新');
    return;
  }
  setUpdatePhase('downloading');
  setUpdateStatus(info.sha ? '正在下载安装包…' : '正在下载安装包…（Release 未附 SHA256，跳过校验）');
  let prog;
  try {
    const ack = await api('/update/download', {
      method: 'PUT',
      body: JSON.stringify({ url: proxiedGitHub(info.url), sha256: info.sha }),
    });
    if (!ack?.ok) throw new Error(ack?.error || '下载任务创建失败');
    prog = await pollUpdateProgress();
  } catch (e) {
    updateFailed(`下载失败: ${e.message}`);
    return;
  }
  if (!prog) {
    updateFailed(state.updateError || '下载失败');
    return;
  }
  if (state.proxyRunning) {
    setUpdatePhase('stopping');
    setUpdateStatus('正在停止免流…');
    try {
      await api('/proxy/stop', { method: 'POST' });
      setRunning(false);
    } catch (e) {
      updateFailed(`停止免流失败: ${e.message}，更新已中止`);
      return;
    }
  }
  setUpdatePhase('applying');
  setUpdateStatus('正在安装新版本并重启…');
  let applied;
  try {
    applied = await tauriInvoke('update_apply', { installer: prog.path });
  } catch (e) {
    updateFailed(`启动安装器失败: ${e.message}`);
    return;
  }
  if (applied !== 'ok') {
    updateFailed('浏览器预览环境无法安装更新');
    return;
  }
  addLog(`更新到 v${info.version}：安装器已启动，应用即将退出并重启`);
}

/* ── Mode Selector ────────────────────────────────────────────── */

function setMode(mode, persist = true, silent = false) {
  if (mode === state.mode && silent) return;
  const changed = mode !== state.mode;
  state.mode = mode;
  // Sync settings page segmented chips (md-filter-chip)
  $$('#settingsModeGroup .setting-seg').forEach(seg => {
    seg.selected = seg.dataset.mode === mode;
  });
  if (!silent) addLog(`切换为${MODE_LABELS[mode] || mode}模式`);
  // 运行中切换模式要重启免流才生效 → 保存成功后轻提醒
  if (persist) persistSettings().then(saved => { if (changed) notifyEffectiveNextStart(saved); });
}

function persistSettings() {
  if (!state.helperOnline) {
    addLog('helper 未连接，设置未保存', true);
    return Promise.resolve(false);
  }
  return api('/settings', { method: 'PUT', body: JSON.stringify({ mode: state.mode, campusCidrs: state.cidrs, devMode: state.devMode, autoStart: state.autoStart, silentStart: state.silentStart, autoRunProxy: state.autoRunProxy, hotkeyEnabled: state.hotkeyEnabled }) })
    .then(() => { addLog('设置已保存'); return true; })
    .catch(e => { addLog(`保存设置失败: ${e.message}`, true); return false; });
}

/* ── Settings save feedback (toast) ─────────────────────────── */

let toastTimer = null;

function showToast(message) {
  const el = $('#toast');
  const text = $('#toastText');
  if (!el || !text) return;
  text.textContent = message;
  el.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.remove('show'), 3000);
}

// 免流运行中，模式 / 绕过白名单 / 开发者模式的改动要重启免流才生效；
// 保存成功后弹轻提醒。未在运行时这些设置本就即时生效，不打扰。
function notifyEffectiveNextStart(saved) {
  if (saved && state.proxyRunning) showToast('已保存，将在下次启动免流时生效');
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

/* ── Persistent Traffic Stats + detail hint card ─────────────── */
// 本月/累计流量与上面的会话累计同源（pollTraffic 的 delta），存 localStorage
// 跨会话保留；只在本地计算与展示，不上传（见 docs/privacy.md）。

const TRAFFIC_STATS_KEY = 'freev6-traffic-stats';
const TRAFFIC_FLUSH_MS = 3000; // 每秒都有增量：合并写入，避免高频落盘

const trafficStats = { total: { up: 0, down: 0 }, months: {}, dirty: false, lastFlush: 0 };

function monthKey(d = new Date()) {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`;
}

function loadTrafficStats() {
  try {
    const raw = localStorage.getItem(TRAFFIC_STATS_KEY);
    if (!raw) return;
    const data = JSON.parse(raw);
    const num = v => (typeof v === 'number' && isFinite(v) && v > 0 ? v : 0);
    if (data?.total) trafficStats.total = { up: num(data.total.up), down: num(data.total.down) };
    if (data?.months && typeof data.months === 'object') {
      const months = {};
      for (const [key, val] of Object.entries(data.months)) {
        if (/^\d{4}-(0[1-9]|1[0-2])$/.test(key) && val) months[key] = { up: num(val.up), down: num(val.down) };
      }
      trafficStats.months = months;
    }
  } catch (_) { /* 数据损坏 → 从零重新累计 */ }
}

function addTrafficStats(up, down) {
  if (up <= 0 && down <= 0) return;
  trafficStats.total.up += up;
  trafficStats.total.down += down;
  const key = monthKey();
  const month = trafficStats.months[key] || (trafficStats.months[key] = { up: 0, down: 0 });
  month.up += up;
  month.down += down;
  trafficStats.dirty = true;
}

function flushTrafficStats(force = false) {
  if (!trafficStats.dirty) return;
  const now = Date.now();
  if (!force && now - trafficStats.lastFlush < TRAFFIC_FLUSH_MS) return;
  try {
    localStorage.setItem(TRAFFIC_STATS_KEY, JSON.stringify({ total: trafficStats.total, months: trafficStats.months }));
    trafficStats.dirty = false;
    trafficStats.lastFlush = now;
  } catch (_) { /* 存储不可用：内存继续累计，下次再试 */ }
}

function renderTrafficHint() {
  const hint = $('#trafficHint');
  if (!hint || hint.hidden) return;
  const key = monthKey();
  const month = trafficStats.months[key] || { up: 0, down: 0 };
  $('#hintMonthKey').textContent = `${key.slice(0, 4)}年${parseInt(key.slice(5), 10)}月`;
  $('#hintMonthUp').textContent = formatBytes(month.up);
  $('#hintMonthDown').textContent = formatBytes(month.down);
  $('#hintMonthSum').textContent = formatBytes(month.up + month.down);
  $('#hintTotalUp').textContent = formatBytes(trafficStats.total.up);
  $('#hintTotalDown').textContent = formatBytes(trafficStats.total.down);
  $('#hintTotalSum').textContent = formatBytes(trafficStats.total.up + trafficStats.total.down);
}

// 卡片下方空间不足（窗口较矮）时翻转到卡片上方；打开时与窗口 resize 时都会跑
function positionTrafficHint() {
  const hint = $('#trafficHint');
  const card = $('#statsCard');
  if (!hint || hint.hidden || !card) return;
  hint.classList.remove('above');
  if (card.getBoundingClientRect().bottom + hint.offsetHeight + 10 > window.innerHeight) {
    hint.classList.add('above');
  }
}

function openTrafficHint() {
  const hint = $('#trafficHint');
  const card = $('#statsCard');
  if (!hint || !card) return;
  hint.hidden = false;
  positionTrafficHint();
  card.classList.add('open');
  card.setAttribute('aria-expanded', 'true');
  renderTrafficHint();
}

function closeTrafficHint() {
  const hint = $('#trafficHint');
  if (!hint || hint.hidden) return;
  hint.hidden = true;
  const card = $('#statsCard');
  card?.classList.remove('open');
  card?.setAttribute('aria-expanded', 'false');
}

function toggleTrafficHint() {
  const hint = $('#trafficHint');
  if (!hint) return;
  if (hint.hidden) openTrafficHint(); else closeTrafficHint();
}

/* ── mihomo core download (设置页核心行 + 启动拦截弹窗共用) ─── */

// 下载核心；btn 传入时显示「下载中…」进度，成功返回 true。
async function downloadCore(btn) {
  if (!state.helperOnline) { addLog('helper 未连接', true); return false; }
  if (btn) { btn.disabled = true; btn.textContent = '下载中…'; }
  try {
    const result = await api('/runtime/download', { method: 'POST' });
    state.runtimePresent = true;
    state.runtimeChecked = true;
    addLog(`mihomo 核心下载完成${result.version ? `: ${result.version}` : ''}`);
    return true;
  } catch (e) {
    addLog(`下载失败: ${e.message}`, true);
    return false;
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = '下载'; }
    updateSettingsUI();
    updateConnectionChip(state.helperOnline);
  }
}

/* ── WARP registration (手动按钮 + 首次使用自动注册) ────────── */

async function registerWarp(isAuto) {
  if (!state.helperOnline) { addLog('helper 未连接', true); return false; }
  if (state.warpRegistering) return false;   // 自动/手动撞车时不重复请求
  state.warpRegistering = true;
  try {
    await api('/warp/register', { method: 'POST', body: JSON.stringify({ name: 'freev6-windows' }) });
    state.warpRegistered = true;
    updateSettingsUI();
    addLog(isAuto ? '首次使用：已自动注册 WARP' : 'WARP 注册成功');
    return true;
  } catch (e) {
    addLog(`${isAuto ? '自动注册 WARP 失败' : 'WARP 注册失败'}: ${e.message}`, true);
    return false;
  } finally {
    state.warpRegistering = false;
  }
}

// 第一次使用且 helper 报告未注册 WARP → 自动注册一次。每次启动最多尝试一次，
// 失败交给「注册」按钮或下次启动，避免轮询反复请求 Cloudflare。
function maybeAutoRegisterWarp() {
  if (state.warpRegistered || state.warpAutoTried) return;
  state.warpAutoTried = true;
  addLog('未注册 WARP，正在自动注册…');
  registerWarp(true);
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
    if (typeof status.settings?.silentStart === 'boolean') state.silentStart = status.settings.silentStart;
    if (typeof status.settings?.autoRunProxy === 'boolean') state.autoRunProxy = status.settings.autoRunProxy;
    if (typeof status.settings?.hotkeyEnabled === 'boolean') state.hotkeyEnabled = status.settings.hotkeyEnabled;

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
    // 首次拉到状态：未注册则自动注册（每会话仅一次）
    maybeAutoRegisterWarp();
    // 设置开启时：软件被打开后自动尝试启动免流
    maybeArmAutoRun();

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
      // 同一份增量同时计入本月 / 累计（跨会话持久化）
      addTrafficStats(deltaUp, deltaDown);
    }

    state.lastUpload = upTotal;
    state.lastDownload = downTotal;

    // Update speed display
    $('#speedUp').textContent = formatSpeed(deltaUp);
    $('#speedDown').textContent = formatSpeed(deltaDown);

    // Update stats
    updateStatsRing();
    flushTrafficStats();
    renderTrafficHint();

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
      // 首次连上：同步 WARP 注册状态，未注册则自动注册一次
      state.warpRegistered = Boolean(status.warp?.registered);
      updateSettingsUI();
      maybeAutoRegisterWarp();
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
    const nextSilentStart = typeof status.settings?.silentStart === 'boolean' ? status.settings.silentStart : state.silentStart;
    const nextAutoRunProxy = typeof status.settings?.autoRunProxy === 'boolean' ? status.settings.autoRunProxy : state.autoRunProxy;
    const nextHotkeyEnabled = typeof status.settings?.hotkeyEnabled === 'boolean' ? status.settings.hotkeyEnabled : state.hotkeyEnabled;
    if (nextDevMode !== state.devMode || nextAutoStart !== state.autoStart || nextSilentStart !== state.silentStart || nextAutoRunProxy !== state.autoRunProxy || nextHotkeyEnabled !== state.hotkeyEnabled) {
      state.devMode = nextDevMode;
      state.autoStart = nextAutoStart;
      state.silentStart = nextSilentStart;
      state.autoRunProxy = nextAutoRunProxy;
      state.hotkeyEnabled = nextHotkeyEnabled;
      updateSettingsUI();
    }
    // 状态已同步：helper 连上且设置开启时排队一次自动启动（幂等）
    maybeArmAutoRun();
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
        // 手动接管：本会话不再自动启动
        autoRun.done = true;
        disarmAutoRun();
      } else {
        // 手动接管：取消排队中的自动启动，改由本次点击决定结果
        autoRun.done = true;
        disarmAutoRun();
        const res = await startProxyFlow(false);
        if (!res.ok) reportStartFailure(res);
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

  // Missing-core dialog: 下载 / 取消 / 遮罩 / Escape（下载成功后自动关闭）
  $('#coreDialogDownload')?.addEventListener('click', async () => {
    const ok = await downloadCore($('#coreDialogDownload'));
    if (ok) closeCoreDialog();
  });
  $('#coreDialogCancel')?.addEventListener('click', closeCoreDialog);
  $('#coreDialogScrim')?.addEventListener('click', (e) => {
    if (e.target?.id === 'coreDialogScrim') closeCoreDialog();
  });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') closeCoreDialog();
  });

  // Quit confirm dialog: act via buttons, dismiss via backdrop or Escape
  $('#quitDialogCancel')?.addEventListener('click', closeQuitDialog);
  $('#quitDialogStopQuit')?.addEventListener('click', stopAndQuit);
  $('#quitDialogScrim')?.addEventListener('click', (e) => {
    if (e.target?.id === 'quitDialogScrim') closeQuitDialog();
  });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') closeQuitDialog();
  });

  // Developer mode confirm
  $('#devModeDialogCancel')?.addEventListener('click', closeDevModeDialog);
  $('#devModeDialogConfirm')?.addEventListener('click', () => {
    closeDevModeDialog();
    applyDevMode(true);
  });
  $('#devModeDialogScrim')?.addEventListener('click', (e) => {
    if (e.target?.id === 'devModeDialogScrim') closeDevModeDialog();
  });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') closeDevModeDialog();
  });

  // First-run guide: 翻页按钮 / 页码点；只能点「开始使用」关闭
  // （不响应 Escape 与遮罩点击 —— 继续使用即代表同意条款）
  $('#guideNext')?.addEventListener('click', advanceGuide);
  $$('#guideDots .guide-dot').forEach((dot) => {
    dot.addEventListener('click', () => {
      guidePage = parseInt(dot.dataset.guideDot, 10) || 0;
      renderGuide();
    });
  });

  // Update dialog: actions + backdrop/Escape (busy phases are blocked
  // inside closeUpdateDialog)
  $('#updateLater')?.addEventListener('click', closeUpdateDialog);
  $('#updateSkip')?.addEventListener('click', () => {
    const v = state.updateInfo?.version;
    if (v) {
      try { localStorage.setItem(SKIP_VERSION_KEY, v); } catch (_) { /* ignore */ }
      addLog(`已跳过版本 v${v}（设置页版本行仍可查看并手动更新）`);
    }
    closeUpdateDialog();
  });
  $('#updateNow')?.addEventListener('click', startUpdate);
  $('#updateClose')?.addEventListener('click', closeUpdateDialog);
  $('#updateOpenPage')?.addEventListener('click', () => {
    if (state.updateInfo) openExternal(state.updateInfo.htmlUrl);
  });
  $('#updateDialogScrim')?.addEventListener('click', (e) => {
    if (e.target?.id === 'updateDialogScrim') closeUpdateDialog();
  });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') closeUpdateDialog();
  });
  $('#closeLogs')?.addEventListener('click', closeLogs);
  $('#scrim')?.addEventListener('click', closeLogs);

  // Refresh
  $('#refreshButton')?.addEventListener('click', () => {
    addLog('手动刷新');
    refreshBackendState();
    fetchProxies();
  });

  // 流量统计卡：点击 / 回车弹出「本月 + 累计」详情提示卡
  // 点到提示卡内部不算切换（否则点内容会把它关掉），交给外部点击/Esc 关闭
  $('#statsCard')?.addEventListener('click', (e) => {
    if (e.target?.closest?.('#trafficHint')) return;
    toggleTrafficHint();
  });
  $('#statsCard')?.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      toggleTrafficHint();
    }
  });
  // 点击提示卡以外区域 / Esc 关闭
  document.addEventListener('click', (e) => {
    if (!e.target?.closest?.('#statsCard')) closeTrafficHint();
  });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') closeTrafficHint();
  });
  // 提示卡打开期间调整窗口 → 重新判断上/下翻转
  window.addEventListener('resize', positionTrafficHint);
  // 离开页面或切到后台时，把未落盘的统计强制写入 localStorage
  window.addEventListener('pagehide', () => flushTrafficStats(true));
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'hidden') flushTrafficStats(true);
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
      persistSettings().then(notifyEffectiveNextStart);
      addLog(`移除条目: ${removed}`);
    });
  });
}

function updateSettingsUI() {
  // Mode segmented chips (md-filter-chip)
  $$('#settingsModeGroup .setting-seg').forEach(seg => {
    seg.selected = seg.dataset.mode === state.mode;
  });
  // 直连 option only exists in developer mode
  $('#settingsModeGroup')?.classList.toggle('dev-mode', state.devMode);

  // CIDR list, developer-mode + auto-start switches
  renderCidrs();
  const devSwitch = $('#devModeSwitch');
  if (devSwitch) devSwitch.selected = state.devMode;
  const autoSwitch = $('#autoStartSwitch');
  if (autoSwitch) autoSwitch.selected = state.autoStart;
  const silentSwitch = $('#silentStartSwitch');
  if (silentSwitch) silentSwitch.selected = state.silentStart;
  const autoRunSwitch = $('#autoRunSwitch');
  if (autoRunSwitch) autoRunSwitch.selected = state.autoRunProxy;
  const hotkeySwitch = $('#hotkeySwitch');
  if (hotkeySwitch) hotkeySwitch.selected = state.hotkeyEnabled;
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

  // WARP: the register button doubles as 重新注册 once registered
  const warpBtn = $('#registerWarp');
  if (warpBtn) warpBtn.textContent = state.warpRegistered ? '重新注册' : '注册';
}

function wireSettingsEvents() {
  // Theme select（切换走色彩渐变过渡）
  $('#themeSelect')?.addEventListener('change', (e) => {
    const theme = e.target.value;
    withColorTransition(() => applyTheme(theme));
    addLog(`切换主题: ${theme}`);
  });

  // GitHub project — opens in the system browser
  $('#githubItem')?.addEventListener('click', () => {
    openExternal('https://github.com/evansrrr/free-v6');
  });
  $('#feedbackItem')?.addEventListener('click', () => {
    openExternal('https://github.com/evansrrr/free-v6/issues');
  });
  // 使用说明: README.md ships with the installer (bundle.resources maps
  // ../README.md -> README.md under resource_dir); the host command resolves
  // the install dir. A standalone browser falls back to the repository page.
  $('#readmeItem')?.addEventListener('click', async () => {
    if (await invokeHost('open_readme')) return;
    window.open('https://github.com/evansrrr/free-v6#readme', '_blank', 'noopener,noreferrer');
  });

  // 软件版本行：发现新版时显示“有新版本”角标，点击重新打开更新弹窗
  // （即使用户点过“跳过此版本”，这里始终可进入）
  $('#versionItem')?.addEventListener('click', () => {
    if (state.updateInfo) openUpdateDialog();
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

  // Developer mode — 打开前必须弹窗确认（取消/遮罩/Esc → 保持关闭）；
  // 关闭直接生效。落库与失败拨回统一走 applyDevMode。
  $('#devModeSwitch')?.addEventListener('change', (e) => {
    const next = e.target.selected;
    if (next === state.devMode) return; // 程序化复位会再次触发 change，忽略
    if (next) {
      e.target.selected = false; // 先拨回，确认通过后才真正开启
      openDevModeDialog();
      return;
    }
    applyDevMode(false);
  });

  // Auto-start with Windows — helper registers the ONLOGON task plus the
  // Task Manager marker entry (tray-only launch). Same contract as developer
  // mode: announce only after the helper confirmed, revert on failure.
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

  // Silent start — with it on, every launch (autostart or manual) skips the
  // window and only shows the tray icon. Same revert-on-failure contract.
  $('#silentStartSwitch')?.addEventListener('change', async (e) => {
    const next = e.target.selected;
    const previous = state.silentStart;
    state.silentStart = next;
    const saved = await persistSettings();
    if (!saved) {
      state.silentStart = previous;
      e.target.selected = previous;
      return;
    }
    addLog(next ? '已开启静默启动（开机自启与手动打开均仅驻留托盘）' : '已关闭静默启动');
  });

  // 自动运行免流 —— 打开软件后自动尝试启动；网络未就绪时等待合适时机，
  // 确定性失败则自动弹出主窗口。与静默启动同样的回滚约定。
  $('#autoRunSwitch')?.addEventListener('change', async (e) => {
    const next = e.target.selected;
    const previous = state.autoRunProxy;
    state.autoRunProxy = next;
    const saved = await persistSettings();
    if (!saved) {
      state.autoRunProxy = previous;
      e.target.selected = previous;
      return;
    }
    if (next) {
      // 只保存设置，不立即启动：自动启动按需求发生在“软件被打开”时
      addLog('已开启自动运行免流（下次打开软件时自动尝试启动）');
      autoRun.done = false;
    } else {
      addLog('已关闭自动运行免流');
      disarmAutoRun();
    }
  });

  // 快捷键 —— 启用后按 F6 打开主窗口，即使窗口正驻留托盘/静默启动。
  // 先让桌面壳注册/注销系统级热键（F6 被占用会失败），成功后再落盘；
  // 注册失败则回滚开关，UI 不声称一个没生效的状态。
  $('#hotkeySwitch')?.addEventListener('change', async (e) => {
    const next = e.target.selected;
    const previous = state.hotkeyEnabled;
    state.hotkeyEnabled = next;
    try {
      await tauriInvoke('hotkey_set', { enabled: next });
    } catch (err) {
      state.hotkeyEnabled = previous;
      e.target.selected = previous;
      addLog(`快捷键设置失败: ${err}`, true);
      return;
    }
    const saved = await persistSettings();
    if (!saved) {
      // 落盘失败 → 回滚热键注册与开关状态
      state.hotkeyEnabled = previous;
      e.target.selected = previous;
      tauriInvoke('hotkey_set', { enabled: previous });
      return;
    }
    addLog(next ? '已开启快捷键（按 F6 打开主窗口）' : '已关闭快捷键');
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
      withColorTransition(() => applyDynamicPalette());
      addLog('动态取色已关闭');
    }
  });

  // Pure black background (OLED) — surfaces go #000, dark themes only（渐变过渡）
  $('#pureBlackSwitch')?.addEventListener('change', (e) => {
    state.pureBlack = e.target.selected;
    try { localStorage.setItem('freev6-pure-black', state.pureBlack ? '1' : '0'); } catch (_) {}
    withColorTransition(() => applyPureBlack(state.pureBlack));
    addLog(state.pureBlack ? '纯黑背景已开启（深色主题下生效）' : '纯黑背景已关闭');
  });

  // Download core（设置页「核心」行，与启动拦截弹窗共用 downloadCore）
  $('#downloadCore')?.addEventListener('click', () => { downloadCore($('#downloadCore')); });

  // Register WARP（手动；首次使用的自动注册见 maybeAutoRegisterWarp）
  $('#registerWarp')?.addEventListener('click', () => { registerWarp(false); });
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
    persistSettings().then(notifyEffectiveNextStart);
    addLog(`添加条目: ${value}`);
  }
  input.value = '';
}

/* ── 色彩切换过渡（主题 / 动态取色 / 纯黑背景） ────────────── */

// View Transitions：旧画面保持不动、新画面淡入，两套颜色在屏上混合渐变，
// 而不是属性一改整屏硬切。连续切换时先 skip 上一个过渡避免排队；
// 不支持 startViewTransition 的环境（老 WebView）退化为直接切换。
let _colorVT = null;
function withColorTransition(fn) {
  if (typeof document.startViewTransition !== 'function') { fn(); return null; }
  if (_colorVT) { try { _colorVT.skipTransition(); } catch (_) {} _colorVT = null; }
  try {
    const t = document.startViewTransition(fn);
    _colorVT = t;
    // ready/finished 都可能因被 skip 而 reject（快速连续切换）——
    // 必须各自接住，否则会冒成 unhandledrejection
    t.ready?.catch(() => {});
    t.finished.then(() => { if (_colorVT === t) _colorVT = null; }, () => {});
    return t;
  } catch (_) { fn(); return null; }
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
        // （跟随时也走渐变，避免系统切换时硬跳）
        withColorTransition(() => applyDynamicPalette());
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
    // 后台静默刷新（初始化/断线重连）不动画；用户打开开关时走渐变过渡
    if (silent) applyDynamicPalette();
    else withColorTransition(() => applyDynamicPalette());
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
  loadTrafficStats();
  wireTitlebar();
  wireEvents();
  wireProxyEvents();
  wireSettingsEvents();
  renderLogs();
  initChart();
  refreshBackendState();

  // 后台检查 GitHub 正式版更新（离线/私有仓库 404 → 静默跳过，不阻塞启动）
  setTimeout(() => { checkForUpdate(); }, 1500);

  // Show proxy FAB on initial dashboard view
  $('#proxyToggle')?.classList.add('visible');

  // 首次启动显示欢迎引导（已同意过，或未带强制参数时不显示）
  if (guideForced() || !guideSeen()) setTimeout(openGuide, 400);

  setInterval(pollStatus, 3000);
  setInterval(pollTraffic, 1000);
  setInterval(fetchProxies, 5000);

  // 自动运行免流：网络恢复后立即重试（离线期间按间隔静默等待）
  window.addEventListener('online', () => {
    if (!autoRun.armed) return;
    autoRun.tries = 0;
    autoRun.offlineTries = 0;
    autoRun.lastWhy = '';
    addLog('网络已恢复，重新尝试自动启动免流');
    scheduleAutoRun(600);
  });
}

document.addEventListener('DOMContentLoaded', init);
