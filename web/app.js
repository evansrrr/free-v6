const state = {
  activeView: 'overview',
  running: false,
  mode: 'rule',
  cidrs: [],
  logs: ['等待 helper 连接...'],
  helperOnline: false,
  egress: '',
};

const API_BASE = 'http://127.0.0.1:13335/api/v1';

const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => [...document.querySelectorAll(selector)];

function addLog(message) {
  const now = new Date().toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' });
  state.logs.unshift(`${now}  ${message}`);
  state.logs = state.logs.slice(0, 30);
  renderLogs();
}

function showView(view) {
  state.activeView = view;
  $$('.nav-item[data-view]').forEach((item) => item.classList.toggle('active', item.dataset.view === view));
  $$('.view').forEach((item) => item.classList.toggle('active', item.id === `${view}View`));
  $('#pageTitle').textContent = { overview: '总览', settings: '设置', runtime: '核心' }[view];
}

function renderCidrs() {
  $('#cidrCount').textContent = state.cidrs.length;
  $('#cidrValue').textContent = state.cidrs.length ? `${state.cidrs.length} 个自定义网段` : '未添加网段';
  $('#cidrList').innerHTML = state.cidrs.length ? state.cidrs.map((cidr, index) => `<span class="chip">${cidr}<button type="button" data-remove-cidr="${index}" aria-label="删除 ${cidr}">×</button></span>`).join('') : '<span class="empty-state">还没有自定义网段</span>';
}

function persistSettings() {
  if (!state.helperOnline) return;
  api('/settings', { method: 'PUT', body: JSON.stringify({ mode: state.mode, campusCidrs: state.cidrs }) })
    .then(() => addLog('设置已保存'))
    .catch((error) => addLog(`保存设置失败: ${error.message}`));
}

function setMode(mode, persist = true) {
  state.mode = mode;
  const isRule = mode === 'rule';
  $$('.segment').forEach((item) => item.classList.toggle('active', item.dataset.mode === mode));
  $('#modeValue').textContent = isRule ? '规则模式' : '全局模式';
  $('#modeButton').textContent = isRule ? '规则模式' : '全局模式';
  $('#modeHelp').textContent = isRule ? '校园网网段直连，其他流量全部进入 WARP。适合日常使用。' : '所有非局域网流量进入 WARP，适合需要完整接管的场景。';
  addLog(`切换为${isRule ? '规则' : '全局'}模式`);
  if (persist) persistSettings();
}

function setRunning(running, egress = '') {
  state.running = running;
  state.egress = egress;
  const button = $('#proxyToggle');
  $('#proxyHeadline').textContent = running ? '免流已开启' : '准备就绪';
  $('#proxyDescription').textContent = running ? 'mihomo 正在接管非校园网流量，连接经过 WARP IPv6 隧道。' : '启动 mihomo 后，所有非校园网流量将通过 WARP IPv6 隧道。';
  button.innerHTML = running ? '<span class="button-symbol">■</span><span>停止免流</span>' : '<span class="button-symbol">▶</span><span>启动免流</span>';
  $('#orbitalStatus').classList.toggle('live', running);
  $('#orbitalStatus .orbital-core').textContent = running ? 'ON' : 'OFF';
  $('#headerStatus').textContent = running ? '免流运行中' : '核心未连接';
  $('#headerDot').className = `status-dot ${running ? 'live' : 'muted'}`;
  $('#networkPill').textContent = running ? '已连接' : '待检测';
  $('#networkPill').className = `status-pill ${running ? 'success' : 'warning'}`;
  $('#egressValue').textContent = running ? (egress || 'IPv6 已连接') : '未检测';
  $('#nodeValue').textContent = running ? '自动选择' : '未选择';
  $('#tunValue').textContent = running ? '已启用' : '未启动';
  $('.quality-bar span').style.width = running ? '86%' : '24%';
  $('.quality-bar span').style.background = running ? 'var(--green)' : 'var(--orange)';
}

async function api(path, options = {}) {
  const response = await fetch(`${API_BASE}${path}`, {
    ...options,
    headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
  });
  const payload = await response.json();
  if (!response.ok) throw new Error(payload.error || `HTTP ${response.status}`);
  return payload;
}

function setHelperOffline(message = 'helper 未连接') {
  state.helperOnline = false;
  $('#headerStatus').textContent = message;
  $('#headerDot').className = 'status-dot muted';
  $('#runtimeDescription').textContent = '启动 helper 后检查运行时';
  $('#adminCheckText').textContent = '等待 helper';
  $('#warpCheckText').textContent = '等待 helper';
}

async function refreshBackendState() {
  try {
    const [status, runtime] = await Promise.all([api('/status'), api('/runtime')]);
    state.helperOnline = true;
    state.mode = status.settings?.mode || state.mode;
    state.cidrs = status.settings?.campusCidrs || state.cidrs;
    renderCidrs();
    setMode(state.mode, false);
    setRunning(Boolean(status.proxy?.running));
    $('#adminCheckIcon').textContent = status.admin ? '✓' : '!';
    $('#adminCheckIcon').className = `check-icon ${status.admin ? 'ready' : 'pending'}`;
    $('#adminCheckText').textContent = status.admin ? '已获得管理员权限' : '请重新以管理员启动 GUI';
    $('#warpCheckIcon').textContent = status.warp?.registered ? '✓' : '!';
    $('#warpCheckIcon').className = `check-icon ${status.warp?.registered ? 'ready' : 'pending'}`;
    $('#warpCheckText').textContent = status.warp?.registered ? '设备已注册' : '请在核心页注册 WARP';
    $('#runtimeValue').textContent = runtime.present ? '核心已就绪' : '需要下载核心';
    $('#runtimeDescription').textContent = runtime.present ? 'mihomo Alpha 已找到' : '安装目录中未找到核心';
    $('#runtimeName').textContent = runtime.present ? 'mihomo Alpha 已就绪' : '未发现 mihomo 核心';
    $('#runtimePill').textContent = runtime.present ? '已安装' : '需要下载';
    $('#runtimePill').className = `status-pill ${runtime.present ? 'success' : 'warning'}`;
    addLog('已连接 freev6 helper');
  } catch (error) {
    setHelperOffline();
    addLog(`helper 不可用: ${error.message}`);
  }
}

