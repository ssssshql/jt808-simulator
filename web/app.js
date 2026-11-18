// 核心：后端交互、状态轮询、弹出面板（sheet）导航、主题、提示框、顶栏
'use strict';
const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const hex4 = n => Number(n).toString(16).toUpperCase().padStart(4, '0');
const hex8 = n => (Number(n) >>> 0).toString(16).toUpperCase().padStart(8, '0');
const fmtT = t => new Date(t).toLocaleTimeString('zh-CN', { hour12: false });
const fmtDT = t => new Date(t).toLocaleString('zh-CN', { hour12: false });
function dur(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  return `${Math.floor(s / 3600)}:${String(Math.floor(s / 60) % 60).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}`;
}

// 记住页面上手工填写的操作参数（附件数量这类「上报时用的值」，不属于终端状态、不随后端刷新），
// 重启模拟器或刷新页面后恢复上次输入；动态下拉尚未生成选项时先存 dataset.keepVal，由渲染函数兜底取用
function keepFields(ids) {
  const K = 'jt808.f.';
  for (const id of ids) {
    const el = document.getElementById(id);
    if (!el) continue;
    const apply = v => {
      if (el.type === 'checkbox') { el.checked = v === '1'; return true; }
      if (el.tagName === 'SELECT' && ![...el.options].some(o => o.value === v)) return false;
      el.value = v;
      return true;
    };
    const v = localStorage.getItem(K + id);
    if (v !== null && !apply(v)) el.dataset.keepVal = v;
    const save = () => {
      if (el.type === 'checkbox') localStorage.setItem(K + id, el.checked ? '1' : '0');
      else if (el.value !== '') localStorage.setItem(K + id, el.value);
    };
    el.addEventListener('change', save);
    el.addEventListener('input', save);
  }
}


let S = null;                 // 最近一次 /api/state// 多终端：当前选中的终端编号（每个浏览器各自记住），接口路径 api/t/<编号>/...
let TID = localStorage.getItem('jt808.term') || '';
const api = p => (TID ? `api/t/${encodeURIComponent(TID)}/` : 'api/') + p;
const switchHooks = [];       // 切换终端时调用（清空日志、重置地图视野等）
const onSwitch = fn => switchHooks.push(fn);
const stateHooks = [];        // 每次轮询后调用
const pageHooks = {};         // 切换到页面时调用
const onState = fn => stateHooks.push(fn);
const onPage = (p, fn) => (pageHooks[p] = pageHooks[p] || []).push(fn);

// ---------------- 提示框 / 确认框 ----------------
function toast(msg, kind = 'err') {
  if (kind === true) kind = 'ok';
  if (kind === false) kind = 'err';
  const d = document.createElement('div');
  d.className = 'toast ' + kind;
  d.innerHTML = `<i>${{ ok: '✓', err: '!', info: 'i' }[kind] || 'i'}</i><div>${esc(msg)}</div>`;
  $('#toasts').appendChild(d);
  setTimeout(() => { d.classList.add('out'); setTimeout(() => d.remove(), 300); }, kind === 'err' ? 5000 : 2200);
}

function confirmBox(text) {
  return new Promise(res => {
    $('#modalText').textContent = text;
    $('#modal').hidden = false;
    const done = v => { $('#modal').hidden = true; $('#modalYes').onclick = $('#modalNo').onclick = null; res(v); };
    $('#modalYes').onclick = () => done(true);
    $('#modalNo').onclick = () => done(false);
  });
}

// ---------------- 下载 / 另存为 ----------------
// 浏览器：a[download]；桌面版（WebView2）：交给后端弹出系统“另存为”对话框写文件
const DESKTOP = !!(window.chrome && window.chrome.webview);
function saveDone(j) {
  if (j.ok) toast('已保存到 ' + j.path, 'ok');
  else if (!j.cancelled) toast(j.error || '保存失败');
}
// downloadURL 后端生成的文件（url 已带查询参数时用 &save=1）
async function downloadURL(url) {
  if (DESKTOP) {
    try { saveDone(await (await fetch(url + (url.includes('?') ? '&' : '?') + 'save=1')).json()); } catch (e) { toast('保存失败：' + e.message); }
    return;
  }
  const a = document.createElement('a');
  a.href = url; a.download = ''; document.body.appendChild(a); a.click(); a.remove();
}
// saveText 页面生成的内容
async function saveText(name, text, mime = 'text/plain;charset=utf-8') {
  if (DESKTOP) {
    try { saveDone(await (await fetch('api/savefile?name=' + encodeURIComponent(name), { method: 'POST', body: text })).json()); } catch (e) { toast('保存失败：' + e.message); }
    return;
  }
  const url = URL.createObjectURL(new Blob([text], { type: mime }));
  const a = document.createElement('a');
  a.href = url; a.download = name; document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 5000);
}

