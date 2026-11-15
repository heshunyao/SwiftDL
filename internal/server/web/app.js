const $ = (s) => document.querySelector(s);

let state = { tasks: [], global: {}, config: {} };
const rows = new Map();
let filter = 'all';
let selectedId = null;
let configLoaded = false;
let toastTimer = null;
let online = true;

/* ---------- 请求 ---------- */

async function api(path, options = {}) {
  const res = await fetch(path, options);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || ('HTTP ' + res.status));
  return data;
}

function post(path, body) {
  return api(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}

/* ---------- 格式化 ---------- */

function fmtBytes(n) {
  n = Number(n) || 0;
  if (n <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return (i === 0 ? v.toFixed(0) : v.toFixed(v >= 100 ? 0 : 1)) + ' ' + units[i];
}

function fmtSpeed(n) {
  n = Number(n) || 0;
  return n > 0 ? fmtBytes(n) + '/s' : '--';
}

function fmtEta(sec) {
  sec = Math.floor(Number(sec) || 0);
  if (sec <= 0) return '--';
  if (sec < 60) return sec + ' 秒';
  if (sec < 3600) return Math.floor(sec / 60) + ' 分钟';
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  return h + ' 时 ' + m + ' 分';
}

function baseName(p) {
  if (!p) return '未命名';
  const parts = String(p).split(/[\\/]/);
  return parts[parts.length - 1] || p;
}

const STATUS_TEXT = {
  queued: '等待中',
  downloading: '下载中',
  paused: '已暂停',
  completed: '已完成',
  failed: '失败',
  canceled: '已取消',
};

function toast(msg, isError) {
  const el = $('#toast');
  el.textContent = msg;
  el.className = 'toast show' + (isError ? ' err' : '');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.className = 'toast' + (isError ? ' err' : ''); }, 2600);
}

/* ---------- 列表 ---------- */

function createRow(id) {
  const el = document.createElement('div');
  el.className = 'row';
  el.innerHTML = `
    <div class="c-name"><span class="ficon"></span><span class="fname"></span></div>
    <div class="c-size"></div>
    <div class="c-prog"><div class="pbar"><div class="pfill"></div></div><span class="ptext"></span></div>
    <div class="c-speed"></div>
    <div class="c-eta"></div>
    <div class="c-state"></div>
    <div class="row-err" style="display:none"></div>
  `;

  const row = {
    el,
    name: el.querySelector('.fname'),
    size: el.querySelector('.c-size'),
    fill: el.querySelector('.pfill'),
    ptext: el.querySelector('.ptext'),
    speed: el.querySelector('.c-speed'),
    eta: el.querySelector('.c-eta'),
    st: el.querySelector('.c-state'),
    err: el.querySelector('.row-err'),
  };

  el.addEventListener('click', () => select(id));
  el.addEventListener('dblclick', () => doAction('reveal'));

  return row;
}

function updateRow(row, t) {
  row.el.dataset.id = t.id;
  row.el.className = 'row s-' + t.status + (t.id === selectedId ? ' sel' : '');

  const name = baseName(t.savePath);
  if (row.name.textContent !== name) row.name.textContent = name;
  row.name.title = t.savePath || '';

  const p = Math.max(0, Math.min(100, t.progress || 0));
  row.fill.style.width = p + '%';
  row.ptext.textContent = p >= 100 ? '100%' : p.toFixed(p < 10 ? 1 : 0) + '%';

  row.size.textContent = t.size > 0
    ? fmtBytes(t.downloaded) + ' / ' + fmtBytes(t.size)
    : fmtBytes(t.downloaded);

  row.speed.textContent = t.status === 'downloading' ? fmtSpeed(t.speed) : '--';
  row.eta.textContent = t.status === 'downloading' ? fmtEta(t.etaSeconds) : '--';
  row.st.textContent = t.statusText || STATUS_TEXT[t.status] || t.status;
  row.st.className = 'c-state ' + t.status;

  if (t.error) {
    row.err.style.display = '';
    row.err.textContent = t.error;
  } else {
    row.err.style.display = 'none';
  }
}

function matchFilter(t) {
  if (filter === 'active') return t.status === 'downloading' || t.status === 'queued' || t.status === 'paused';
  if (filter === 'completed') return t.status === 'completed';
  if (filter === 'failed') return t.status === 'failed' || t.status === 'canceled';
  return true;
}