function addCidr() {
  const input = $('#cidrInput');
  const value = input.value.trim();
  if (!value) return;
  if (!value.includes('/')) {
    addLog(`拒绝无效网段: ${value}`);
    input.setCustomValidity('请输入 CIDR，例如 10.0.0.0/8');
    input.reportValidity();
    return;
  }
  if (!state.cidrs.includes(value)) state.cidrs.push(value);
  input.value = '';
  input.setCustomValidity('');
  renderCidrs();
  addLog(`添加校园网段: ${value}`);
  persistSettings();
}

function renderLogs() {
  $('#logList').innerHTML = state.logs.map((entry) => {
    const split = entry.indexOf('  ');
    const time = split > -1 ? entry.slice(0, split) : '现在';
    const message = split > -1 ? entry.slice(split + 2) : entry;
    return `<div class="log-entry"><time>${time}</time><span>${message}</span></div>`;
  }).join('');
}

$$('.nav-item[data-view]').forEach((item) => item.addEventListener('click', () => showView(item.dataset.view)));
$$('[data-view-target]').forEach((item) => item.addEventListener('click', () => showView(item.dataset.viewTarget)));
$$('.segment').forEach((item) => item.addEventListener('click', () => setMode(item.dataset.mode)));
$('#modeButton').addEventListener('click', () => setMode(state.mode === 'rule' ? 'global' : 'rule'));
$('#proxyToggle').addEventListener('click', async () => {
  if (!state.helperOnline) {
    addLog('请求启动/停止免流失败：helper 未连接');
    return;
  }
  const button = $('#proxyToggle');
  button.disabled = true;
  try {
    if (state.running) {
      await api('/proxy/stop', { method: 'POST' });
      setRunning(false);
      addLog('免流模式已停止');
    } else {
      const result = await api('/proxy/start', { method: 'POST', body: JSON.stringify({ mode: state.mode, campusCidrs: state.cidrs, egressUrl: 'https://api64.ipify.org' }) });
      setRunning(true, result.egress);
      addLog('免流模式已启动');
    }
  } catch (error) {
    addLog(`免流操作失败: ${error.message}`);
  } finally {
    button.disabled = false;
  }
});
$('#addCidr').addEventListener('click', addCidr);
$('#cidrInput').addEventListener('keydown', (event) => { if (event.key === 'Enter') addCidr(); });
$('#cidrList').addEventListener('click', (event) => {
  const index = event.target.dataset.removeCidr;
  if (index === undefined) return;
  const removed = state.cidrs.splice(Number(index), 1)[0];
  renderCidrs();
  addLog(`移除校园网段: ${removed}`);
});
$('#refreshButton').addEventListener('click', refreshBackendState);
$('#checkCore').addEventListener('click', () => addLog('检查 runtime/mihomo-windows-amd64-v3.exe（演示状态）'));
$('#downloadCore').addEventListener('click', async () => {
  if (!state.helperOnline) {
    addLog('下载核心失败：helper 未连接');
    return;
  }
  const button = $('#downloadCore');
  button.disabled = true;
  button.textContent = '下载中...';
  try {
    const result = await api('/runtime/download', { method: 'POST' });
    $('#runtimeValue').textContent = '核心已就绪';
    $('#runtimeDescription').textContent = `${result.version} 已安装`;
    $('#runtimeName').textContent = 'mihomo Alpha 已就绪';
    $('#runtimePill').textContent = '已安装';
    $('#runtimePill').className = 'status-pill success';
    addLog(`mihomo Alpha 下载完成: ${result.version}`);
  } catch (error) {
    addLog(`下载核心失败: ${error.message}`);
  } finally {
    button.disabled = false;
    button.textContent = '自动下载核心';
  }
});
$('#registerWarp').addEventListener('click', async () => {
  if (!state.helperOnline) {
    addLog('WARP 注册失败：helper 未连接');
    return;
  }
  try {
    await api('/warp/register', { method: 'POST', body: JSON.stringify({ name: 'freev6-windows' }) });
    addLog('WARP 注册成功');
  } catch (error) {
    addLog(`WARP 注册失败: ${error.message}`);
  }
});
$('#logsButton').addEventListener('click', () => { $('#logDrawer').classList.add('open'); $('#scrim').classList.add('open'); });
$('#closeLogs').addEventListener('click', () => { $('#logDrawer').classList.remove('open'); $('#scrim').classList.remove('open'); });
$('#scrim').addEventListener('click', () => { $('#logDrawer').classList.remove('open'); $('#scrim').classList.remove('open'); });
$('.close-banner').addEventListener('click', (event) => event.currentTarget.closest('.info-banner').remove());

renderCidrs();
renderLogs();
refreshBackendState();
