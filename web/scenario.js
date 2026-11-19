// 场景脚本（进程级，/api/scenarios）：编辑 YAML/JSON、场景库、运行 / 停止与进度
'use strict';
let SCEN = null, scCur = '';   // scCur 当前编辑的场景库名称（未保存的新场景为空）
const SC_ST = { running: ['运行中', 'info'], done: ['已完成', 'ok'], stopped: ['已停止', ''], error: ['出错', 'err'] };
const scDraft = localStorage.getItem('jt808.scenario');

async function scOp(body, btn) {
  if (btn) btn.disabled = true;
  try {
    const j = await (await fetch('api/scenarios', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })).json();
    if (!j.ok) { toast(j.error || '操作失败'); $('#scMsg').textContent = j.error || ''; $('#scMsg').className = 'hint err-text'; return null; }
    return j;
  } catch (e) { toast('请求失败：' + e.message); return null; }
  finally { if (btn) btn.disabled = false; pollScen(); }
}
function scSetText(text, name) {
  $('#scText').value = text; scCur = name || '';
  $('#scName').textContent = name || '';
  localStorage.setItem('jt808.scenario', text);
  $('#scMsg').textContent = ''; renderScLib();
}
$('#scText').addEventListener('input', () => localStorage.setItem('jt808.scenario', $('#scText').value));
$('#scText').addEventListener('keydown', e => {   // Tab 插入两个空格
  if (e.key !== 'Tab') return;
  e.preventDefault();
  const t = e.target, a = t.selectionStart;
  t.setRangeText('  ', a, t.selectionEnd, 'end');
});
$('#scNew').onclick = () => scSetText('name: 新场景\nterminal: ""\nconnect: true\nsteps:\n  - {at: 5, action: alarm, bit: 0}\n  - {after: 10, action: clearAlarm, bit: 0}\n', '');
$('#scExample').onclick = () => { if (SCEN) scSetText(SCEN.example, ''); };
$('#scImport').onclick = () => $('#scFile').click();
$('#scFile').onchange = async e => {
  const f = e.target.files[0]; e.target.value = '';
  if (f) scSetText(await f.text(), '');
};
$('#scCheck').onclick = async e => {
  const j = await scOp({ op: 'validate', text: $('#scText').value }, e.currentTarget);
  if (j) { $('#scMsg').className = 'hint ok-text'; $('#scMsg').textContent = `✓ 「${j.name}」校验通过，共 ${(j.steps || []).length} 步`; }
};
$('#scSave').onclick = async e => {
  const j = await scOp({ op: 'save', text: $('#scText').value }, e.currentTarget);
  if (j) { scCur = j.name; $('#scName').textContent = j.name; toast(`已保存场景「${j.name}」`, 'ok'); }
};
$('#scExport').onclick = () => {
  const text = $('#scText').value, json = text.trim().startsWith('{');
  const name = (text.match(/^name:\s*(.+)$/m)?.[1] || scCur || 'scenario').replace(/["']/g, '').trim();
  saveText(name + (json ? '.json' : '.yaml'), text);
};
$('#scRun').onclick = async e => {
  const j = await scOp({ op: 'run', text: $('#scText').value, terminal: TID }, e.currentTarget);
  if (j) { toast(`场景已在终端 #${j.terminal} 上运行`, 'ok'); $('#scMsg').textContent = ''; loadTerms(); }
};
$('#scStop').onclick = async e => {
  const run = scRunFor(TID) || (SCEN?.runs || []).find(r => r.status === 'running');
  if (!run) return;
  if (await scOp({ op: 'stop', terminal: run.terminal }, e.currentTarget)) toast('场景已停止', 'ok');
};
$('#scLib').onclick = async e => {
  const del = e.target.closest('[data-del]');
  if (del) {
    e.stopPropagation();
    if (await confirmBox(`从场景库删除「${del.dataset.del}」？`) && await scOp({ op: 'delete', name: del.dataset.del })) toast('已删除', 'ok');
    return;
  }
  const b = e.target.closest('[data-name]');
  const doc = b && SCEN.library.find(d => d.name === b.dataset.name);
  if (doc) scSetText(doc.text, doc.name);
};
$('#scRuns').onclick = e => { const b = e.target.closest('[data-term]'); if (b) setTerm(b.dataset.term); };

const scRunFor = id => (SCEN?.runs || []).find(r => r.terminal === id && r.status === 'running');
function renderScLib() {
  if (!SCEN) return;
  const lib = SCEN.library || [];
  $('#scLibN').textContent = lib.length || '';
  $('#scLib').innerHTML = lib.length ? lib.map(d => `<button class="${d.name === scCur ? 'on' : ''}" data-name="${esc(d.name)}" title="${esc(d.name)}">
    <span>${esc(d.name)}</span><em>${fmtDT(d.updated).slice(5, 16)}</em><i data-del="${esc(d.name)}" title="删除">✕</i></button>`).join('')
    : '<div class="empty">还没有保存的场景<br>编辑后点“保存到场景库”</div>';
}
function renderScRuns() {
  const runs = SCEN?.runs || [];
  const running = runs.filter(r => r.status === 'running').length;
  $('#navScen').hidden = !running && !scRec; $('#navScen').textContent = scRec ? '录' : running || '';
  if (page !== 'scenario') return;
  const t = TERMS.find(x => x.id === TID);
  $('#scTarget').textContent = t ? `未指定 terminal 时在当前终端 #${t.id} ${t.phone} 上运行` : '';
  $('#scStop').disabled = !running;
  const sig = JSON.stringify(runs);
  if (renderScRuns.sig === sig) return;
  renderScRuns.sig = sig;
  $('#scRuns').innerHTML = runs.length ? runs.map(r => {
    const [st, cls] = SC_ST[r.status] || [r.status, ''];
    const failed = new Set(r.log.filter(l => !l.ok && l.step >= 0).map(l => l.step));
    const steps = r.steps.map((s, i) => {
      const c = failed.has(i) ? 'bad' : i < r.step || r.status === 'done' ? 'ok' : i === r.step && r.status === 'running' ? 'cur' : '';
      return `<li class="${c}"><b>${{ ok: '✓', bad: '✕', cur: '▶' }[c] || i + 1}</b><span>${esc(s)}</span></li>`;
    }).join('');
    const last = r.log.slice(-3).reverse().map(l => `<div>${l.t}s ${l.ok ? '✓' : '✕'} ${esc(l.text)}${l.error ? ' — ' + esc(l.error) : ''}</div>`).join('');
    return `<div class="sc-run"><div class="sc-run-h"><b>${esc(r.name)}</b><span class="tag ${esc(cls)}">${esc(st)}</span>
      <button class="lnk" data-term="${esc(r.terminal)}">终端 #${esc(r.terminal)} ${esc(r.phone)}</button><span class="grow"></span>
      <span class="muted mono">${Math.min(r.step, r.total)}/${r.total} 步 · ${dur(r.elapsed * 1000)}${r.failures ? ` · 失败 ${r.failures}` : ''}${r.asserts ? ` · 断言 ${r.asserts - r.assertFails}/${r.asserts} 通过` : ''}</span></div>
      <div class="fbar"><i style="width:${(r.progress * 100).toFixed(1)}%"></i></div>
      ${r.error ? `<p class="hint err-text">${esc(r.error)}</p>` : ''}
      <ol class="sc-steps">${steps}</ol><div class="sc-log">${last}</div></div>`;
  }).join('') : '<div class="empty">还没有运行过场景</div>';
  $$('#scRuns .sc-steps li.cur').forEach(li => { const ol = li.parentElement; ol.scrollTop = li.offsetTop - ol.offsetTop - 60; });
}
let pollingScen = false;
async function pollScen() {
  if (document.hidden || pollingScen) return;
  pollingScen = true;
  try {
    const first = !SCEN;
    SCEN = await fetchJSON('api/scenarios', { cache: 'no-store' });
    if (first && !$('#scText').value) scSetText(scDraft || SCEN.example, '');
    renderScLib(); renderScRuns();
  } catch (_) { }
  finally { pollingScen = false; }
}
let scTick = 0;
setInterval(() => { if (!document.hidden && (page === 'scenario' || (SCEN && SCEN.runs.some(r => r.status === 'running')) || ++scTick % 5 === 0)) pollScen(); }, 1000);
onPage('scenario', pollScen);
pollScen();

// ---------------- 场景录制：记录页面上的操作，停止后生成场景脚本（YAML，可继续编辑 / 保存） ----------------
// 只记录对终端状态有影响的动作；时间间隔写成 after（秒，保留一位小数）。录制状态记在本页（刷新后结束）
const REC_SKIP = new Set(['setConfig', 'deleteArea', 'lockAlarm', 'stopStream', 'answer']);
const REC_BIT = { setAlarm: 'alarm', setStatus: 'status', setVideoAlarm: 'videoAlarm', setSignal: 'signal', setIO: 'io' };
let scRec = null;   // { term, started, last, steps: [] }
function recStep(type, p) {
  if (REC_SKIP.has(type)) return null;
  const pick = (...ks) => Object.fromEntries(ks.filter(k => p[k] !== undefined && p[k] !== '').map(k => [k, p[k]]));
  if (REC_BIT[type]) {
    if (type === 'setAlarm' && !p.on) return { action: 'clearAlarm', bit: p.bit };
    return { action: REC_BIT[type], bit: p.bit, on: p.on ? 1 : 0 };
  }
  switch (type) {
    case 'connect': case 'disconnect': case 'report': case 'clearAlarms': case 'logout': return { action: type };
    case 'setMotion': return p.mode ? { action: 'motion', ...pick('mode', 'speed') } : { action: 'speed', speed: p.speed };
    case 'setPosition': return { action: 'position', lng: +p.lng, lat: +p.lat };
    case 'send': return { action: 'send', msgId: String(p.msgId), ...(p.params && Object.keys(p.params).length ? { params: p.params } : {}) };
    case 'raw': return { action: 'raw', line: p.line };
    case 'setReplay': return { action: 'replay', ...p };
    default: return { action: type, ...p };
  }
}
// YAML 流式写法：每步一行 {k: v}；字符串统一加引号（JSON 字符串即合法 YAML）
const yv = v => typeof v === 'string' ? JSON.stringify(v) : v && typeof v === 'object' ? '{' + Object.entries(v).map(([k, x]) => `${k}: ${yv(x)}`).join(', ') + '}' : String(v);
function recYAML(r) {
  const t = TERMS.find(x => x.id === r.term);
  const lines = [`# 录制于 ${fmtDT(r.started)}，终端 #${r.term}${t ? ' ' + t.phone : ''}；after = 与上一步的间隔（秒）`,
    `name: ${JSON.stringify('录制-' + new Date(r.started).toLocaleString('zh-CN', { hour12: false }).replace(/\//g, '-'))}`,
    'terminal: ""', 'steps:'];
  if (!r.steps.length) lines.push('  - {at: 0, action: log, text: "（录制期间没有操作）"}');
  r.steps.forEach(st => lines.push('  - ' + yv(st)));
  lines.push('  # 可在需要处加断言，例如：', '  # - {after: 0, action: expectDown, msgId: "8001", within: 5}', '  # - {action: expectBit, kind: alarm, bit: 0, on: true}');
  return lines.join('\n') + '\n';
}
function syncRecBtn() {
  const on = !!scRec;
  $('#scRec').textContent = on ? `■ 停止录制（${scRec.steps.length} 步）` : '● 开始录制';
  $('#scRec').classList.toggle('danger', on);
  $('#scRec').setAttribute('aria-pressed', String(on));
  document.documentElement.classList.toggle('recording', on);
  $('#navScen').hidden = !on && !(SCEN?.runs || []).some(r => r.status === 'running');
  if (on) $('#navScen').textContent = '录';
}
onAct((type, p) => {
  if (!scRec || TID !== scRec.term) return;
  const st = recStep(type, p || {});
  if (!st) return;
  const now = Date.now();
  scRec.steps.push({ after: Math.round((now - scRec.last) / 100) / 10, ...st });
  scRec.last = now;
  syncRecBtn();
});
$('#scRec').onclick = () => {
  if (!scRec) {
    scRec = { term: TID, started: Date.now(), last: Date.now(), steps: [] };
    toast(`开始录制终端 #${TID} 上的操作，完成后回到「场景脚本」点“停止录制”`, 'info');
  } else {
    const text = recYAML(scRec), n = scRec.steps.length;
    scRec = null;
    scSetText(text, '');
    toast(`录制完成：${n} 步，已放入编辑框，可修改后保存到场景库`, 'ok');
  }
  syncRecBtn();
};
onSwitch(() => { if (scRec) { toast('已切换终端，录制只记录开始录制时的终端 #' + scRec.term, 'info'); } });
