// 核心：后端交互、状态推送（SSE，失败时退回轮询）、页面导航、主题、提示框、顶栏、终端列表
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


let S = null;                 // 当前终端的状态（SSE 全量快照 + 差量合并；退回轮询时为 /api/state）
// 多终端：当前选中的终端编号（每个浏览器各自记住），接口路径 api/t/<编号>/...
let TID = localStorage.getItem('jt808.term') || '';
const api = p => (TID ? `api/t/${encodeURIComponent(TID)}/` : 'api/') + p;
const switchHooks = [];       // 切换终端时调用（清空日志、重置地图视野等）
const onSwitch = fn => switchHooks.push(fn);
const stateHooks = [];        // 每次状态更新后调用
const actHooks = [];          // 页面动作成功后调用 (type, params)，场景录制用
const onAct = fn => actHooks.push(fn);
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
    const prev = document.activeElement;
    $('#modalText').textContent = text;
    $('#modal').hidden = false;
    $('#modalYes')?.focus();
    const done = v => {
      $('#modal').hidden = true;
      $('#modalYes').onclick = $('#modalNo').onclick = null;
      if (prev && typeof prev.focus === 'function') prev.focus();
      res(v);
    };
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

// ---------------- 复制到剪贴板 ----------------
// 优先 navigator.clipboard；WebView2 / 非安全上下文下不可用时退回隐藏 textarea + execCommand('copy')
async function copyText(text) {
  text = String(text ?? '');
  if (!text) return false;
  try {
    if (navigator.clipboard && window.isSecureContext) { await navigator.clipboard.writeText(text); return true; }
  } catch (_) { }
  const ta = document.createElement('textarea');
  ta.value = text; ta.setAttribute('readonly', '');
  ta.style.cssText = 'position:fixed;top:-1000px;left:-1000px;opacity:0';
  document.body.appendChild(ta);
  const prev = document.activeElement;
  ta.select();
  let ok = false;
  try { ok = document.execCommand('copy'); } catch (_) { }
  ta.remove();
  if (prev && typeof prev.focus === 'function') prev.focus();
  return ok;
}
async function copyAndToast(text) {
  if (await copyText(text)) toast('已复制 ' + text, 'ok');
  else toast('复制失败，请手动选择文本复制');
}
document.addEventListener('click', e => {
  const c = e.target.closest('[data-copy]');
  if (c && c.dataset.copy) { e.preventDefault(); copyAndToast(c.dataset.copy); }
});
document.addEventListener('keydown', e => {
  if (e.key !== 'Enter' && e.key !== ' ') return;
  const c = e.target.closest && e.target.closest('[data-copy][role=button]');
  if (c && c.dataset.copy) { e.preventDefault(); copyAndToast(c.dataset.copy); }
});

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
    actHooks.forEach(fn => { try { fn(type, params); } catch (e) { console.error(e); } });
    if (!streamOK) poll();   // 状态推送连着时由后端立即推送变化
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
    closeMore();
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

// ---------------- 侧栏收起 / 展开（记在本机）；窄屏（≤720px）自动收起为图标栏，展开时浮在内容上方 ----------------
const narrowMQ = window.matchMedia('(max-width: 720px)');
const HTML = document.documentElement;
function syncSideTg() {
  const narrow = narrowMQ.matches;
  HTML.classList.toggle('side-auto', narrow && !HTML.classList.contains('side-float'));
  if (!narrow) HTML.classList.remove('side-float');
  const mini = HTML.classList.contains('side-mini') || HTML.classList.contains('side-auto');
  $('#sideTg .nt').textContent = mini ? '展开侧栏' : '收起侧栏';
  $('#sideTg').title = mini ? '展开侧栏' : '收起侧栏';
  $('#sideTg').setAttribute('aria-expanded', String(!mini));
}
$('#sideTg').onclick = () => {
  if (narrowMQ.matches) HTML.classList.toggle('side-float');   // 窄屏：临时展开为浮层，不记住
  else localStorage.setItem('jt808.sideMini', HTML.classList.toggle('side-mini') ? '1' : '0');
  syncSideTg();
  setTimeout(() => window.dispatchEvent(new Event('resize')), 0);
};
narrowMQ.addEventListener('change', () => { syncSideTg(); setTimeout(() => window.dispatchEvent(new Event('resize')), 0); });
document.addEventListener('click', e => {   // 窄屏浮层：点导航或侧栏外收起
  if (!HTML.classList.contains('side-float')) return;
  if (e.target.closest('#sideTg') || (e.target.closest('.side') && !e.target.closest('[data-page], [data-term]'))) return;
  HTML.classList.remove('side-float'); syncSideTg();
});
syncSideTg();