// ---------------- 后端 ----------------
async function fetchJSON(url, opts) {
  const r = await fetch(url, opts);
  if (!r.ok) {
    let msg = `HTTP ${r.status}`;
    try { const j = await r.json(); if (j?.error) msg = j.error; } catch (_) { }
    throw new Error(msg);
  }
  return r.json();
}

async function act(type, params = {}, btn, okMsg) {
  if (btn) btn.disabled = true;
  try {
    const j = await fetchJSON(api('action'), { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ type, ...params }) });
    if (!j.ok) { toast(j.error || '操作失败'); return false; }
    if (okMsg) toast(okMsg, 'ok');
    poll();
    return true;
  } catch (e) { toast('请求失败：' + e.message); return false; }
  finally { if (btn) btn.disabled = false; }
}

// 正在编辑的输入框不被轮询覆盖
const editing = new Set();
document.addEventListener('focusin', e => { if (e.target.id) editing.add(e.target.id); });
document.addEventListener('focusout', e => editing.delete(e.target.id));
function setVal(id, v) { const el = document.getElementById(id); if (el && !editing.has(id) && !el.dataset.dirty) el.value = v ?? ''; }

document.addEventListener('click', async e => {
  const b = e.target.closest('[data-act]');
  if (b) {
    if (b.dataset.confirm && !(await confirmBox(b.dataset.confirm))) return;
    const msg = { connect: '正在上线…', disconnect: '已断开', logout: '已注销', report: '已上报 0200', clearAlarms: '报警已清除' }[b.dataset.act];
    act(b.dataset.act, {}, b, msg);
  }
  const g = e.target.closest('[data-goto]');
  if (g) {
    e.preventDefault();
    go(g.dataset.goto);
    if (g.dataset.anchor) setTimeout(() => document.getElementById(g.dataset.anchor)?.scrollIntoView({ behavior: 'smooth' }), 50);
  }
});

// ---------------- 主题 ----------------
$('#themeBtn').onclick = () => {
  const t = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
  document.documentElement.dataset.theme = t;
  localStorage.setItem('jt808.theme', t);
  if (S) renderRing(S);
};

// ---------------- 侧栏收起 / 展开（记在本机） ----------------
function syncSideTg() {
  const mini = document.documentElement.classList.contains('side-mini');
  $('#sideTg .nt').textContent = mini ? '展开侧栏' : '收起侧栏';
  $('#sideTg').title = mini ? '展开侧栏' : '收起侧栏';
}
$('#sideTg').onclick = () => {
  const mini = document.documentElement.classList.toggle('side-mini');
  localStorage.setItem('jt808.sideMini', mini ? '1' : '0');
  syncSideTg();
  setTimeout(() => window.dispatchEvent(new Event('resize')), 0);
};
syncSideTg();

// ---------------- 导航：左侧栏切换「总览」与各功能页 ----------------
let page = 'overview';        // 'overview' = 只看主屏；其它 = 弹出面板当前标签
function go(p) {
  if (!document.getElementById('page-' + p)) p = 'overview';
  page = p;
  const open = p !== 'overview';
  $('#sheet').hidden = !open;
  $('#dash').hidden = open;
  if (!open) setTimeout(() => window.dispatchEvent(new Event('resize')), 0);   // 地图重新计算尺寸
  $('#pageTitle').textContent = $(`#nav a[data-page="${p}"] .nt`)?.textContent || '';
  document.body.classList.toggle('sheet-open', open);
  $$('#nav a').forEach(a => a.classList.toggle('on', a.dataset.page === p));
  $$('.page').forEach(s => s.hidden = s.id !== 'page-' + p);
  const h = open ? '#' + p : '';
  if (location.hash !== h) history.replaceState(null, '', h || location.pathname + location.search);
  (pageHooks[p] || []).forEach(fn => fn());
  if (open) { $('#content').scrollTop = 0; $('#nav a.on')?.scrollIntoView({ block: 'nearest', inline: 'nearest' }); }
  if (S) runHooks();
}
$('#nav').onclick = e => { const a = e.target.closest('a[data-page]'); if (a) go(a.dataset.page); };
$('#nav').addEventListener('keydown', e => {
  if (e.key !== 'Enter' && e.key !== ' ') return;
  const a = e.target.closest('a[data-page]');
  if (a) { e.preventDefault(); go(a.dataset.page); }
});
$('#sheetClose').onclick = $('#mask').onclick = () => go('overview');
document.addEventListener('keydown', e => {
  if (e.key !== 'Escape') return;
  for (const id of ['modal', 'exModal', 'updModal', 'repModal']) {
    const el = document.getElementById(id);
    if (el && !el.hidden) {
      if (id === 'modal') $('#modalNo')?.click();
      else el.hidden = true;
      return;
    }
  }
  if (page !== 'overview') go('overview');
});
window.addEventListener('hashchange', () => go(location.hash.slice(1)));