function render() {
  const list = (state.tasks || []).filter(matchFilter);

  const counts = { all: 0, active: 0, completed: 0, failed: 0 };
  (state.tasks || []).forEach((t) => {
    counts.all++;
    if (t.status === 'downloading' || t.status === 'queued' || t.status === 'paused') counts.active++;
    else if (t.status === 'completed') counts.completed++;
    else counts.failed++;
  });
  $('#cntAll').textContent = counts.all;
  $('#cntActive').textContent = counts.active;
  $('#cntDone').textContent = counts.completed;
  $('#cntFail').textContent = counts.failed;

  const speed = state.global.downloadSpeed || 0;
  $('#globalSpeed').textContent = fmtSpeed(speed) === '--' ? '0 B/s' : fmtSpeed(speed);
  $('#statusSpeed').innerHTML = '总下载速度 <b>' + (fmtSpeed(speed) === '--' ? '0 B/s' : fmtSpeed(speed)) + '</b>';
  $('#statusCount').textContent = '正在下载 ' + (state.global.activeTasks || 0) + ' 个 / 共 ' + counts.all + ' 个';
  $('#statusDir').textContent = state.config.saveDir ? '保存到 ' + state.config.saveDir : '';
  $('#lastUpdate').textContent = '更新于 ' + new Date().toLocaleTimeString('zh-CN', { hour12: false });

  const box = $('#list');

  if (!list.length) {
    rows.forEach((r) => r.el.remove());
    rows.clear();
    box.innerHTML = '<div class="empty"><b>暂无任务</b>点击左上角「新建」添加下载链接</div>';
    updateToolbar();
    return;
  }

  const empty = box.querySelector('.empty');
  if (empty) empty.remove();

  const seen = new Set();
  list.forEach((t, index) => {
    seen.add(t.id);
    let row = rows.get(t.id);
    if (!row) {
      row = createRow(t.id);
      rows.set(t.id, row);
    }
    updateRow(row, t);
    const current = box.children[index];
    if (current !== row.el) box.insertBefore(row.el, current || null);
  });

  rows.forEach((row, id) => {
    if (!seen.has(id)) { row.el.remove(); rows.delete(id); }
  });

  updateToolbar();
}

/* ---------- 选中与操作 ---------- */

function select(id) {
  selectedId = id;
  rows.forEach((row) => row.el.classList.toggle('sel', row.el.dataset.id === id));
  updateToolbar();
}

function selectedTask() {
  return (state.tasks || []).find((t) => t.id === selectedId) || null;
}

function updateToolbar() {
  const t = selectedTask();
  $('#btnStart').disabled = !(t && ['paused', 'failed', 'canceled'].includes(t.status));
  $('#btnPause').disabled = !(t && ['downloading', 'queued'].includes(t.status));
  $('#btnDelete').disabled = !t;
  $('#btnReveal').disabled = !t;
}

async function doAction(action) {
  const t = selectedTask();
  if (!t) { toast('请先选择一个任务'); return; }
  try {
    if (action === 'delete') {
      if (!confirm('确定删除任务「' + baseName(t.savePath) + '」？\n已下载的本地文件会一并删除。')) return;
      await api('/api/tasks/' + encodeURIComponent(t.id) + '?deleteFile=1', { method: 'DELETE' });
      if (selectedId === t.id) selectedId = null;
    } else {
      await post('/api/tasks/' + encodeURIComponent(t.id) + '/' + action);
    }
    await refresh();
  } catch (e) {
    toast(e.message, true);
  }
}

/* ---------- 弹窗 ---------- */

function openDlg(id) { $('#' + id).classList.add('open'); }
function closeDlg(id) { $('#' + id).classList.remove('open'); }

document.querySelectorAll('[data-close]').forEach((el) => {
  el.addEventListener('click', () => closeDlg(el.dataset.close));
});

document.querySelectorAll('.modal-mask').forEach((mask) => {
  mask.addEventListener('mousedown', (e) => {
    if (e.target === mask) mask.classList.remove('open');
  });
});

/* ---------- 新建任务 ---------- */

function openNewDlg() {
  const cfg = state.config || {};
  $('#addError').textContent = '';
  $('#probeInfo').textContent = '';
  if (!$('#dir').value) $('#dir').value = cfg.saveDir || '';
  if (!$('#threads').value) $('#threads').value = cfg.defaultThreads || 16;
  if ($('#retries').value === '') $('#retries').value = cfg.retries ?? 3;
  openDlg('newDlg');
  setTimeout(() => $('#url').focus(), 30);
}

