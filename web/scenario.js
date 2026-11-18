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
  $('#navScen').hidden = !running; $('#navScen').textContent = running || '';
  if (page !== 'scenario') return;
  const t = TERMS.find(x => x.id === TID);
  $('#scTarget').textContent = t ? `未指定 terminal 时在当前终端 #${t.id} ${t.phone} 上运行` : '';
  $('#scStop').disabled = !running;
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
      <span class="muted mono">${Math.min(r.step, r.total)}/${r.total} 步 · ${dur(r.elapsed * 1000)}${r.failures ? ` · 失败 ${r.failures}` : ''}</span></div>
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