// ---------------- 顶栏 ----------------
const ST = { offline: '离线', connecting: '连接中', online: '在线', reconnecting: '等待重连' };
function renderTop(s) {
  const c = s.conn, k = s.counters;
  const pill = $('#connPill');
  pill.className = 'status st-' + c.status;
  $('#connText').textContent = ST[c.status] + (c.status === 'online' && c.onlineSince ? ' · ' + dur(Date.now() - c.onlineSince) : '') +
    (c.lastError && c.status !== 'online' ? ' · ' + c.lastError : '');
  pill.title = c.lastError || '';
  $('#meta').innerHTML = (() => {
    const metaSig = `${c.phone}|${c.version}|${c.host}:${c.port}|${c.transport}|${s.device.plate}`;
    if (renderTop.metaSig === metaSig) return $('#meta').innerHTML;
    renderTop.metaSig = metaSig;
    return [['手机号', c.phone || '未设置', 1], ['协议', 'JT/T 808-' + c.version], ['服务器', `${c.host}:${c.port}${c.transport === 'udp' ? ' /UDP' : ''}`, 1], ['车牌', s.device.plate || '-']]
      .map(([n, v, m]) => `<span class="m"><span>${n}</span><b class="${m ? 'mono' : ''}">${esc(v)}</b></span>`).join('');
  })();
  const on = c.status !== 'offline';
  $('#btnConnect').disabled = on;
  $('#btnDisconnect').disabled = !on;
  const alarms = s.alarms.filter(b => b.on).length + s.videoAlarms.filter(b => b.on).length;
  $('#navAlarm').hidden = !alarms; $('#navAlarm').textContent = alarms;
  $('#navStream').hidden = !s.streams.length; $('#navStream').textContent = s.streams.length;
  $('#lStat').textContent = `上行 ${k.up} · 下行 ${k.down}`;
  $('#tbCur').innerHTML = `<b class="mono">${esc(c.phone || '未设置')}</b><span>${esc(s.device.plate || '')}</span>`;
  $('#sbInfo').textContent = `上行 ${k.up}  ·  下行 ${k.down}`;
}
onState(renderTop);

// 渲染出错时同一错误只打印一次，避免每秒刷屏
const hookErrs = new Set();
function runHooks() {
  for (const fn of stateHooks) {
    try { fn(S); } catch (e) {
      const k = (fn.name || '') + ':' + e.message;
      if (!hookErrs.has(k)) { hookErrs.add(k); console.error(e); }
    }
  }
}
// 页面与服务端版本不一致（浏览器/CDN 缓存了旧页面）时自动刷新一次
const BUILD = document.querySelector('meta[name=jt808-build]')?.content || '';
function checkBuild(r) {
  const b = r.headers.get('X-JT808-Build');
  if (!b || !BUILD || b === BUILD) { sessionStorage.removeItem('jt808.reloaded'); return; }
  if (sessionStorage.getItem('jt808.reloaded') !== b) { sessionStorage.setItem('jt808.reloaded', b); location.reload(); }
}