async function addTasks() {
  const errEl = $('#addError');
  errEl.textContent = '';

  const urls = $('#url').value.split('\n').map((s) => s.trim()).filter(Boolean);
  if (!urls.length) { errEl.textContent = '请填写下载地址'; return; }
  for (const u of urls) {
    if (!/^https?:\/\//i.test(u)) { errEl.textContent = '地址格式不对：' + u; return; }
  }

  const base = {
    mirrors: $('#mirrors').value.split('\n').map((s) => s.trim()).filter(Boolean),
    dir: $('#dir').value.trim(),
    threads: Number($('#threads').value) || 0,
    speedLimit: Math.max(0, Number($('#speedLimit').value) || 0) * 1024,
    retries: $('#retries').value === '' ? -1 : Number($('#retries').value),
    expectedHash: $('#hash').value.trim(),
  };

  const btn = $('#addBtn');
  btn.disabled = true;
  btn.textContent = '正在解析…';

  const failed = [];
  try {
    for (const url of urls) {
      try {
        await post('/api/tasks', Object.assign({ url }, base));
      } catch (e) {
        failed.push(url + ' → ' + e.message);
      }
    }
  } finally {
    btn.disabled = false;
    btn.textContent = '立即下载';
  }

  if (failed.length) {
    errEl.textContent = failed.join('；');
    toast('有 ' + failed.length + ' 个地址添加失败', true);
  } else {
    closeDlg('newDlg');
    $('#url').value = '';
    $('#mirrors').value = '';
    $('#hash').value = '';
    toast('已加入下载队列（' + urls.length + ' 个）');
  }
  await refresh();
}

/* ---------- 设置 ---------- */

function applyConfig(cfg) {
  $('#cfgMax').value = cfg.maxConcurrentTasks || 3;
  $('#cfgLimit').value = Math.round((cfg.globalSpeedLimit || 0) / 1024);
  $('#cfgThreads').value = cfg.defaultThreads || 16;
  $('#cfgRetries').value = cfg.retries ?? 3;
  $('#cfgDir').value = cfg.saveDir || '';
  $('#cfgProxy').value = cfg.proxy || '';

  $('#sideDir').textContent = cfg.saveDir || '-';
  $('#sideDir').title = cfg.saveDir || '';
  if (!$('#dir').value) $('#dir').value = cfg.saveDir || '';
  if (!$('#threads').value) $('#threads').value = cfg.defaultThreads || 16;
  if ($('#retries').value === '') $('#retries').value = cfg.retries ?? 3;
}

async function saveConfig() {
  const body = {
    saveDir: $('#cfgDir').value.trim(),
    maxConcurrentTasks: Number($('#cfgMax').value) || 3,
    defaultThreads: Number($('#cfgThreads').value) || 16,
    globalSpeedLimit: Math.max(0, Number($('#cfgLimit').value) || 0) * 1024,
    retries: Number($('#cfgRetries').value) || 0,
    retryDelayMs: (state.config && state.config.retryDelayMs) || 1000,
    proxy: $('#cfgProxy').value.trim(),
  };
  try {
    state.config = await api('/api/config', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    applyConfig(state.config);
    $('#cfgMsg').textContent = '已保存';
    toast('设置已保存');
    setTimeout(() => { $('#cfgMsg').textContent = ''; }, 2000);
  } catch (e) {
    toast(e.message, true);
  }
}

/* ---------- 轮询 ---------- */

async function refresh() {
  let data;
  try {
    data = await api('/api/state');
  } catch (e) {
    online = false;
    $('#connDot').classList.add('off');
    $('#connText').textContent = '离线 · ' + (e.message || '连接失败');
    return;
  }

  state = data;
  if (!configLoaded) {
    applyConfig(state.config || {});
    configLoaded = true;
  }
  if (!online) {
    online = true;
    $('#connDot').classList.remove('off');
  }
  $('#connText').textContent = '就绪';

  try {
    render();
  } catch (e) {
    $('#connText').textContent = '界面异常 · ' + e.message;
    console.error(e);
  }
}

/* ---------- 事件 ---------- */

$('#btnNew').addEventListener('click', openNewDlg);
$('#btnStart').addEventListener('click', () => doAction('resume'));
$('#btnPause').addEventListener('click', () => doAction('pause'));
$('#btnDelete').addEventListener('click', () => doAction('delete'));
$('#btnReveal').addEventListener('click', () => doAction('reveal'));
$('#btnSettings').addEventListener('click', () => openDlg('cfgDlg'));
$('#addBtn').addEventListener('click', addTasks);
$('#saveCfg').addEventListener('click', saveConfig);

$('#advToggle').addEventListener('click', () => {
  const box = $('#advBox');
  box.hidden = !box.hidden;
  $('#advToggle').textContent = box.hidden ? '高级设置 ▾' : '高级设置 ▴';
});

$('#filters').addEventListener('click', (e) => {
  const li = e.target.closest('li[data-filter]');
  if (!li) return;
  filter = li.dataset.filter;
  document.querySelectorAll('#filters li').forEach((x) => x.classList.toggle('on', x === li));
  render();
});

let probeTimer = null;
$('#url').addEventListener('input', () => {
  clearTimeout(probeTimer);
  const url = $('#url').value.split('\n')[0].trim();
  if (!/^https?:\/\//i.test(url)) { $('#probeInfo').textContent = ''; return; }
  probeTimer = setTimeout(async () => {
    $('#probeInfo').textContent = '正在解析…';
    try {
      const meta = await post('/api/probe', { url });
      const range = meta.rangeSupported ? '支持分块并发' : '不支持 Range（单连接）';
      const size = meta.size > 0 ? fmtBytes(meta.size) : '未知大小';
      $('#probeInfo').textContent = size + ' · ' + range + (meta.filename ? ' · ' + meta.filename : '');
    } catch (e) {
      $('#probeInfo').textContent = e.message;
    }
  }, 600);
});

document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') document.querySelectorAll('.modal-mask.open').forEach((m) => m.classList.remove('open'));
  if ((e.key === 'Delete' || e.key === 'Backspace') && !/input|textarea/i.test(document.activeElement.tagName)) {
    if (selectedId) doAction('delete');
  }
});

refresh();
setInterval(refresh, 800);

window.addEventListener('error', (ev) => {
  toast('页面脚本错误：' + (ev.message || '未知错误'), true);
});