// ---------------- 「更多」下拉菜单 ----------------
function closeMore() { $('#moreMenu').hidden = true; $('#moreBtn').setAttribute('aria-expanded', 'false'); }
$('#moreBtn').onclick = e => {
  e.stopPropagation();
  const open = $('#moreMenu').hidden;
  $('#moreMenu').hidden = !open;
  $('#moreBtn').setAttribute('aria-expanded', String(open));
  if (open) $('#moreMenu [role=menuitem]')?.focus();
};
document.addEventListener('pointerdown', e => { if (!$('#moreMenu').hidden && !e.target.closest('#moreDd')) closeMore(); }, true);
window.addEventListener('blur', closeMore);
$('#moreMenu').addEventListener('keydown', e => {
  const items = $$('#moreMenu [role=menuitem]'), i = items.indexOf(document.activeElement);
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); items[(i + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length].focus(); }
  if (e.key === 'Escape') { e.stopPropagation(); closeMore(); $('#moreBtn').focus(); }
  if (e.key === 'Tab') closeMore();
});

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
  if (!$('#moreMenu').hidden) { closeMore(); return; }
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
const ST = { offline: '离线', connecting: '连接中', online: '在线', reconnecting: '重连中' };
// 连接状态胶囊：离线 / 连接中 / 重连中显示重连次数与最近错误（如「重连中 · 第 3 次 · connection refused」），完整内容见悬停提示
function connText(c) {
  if (c.status === 'online') return ST.online + (c.onlineSince ? ' · ' + dur(Date.now() - c.onlineSince) : '');
  const parts = [ST[c.status] || c.status];
  if (c.status !== 'offline' && c.reconnects > 0) parts.push(`第 ${c.reconnects} 次`);
  if (c.lastError) parts.push(shortErr(c.lastError));
  return parts.join(' · ');
}
// shortErr 错误摘要：去掉 "dial tcp 1.2.3.4:7611: connect:" 这类前缀，最长 40 字
function shortErr(e) {
  let s = String(e).replace(/^.*?(?:dial|read|write) (?:tcp|udp)[^:]*(?::\d+)?(?:->[^:]+:\d+)?: /, '').replace(/^connect: /, '');
  return s.length > 40 ? s.slice(0, 39) + '…' : s;
}
// copyChip 可点击复制的值（手机号 / 车牌）：键盘可达（role=button + Enter/Space）
const copyChip = (v, cls = '') => v ? `<b class="cp ${cls}" role="button" tabindex="0" data-copy="${esc(v)}" title="点击复制">${esc(v)}</b>` : `<b class="${cls}">未设置</b>`;
function renderTop(s) {
  const c = s.conn, k = s.counters;
  const pill = $('#connPill');
  pill.className = 'status st-' + c.status;
  $('#connText').textContent = connText(c);
  pill.title = [`状态：${ST[c.status] || c.status}`, c.reconnects ? `重连次数：${c.reconnects}` : '', c.lastError ? `最近错误：${c.lastError}` : '',
    `服务器：${c.host}:${c.port}/${(c.transport || 'tcp').toUpperCase()}`].filter(Boolean).join('\n');
  const metaSig = `${c.phone}|${c.version}|${c.host}:${c.port}|${c.transport}|${s.device.plate}`;
  if (renderTop.metaSig !== metaSig) {
    renderTop.metaSig = metaSig;
    $('#meta').innerHTML = [['手机号', copyChip(c.phone, 'mono')], ['协议', `<b>JT/T 808-${esc(c.version)}</b>`], ['服务器', `<b class="mono">${esc(`${c.host}:${c.port}${c.transport === 'udp' ? ' /UDP' : ''}`)}</b>`], ['车牌', copyChip(s.device.plate)]]
      .map(([n, v]) => `<span class="m"><span>${n}</span>${v}</span>`).join('');
    $('#tbCur').innerHTML = `${copyChip(c.phone, 'mono')}${s.device.plate ? `<span class="cp" role="button" tabindex="0" data-copy="${esc(s.device.plate)}" title="点击复制">${esc(s.device.plate)}</span>` : ''}`;
  }
  // 单个上线 / 断开切换按钮：离线时为蓝色主按钮「上线」，否则为「断开」
  const on = c.status !== 'offline', btn = $('#btnConn');
  btn.textContent = on ? '断开' : '上线';
  btn.classList.toggle('dk', !on);
  btn.dataset.on = on ? '1' : '';
  btn.title = on ? '断开连接（不注销）' : '上线（无鉴权码先注册）';
  const alarms = s.alarms.filter(b => b.on).length + s.videoAlarms.filter(b => b.on).length;
  $('#navAlarm').hidden = !alarms; $('#navAlarm').textContent = alarms;
  $('#navStream').hidden = !s.streams.length; $('#navStream').textContent = s.streams.length;
  $('#lStat').textContent = `上行 ${k.up} · 下行 ${k.down}`;
  $('#sbInfo').textContent = `上行 ${k.up}  ·  下行 ${k.down}`;
  // 盲区队列：接近上限或已丢弃时在状态栏提示
  const warn = k.blindDropped > 0 ? `盲区队列已满，已丢弃最旧 ${k.blindDropped} 条（上限 ${k.blindCap}）` : k.blindQueue >= k.blindCap * 0.9 ? `盲区队列接近上限 ${k.blindQueue}/${k.blindCap}` : '';
  $('#sbWarn').hidden = !warn; $('#sbWarn').textContent = warn ? '⚠ ' + warn : '';
  if (k.blindDropped > (renderTop.dropped ?? k.blindDropped)) toast(`盲区补报队列已满（上限 ${k.blindCap} 条），丢弃了最旧的数据；可在「终端配置 → 行为」调大上限`);
  renderTop.dropped = k.blindDropped;
}
$('#btnConn').onclick = e => {
  const on = !!e.currentTarget.dataset.on;
  act(on ? 'disconnect' : 'connect', {}, e.currentTarget, on ? '已断开' : '正在上线…');
};
onSwitch(() => { renderTop.metaSig = ''; renderTop.dropped = undefined; });
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