let pollingState = false;
async function poll() {
  if (document.hidden || pollingState) return; // 窗口最小化/标签页隐藏时暂停轮询，避免并发重入
  pollingState = true;
  try {
    const want = TID, r = await fetch(api('state'), { cache: 'no-store' });
    checkBuild(r);
    if (r.status === 404) { TID = ''; return; }
    if (!r.ok) throw new Error('HTTP ' + r.status);
    const j = await r.json();
    if (want !== TID) return;   // 轮询途中切换了终端，丢弃旧终端的结果
    S = j;
    runHooks();
  } catch (e) {
    $('#connPill').className = 'status st-connecting';
    $('#connText').textContent = '模拟器未响应';
  } finally {
    pollingState = false;
  }
}
setInterval(poll, 1000);
// ---------------- 终端切换栏 ----------------
let TERMS = [], loadingTerms = false;
async function loadTerms() {
  if (document.hidden || loadingTerms) return;
  loadingTerms = true;
  try {
    const j = await fetchJSON('api/terminals', { cache: 'no-store' });
    TERMS = j.terminals || [];
    if (!TERMS.some(t => t.id === TID)) setTerm(j.default, true);
    renderTerms(j.max);
  } catch (_) { }
  finally { loadingTerms = false; }
}
function renderTerms(max) {
  const sig = JSON.stringify([TID, TERMS]);
  if (renderTerms.sig === sig) return;
  renderTerms.sig = sig;
  $('#tchips').innerHTML = TERMS.map(t => `<button class="tc st-${t.status} ${t.id === TID ? 'on' : ''}" data-term="${esc(t.id)}" title="#${esc(t.id)} ${esc(t.phone)} · ${esc(t.host)}:${t.port}/${esc((t.transport || 'tcp').toUpperCase())} · ${esc(ST[t.status] || t.status)}${t.default ? ' · 默认终端（旧接口 /api/state 等作用于它）' : ''}">
    <i class="dot"></i><b>#${esc(t.id)}</b><span class="mono">${esc(t.phone || '未设置')}</span><em>${esc(t.plate || '')}</em>${t.alarms ? `<span class="badge">${t.alarms}</span>` : ''}</button>`).join('');
  $('#tDel').disabled = TERMS.length <= 1;
  $('#tAdd').disabled = $('#tClone').disabled = max && TERMS.length >= max;
  $('#tchips .tc.on')?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
}
function setTerm(id, quiet) {
  if (!id || id === TID) return;
  TID = id;
  localStorage.setItem('jt808.term', id);
  S = null;
  switchHooks.forEach(fn => { try { fn(id); } catch (e) { console.error(e); } });
  renderTerms();
  if (!quiet) { const t = TERMS.find(x => x.id === id); toast(`已切换到终端 #${id}${t ? ' ' + t.phone : ''}`, 'info'); }
  poll();
}
$('#tchips').onclick = e => { const b = e.target.closest('[data-term]'); if (b) setTerm(b.dataset.term); };
async function termOp(op, extra = {}, btn) {
  if (btn) btn.disabled = true;
  try {
    const j = await fetchJSON('api/terminals', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ op, id: TID, ...extra }) });
    if (!j.ok) { toast(j.error || '操作失败'); return null; }
    return j;
  } catch (e) { toast('请求失败：' + e.message); return null; }
  finally { if (btn) btn.disabled = false; await loadTerms(); }
}
$('#tAdd').onclick = async e => { const j = await termOp('add', {}, e.currentTarget); if (j) { setTerm(j.id, true); toast(`已新增终端 #${j.id}`, 'ok'); } };
$('#tClone').onclick = async e => { const j = await termOp('clone', {}, e.currentTarget); if (j) { setTerm(j.id, true); toast(`已复制为终端 #${j.id}（手机号/车牌递增，需重新注册）`, 'ok'); } };
$('#tDel').onclick = async e => {
  const t = TERMS.find(x => x.id === TID);
  if (!t || !(await confirmBox(`删除终端 #${t.id}（${t.phone || '未设置手机号'}）？会先下线，并删除它保存的状态文件。`))) return;
  if (await termOp('delete', { purge: 1 }, e.currentTarget)) { toast(`终端 #${t.id} 已删除`, 'ok'); setTerm(TERMS[0]?.id, true); }
};
setInterval(loadTerms, 2000);
document.addEventListener('visibilitychange', () => { if (!document.hidden) { poll(); loadTerms(); } });

// 程序版本（/api/version，构建时由 -ldflags 注入）
async function loadVersion() {
  try {
    const v = await (await fetch('api/version', { cache: 'no-store' })).json();
    const el = $('#verTag');
    el.textContent = v.version;
    el.title = `版本 ${v.version}\n提交 ${v.commit || '-'}\n构建时间 ${v.buildTime || '-'}\n${v.go} ${v.platform}\n前端 ${v.web || '-'}`;
    el.hidden = false;
  } catch (_) { }
}
window.addEventListener('DOMContentLoaded', () => {
  const h = location.hash.slice(1); // go('overview') 会清掉地址栏的 #，先记下深链接
  loadVersion(); loadTerms(); go('overview'); if (h) go(h); poll(); });