// ---------------- 状态：SSE 推送（全量快照 + 变化字段），断开时退回 1 秒轮询 ----------------
let streamOK = false, stateES = null, streamRetry = 0;
function setState(j) { S = j; runHooks(); }
function startStream() {
  clearTimeout(streamRetry);
  if (stateES) { stateES.close(); stateES = null; }
  streamOK = false;
  if (document.hidden || !window.EventSource) return;
  const want = TID, es = stateES = new EventSource(api('state/stream'));
  es.addEventListener('state', m => {
    if (stateES !== es || want !== TID) return;
    let j;
    try { j = JSON.parse(m.data); } catch (_) { return; }
    if (j.full) {
      streamOK = true;
      if (j.build && BUILD && j.build !== BUILD) { checkBuild({ headers: { get: () => j.build } }); return; }
      setState(j.state);
    } else if (S && j.diff) {
      Object.assign(S, j.diff);
      runHooks();
    }
  });
  es.addEventListener('gone', () => { es.close(); if (stateES === es) { stateES = null; streamOK = false; loadTerms(); } });
  es.onerror = () => {   // 网络中断 / 服务重启：关闭并在 3 秒后重连，期间由轮询兜底
    es.close();
    if (stateES !== es) return;
    stateES = null; streamOK = false;
    streamRetry = setTimeout(startStream, 3000);
  };
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
    setState(j);
  } catch (e) {
    $('#connPill').className = 'status st-connecting';
    $('#connText').textContent = '模拟器未响应';
  } finally {
    pollingState = false;
  }
}
setInterval(() => { if (!streamOK) poll(); }, 1000);

// ---------------- 终端列表（侧栏）：搜索、在线状态点、多选批量上线 / 断开 ----------------
let TERMS = [], loadingTerms = false, termMax = 0;
const tSel = new Set();       // 多选模式下选中的终端编号
let tSelMode = false;
async function loadTerms() {
  if (document.hidden || loadingTerms) return;
  loadingTerms = true;
  try {
    const j = await fetchJSON('api/terminals', { cache: 'no-store' });
    TERMS = j.terminals || [];
    termMax = j.max;
    if (!TERMS.some(t => t.id === TID)) setTerm(j.default, true);
    for (const id of tSel) if (!TERMS.some(t => t.id === id)) tSel.delete(id);
    renderTerms();
  } catch (_) { }
  finally { loadingTerms = false; }
}
const termMatch = (t, q) => !q || [t.id, '#' + t.id, t.phone, t.plate].some(v => String(v || '').toLowerCase().includes(q));
function renderTerms() {
  const q = $('#tFilter').value.trim().toLowerCase();
  const list = TERMS.filter(t => termMatch(t, q));
  const sig = JSON.stringify([TID, TERMS, q, tSelMode, [...tSel]]);
  if (renderTerms.sig === sig) return;
  renderTerms.sig = sig;
  const online = TERMS.filter(t => t.status === 'online').length;
  $('#tCount').textContent = TERMS.length > 1 ? `${online}/${TERMS.length} 在线` : '';
  $('#tchips').classList.toggle('sel-mode', tSelMode);
  $('#tchips').innerHTML = list.map(t => {
    const st = ST[t.status] || t.status, picked = tSel.has(t.id);
    return `<button class="tc st-${t.status} ${t.id === TID ? 'on' : ''} ${picked ? 'picked' : ''}" role="option" aria-selected="${tSelMode ? picked : t.id === TID}" data-term="${esc(t.id)}" title="#${esc(t.id)} ${esc(t.phone)} · ${esc(t.host)}:${t.port}/${esc((t.transport || 'tcp').toUpperCase())} · ${esc(st)}${t.default ? ' · 默认终端（旧接口 /api/state 等作用于它）' : ''}">
    ${tSelMode ? `<i class="ck" aria-hidden="true"></i>` : ''}<i class="dot" aria-label="${esc(st)}"></i><b>#${esc(t.id)}</b><span class="mono">${esc(t.phone || '未设置')}</span><em>${esc(t.plate || '')}</em>${t.alarms ? `<span class="badge" aria-label="${t.alarms} 项报警">${t.alarms}</span>` : ''}</button>`;
  }).join('') || `<div class="empty sm">没有匹配「${esc(q)}」的终端</div>`;
  $('#tDel').disabled = TERMS.length <= 1;
  $('#tAdd').disabled = $('#tClone').disabled = termMax && TERMS.length >= termMax;
  $('#tBatch').hidden = !tSelMode;
  $('#tSelN').textContent = `已选 ${tSel.size}`;
  $('#tAll').checked = list.length > 0 && list.every(t => tSel.has(t.id));
  $('#tAll').indeterminate = !$('#tAll').checked && list.some(t => tSel.has(t.id));
  $('#tOn').disabled = $('#tOff').disabled = !tSel.size;
  if (!tSelMode) $('#tchips .tc.on')?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
}
function setTerm(id, quiet) {
  if (!id || id === TID) return;
  TID = id;
  localStorage.setItem('jt808.term', id);
  S = null;
  switchHooks.forEach(fn => { try { fn(id); } catch (e) { console.error(e); } });
  renderTerms();
  if (!quiet) { const t = TERMS.find(x => x.id === id); toast(`已切换到终端 #${id}${t ? ' ' + t.phone : ''}`, 'info'); }
  startStream();
  poll();
}
$('#tchips').onclick = e => {
  const b = e.target.closest('[data-term]');
  if (!b) return;
  if (tSelMode) { const id = b.dataset.term; tSel.has(id) ? tSel.delete(id) : tSel.add(id); renderTerms(); return; }
  setTerm(b.dataset.term);
};
$('#tFilter').addEventListener('input', renderTerms);
$('#tFilter').addEventListener('keydown', e => {
  if (e.key === 'Escape' && e.target.value) { e.stopPropagation(); e.target.value = ''; renderTerms(); }
  if (e.key === 'Enter' && !tSelMode) { const first = $('#tchips [data-term]'); if (first) setTerm(first.dataset.term); }
});
$('#tSel').onclick = () => {
  tSelMode = !tSelMode;
  if (!tSelMode) tSel.clear();
  $('#tSel').setAttribute('aria-pressed', String(tSelMode));
  $('#tSel').classList.toggle('on', tSelMode);
  renderTerms();
};
$('#tAll').onchange = e => {
  const q = $('#tFilter').value.trim().toLowerCase();
  TERMS.filter(t => termMatch(t, q)).forEach(t => e.target.checked ? tSel.add(t.id) : tSel.delete(t.id));
  renderTerms();
};
// 批量上线 / 断开：逐个调用各终端的动作接口
async function batchConn(type, btn) {
  const ids = TERMS.filter(t => tSel.has(t.id)).map(t => t.id);
  if (!ids.length) return;
  btn.disabled = true;
  let ok = 0;
  const errs = [];
  await Promise.all(ids.map(async id => {
    try {
      const j = await fetchJSON(`api/t/${encodeURIComponent(id)}/action`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ type }) });
      if (j.ok) ok++; else errs.push(`#${id} ${j.error || ''}`);
    } catch (e) { errs.push(`#${id} ${e.message}`); }
  }));
  btn.disabled = false;
  toast(`${type === 'connect' ? '上线' : '断开'} ${ok}/${ids.length} 个终端${errs.length ? '；失败：' + errs.slice(0, 3).join('，') : ''}`, errs.length ? 'err' : 'ok');
  loadTerms();
}
$('#tOn').onclick = e => batchConn('connect', e.currentTarget);
$('#tOff').onclick = e => batchConn('disconnect', e.currentTarget);
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
// 窗口隐藏时断开状态推送（与原先暂停轮询一致），回到前台重新订阅（先收到全量快照）
document.addEventListener('visibilitychange', () => {
  if (document.hidden) { clearTimeout(streamRetry); if (stateES) { stateES.close(); stateES = null; } streamOK = false; }
  else { startStream(); poll(); loadTerms(); }
});

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
document.addEventListener('click', async e => {
  const a = e.target.closest('a[data-ext]');
  if (!a) return;
  e.preventDefault();
  const url = a.href;
  try {
    const j = await fetchJSON('api/open', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ url }) });
    if (!j.ok) window.open(url, '_blank', 'noopener');
  } catch (_) { window.open(url, '_blank', 'noopener'); }
});

window.addEventListener('DOMContentLoaded', () => {
  const h = location.hash.slice(1); // go('overview') 会清掉地址栏的 #，先记下深链接
  loadVersion(); loadTerms(); go('overview'); if (h) go(h); poll(); startStream(); });
