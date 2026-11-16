// 终端参数：按分组逐项编辑。结构体参数逐字段编辑（Go 端编解码，存储格式不变），按位参数用复选框
'use strict';
let P = null;             // /api/params
let pGroup = '通信';
const pDirty = new Map(); // id -> {value} | {fields}
let pSig = '';

async function reloadParams() {
  try {
    const r = await fetch(api('params'));
    P = await r.json();
    pDirty.clear();
    renderParamsPage();
  } catch (e) { toast('读取参数失败：' + e.message); }
}
window.reloadParams = reloadParams;
onSwitch(() => { pSig = ''; P = null; pDirty.clear(); if (page === 'params') reloadParams(); });
onPage('params', reloadParams);

// 平台 8103 等修改参数后自动刷新（有未保存修改时只提示）
onState(s => {
  const sig = JSON.stringify(s.params.map(p => p.id + '=' + p.value));
  if (pSig && sig !== pSig && page === 'params') {
    if (pDirty.size) toast('参数已被平台或其它操作修改，保存前可先“放弃修改”刷新', 'info');
    else reloadParams();
  }
  pSig = sig;
});

const num = v => (typeof v === 'number' ? v : Number(v));
function bitGrid(bits, value, attrs, width = 32) {
  return `<div class="bitgrid">${Object.entries(bits).filter(([b]) => +b < width).map(([b, n]) =>
    `<label title="bit${b} ${esc(n)}"><input type="checkbox" ${attrs} data-bit="${b}" ${(Math.floor(num(value) / 2 ** b) % 2) ? 'checked' : ''}><em>${b}</em><span>${esc(n)}</span></label>`).join('')}</div>`;
}
function optSelect(opts, value, attrs) {
  const has = opts.some(o => String(o.v) === String(value));
  return `<select ${attrs}>${has ? '' : `<option value="${esc(value)}" selected>${esc(value)}（非标准值）</option>`}${opts.map(o =>
    `<option value="${o.v}" ${String(o.v) === String(value) ? 'selected' : ''}>${o.v} · ${esc(o.l)}</option>`).join('')}</select>`;
}
function withUnit(ctl, unit) { return unit ? `<div class="input-unit">${ctl}<span>${esc(unit)}</span></div>` : ctl; }

// ---------- 结构体字段 ----------
function sfInput(f, val, attrs) {
  if (f.subs) return f.subs.map(s => sfField({ ...s, size: 4 }, val[s.key], `${attrs} data-k="${s.key}"`)).join('');
  return sfField(f, val[f.key], `${attrs} data-k="${f.key}"`);
}
function sfField(f, v, attrs) {
  const label = `<label>${esc(f.name || f.key)}${f.auto ? '（自动）' : ''}</label>`;
  const help = f.help ? `<span class="muted">${esc(f.help)}</span>` : '';
  if (f.bits) return `<div class="sf full">${label}${bitGrid(f.bits, v ?? 0, attrs + ' data-bits="1"', f.width || (f.size || 4) * 8)}${help}</div>`;
  if (f.opts) return `<div class="sf">${label}${optSelect(f.opts, v ?? 0, attrs)}${help}</div>`;
  if (f.kind === 'str' || f.kind === 'hex' || f.kind === 'bcd')
    return `<div class="sf">${label}<input ${attrs} data-kind="${f.kind}" class="${f.kind === 'str' ? '' : 'mono'}" value="${esc(v ?? '')}" maxlength="${f.size * 2}">${help}</div>`;
  const max = f.max || (f.width ? 2 ** f.width - 1 : 2 ** (8 * f.size) - 1);
  const txt = f.key === 'canId';
  const shown = txt ? '0x' + Number(v ?? 0).toString(16).toUpperCase() : (v ?? 0);
  return `<div class="sf">${label}${withUnit(`<input ${attrs} ${txt ? '' : `type="number" min="${f.min || 0}" max="${max}"`} class="mono" value="${esc(shown)}" ${f.auto ? 'disabled' : ''} data-min="${f.min || 0}" data-max="${max}">`, f.unit)}${help}</div>`;
}
function structEditor(p, fields) {
  const s = p.schema;
  let h = `<div class="struct" data-struct="${p.id}"><div class="sgrid">${s.fields.map(f => sfInput(f, fields, '')).join('')}</div>`;
  if (s.item) {
    const items = fields.items || [];
    h += `<div class="items">${items.map((it, i) => `<div class="item" data-item="${i}"><div class="item-h">${esc(s.itemName || '项')} ${i + 1}<div class="grow"></div>
      <button class="btn xs ghost danger" data-delitem="${i}">删除</button></div><div class="sgrid">${s.item.map(f => sfInput(f, it, `data-i="${i}"`)).join('')}</div></div>`).join('')}</div>
      <div class="btns"><button class="btn xs" data-additem="${p.id}" ${s.maxItems && items.length >= s.maxItems ? 'disabled' : ''}>＋ 添加${esc(s.itemName || '项')}</button></div>`;
  }
  if (fields._rest) h += `<div class="sgrid" style="margin-top:10px"><div class="sf full"><label>扩展字节（超出标准定义部分，原样保留）</label><input class="mono" data-k="_rest" data-kind="hex" value="${esc(fields._rest)}"></div></div>`;
  h += `<div class="hexv">存储值：${esc(p.value || '(空)')}</div></div>`;
  return h;
}
function collectStruct(box, p) {
  const out = {}, items = [];
  const put = (o, k, v) => { o[k] = v; };
  $$('[data-k]', box).forEach(el => {
    const i = el.dataset.i, tgt = i != null ? (items[i] = items[i] || {}) : out, k = el.dataset.k;
    if (el.dataset.bits) { tgt[k] = (tgt[k] || 0) + (el.checked ? 2 ** +el.dataset.bit : 0); return; }
    if (el.dataset.kind) { put(tgt, k, el.value.trim()); return; }
    if (el.disabled) return;
    const v = el.value.trim();
    put(tgt, k, /^0x/i.test(v) ? v : Number(v));
  });
  if (p.schema.item) out.items = items.filter(Boolean);
  return out;
}
function defaultItem(schema) {
  const it = {};
  schema.item.forEach(f => { if (f.subs) f.subs.forEach(s => it[s.key] = s.def || 0); else it[f.key] = f.def ?? (f.kind ? '' : 0); });
  return it;
}

// ---------- 单个参数 ----------
function curValue(p) { const d = pDirty.get(p.id); return d && 'value' in d ? d.value : p.value; }
function curFields(p) { const d = pDirty.get(p.id); return d && d.fields ? d.fields : p.fields; }

function paramRow(p) {
  const v = curValue(p), dirty = pDirty.has(p.id);
  const wide = p.schema || p.bits;
  let ctl = '';
  const attrs = `data-pid="${p.id}"${p.readOnly ? ' disabled title="只支持查询"' : ''}`;
  if (p.schema) ctl = '';
  else if (p.bits) ctl = `<span class="mono muted">0x${hex8(v)}</span>`;
  else if (p.opts) ctl = optSelect(p.opts, v, attrs);
  else if (p.type === 'STRING') ctl = `<input ${attrs} value="${esc(v)}" placeholder="${p.def ? '默认 ' + esc(p.def) : '未设置'}">`;
  else if (p.type === 'BYTES') ctl = `<input ${attrs} class="mono" value="${esc(v)}" placeholder="hex">`;
  else ctl = withUnit(`<input ${attrs} type="number" class="mono" value="${esc(v)}" min="${p.min ?? 0}" ${p.max != null ? `max="${p.max}"` : ''} data-min="${p.min ?? 0}" data-max="${p.max ?? ({ DWORD: 4294967295, WORD: 65535, BYTE: 255 })[p.type]}">`, p.unit);
  const desc = [p.help, p.min != null && p.max != null ? `范围 ${p.min}~${p.max}` : '', p.schema ? '' : `默认 ${p.def === '' ? '空' : (p.opts?.find(o => String(o.v) === p.def)?.l || p.def)}`].filter(Boolean).join('；');
  const actions = p.readOnly ? '<span class="tag">只读</span>' : `${p.known ? `<button class="btn xs ghost reset" data-pdef="${p.id}" title="恢复该参数默认值">默认</button>` : `<button class="btn xs ghost danger" data-pdel="${p.id}">删除</button>`}`;
  const body = p.schema ? structEditor(p, curFields(p)) : p.bits ? bitGrid(p.bits, v, `data-pbit="${p.id}"`, ({ DWORD: 32, WORD: 16, BYTE: 8 })[p.type]) : '';
  return `<div class="prow ${wide ? 'wide' : ''} ${dirty ? 'dirty' : ''}" data-row="${p.id}">
    <div class="pn"><div class="t"><span class="id">0x${p.id}</span>${esc(p.name)}<span class="tag">${p.type}${p.unit && wide ? ' · ' + esc(p.unit) : ''}</span>${p.std ? `<span class="tag info">${esc(p.std)}</span>` : ''}
      ${wide ? `<div class="grow"></div>${ctl}${actions}` : ''}</div><div class="d">${esc(desc)}</div>${body}</div>
    ${wide ? '' : `<div class="pv">${ctl}${actions}</div>`}</div>`;
}

function renderParamsPage() {
  if (!P) return;
  const f = $('#pFilter').value.trim().toLowerCase();
  const counts = {};
  P.params.forEach(p => counts[p.group] = (counts[p.group] || 0) + 1);
  const groups = P.groups.filter(g => counts[g]);
  if (!groups.includes(pGroup)) pGroup = groups[0];
  $('#pGroups').innerHTML = groups.map(g => `<button class="${!f && g === pGroup ? 'on' : ''}" data-g="${g}">${g}<em>${counts[g]}</em></button>`).join('');
  const list = P.params.filter(p => f ? (p.id + p.name + (p.help || '')).toLowerCase().includes(f) : p.group === pGroup);
  const byGroup = {};
  list.forEach(p => (byGroup[p.group] = byGroup[p.group] || []).push(p));
  $('#pList').innerHTML = Object.entries(byGroup).map(([g, ps]) => `<div class="card pgroup"><h2>${g}<span class="tag">${ps.length} 项</span><div class="grow"></div>
      ${g === 'CAN' ? '<button class="btn xs" id="addCan">＋ 添加 CAN ID 采集项（0x0111~0x01FF）</button>' : ''}</h2>
      <div class="prows">${ps.map(paramRow).join('')}</div></div>`).join('') || '<div class="card empty">没有匹配的参数</div>';
  updateSaveBar();
}

function updateSaveBar() {
  $('#saveBar').hidden = !pDirty.size;
  $('#saveBarText').textContent = `${pDirty.size} 项参数已修改：${[...pDirty.keys()].map(id => '0x' + id).join('、')}`;
}

function markRow(id) {
  const row = $(`[data-row="${id}"]`);
  if (row) row.classList.toggle('dirty', pDirty.has(id));
  updateSaveBar();
}

$('#pGroups').onclick = e => { const b = e.target.closest('[data-g]'); if (b) { pGroup = b.dataset.g; $('#pFilter').value = ''; renderParamsPage(); } };
$('#pFilter').oninput = renderParamsPage;

function findP(id) { return P.params.find(p => p.id === id); }

function checkRange(el) {
  if (el.dataset.max == null || el.disabled || el.type !== 'number') return true;
  const v = el.value.trim(), n = Number(v);
  const bad = v === '' || !Number.isInteger(n) || n < +el.dataset.min || n > +el.dataset.max;
  el.classList.toggle('invalid', bad);
  el.title = bad ? `取值范围 ${el.dataset.min}~${el.dataset.max}` : '';
  return !bad;
}

$('#pList').addEventListener('input', e => onParamEdit(e.target));
$('#pList').addEventListener('change', e => onParamEdit(e.target));
function onParamEdit(el) {
  const row = el.closest('[data-row]');
  if (!row) return;
  const p = findP(row.dataset.row);
  checkRange(el);
  if (el.dataset.pid) {
    if (String(el.value) === String(p.value)) pDirty.delete(p.id); else pDirty.set(p.id, { value: el.value });
  } else if (el.dataset.pbit) {
    let v = 0;
    $$(`[data-pbit="${p.id}"]`, row).forEach(c => { if (c.checked) v += 2 ** +c.dataset.bit; });
    if (String(v) === p.value) pDirty.delete(p.id); else pDirty.set(p.id, { value: String(v) });
    const hx = $('.pn .t .mono', row); if (hx) hx.textContent = '0x' + hex8(v);
  } else if (el.closest('[data-struct]')) {
    pDirty.set(p.id, { fields: collectStruct(el.closest('[data-struct]'), p) });
  }
  markRow(p.id);
}

$('#pList').addEventListener('click', e => {
  const add = e.target.closest('[data-additem]'), del = e.target.closest('[data-delitem]');
  const row = e.target.closest('[data-row]');
  if ((add || del) && row) {
    const p = findP(row.dataset.row), box = row.querySelector('[data-struct]');
    const fields = collectStruct(box, p);
    if (add) { const it = defaultItem(p.schema); if ('logic' in it) it.logic = fields.items.length + 1; if ('phys' in it) it.phys = fields.items.length + 1; fields.items.push(it); }
    else fields.items.splice(+del.dataset.delitem, 1);
    pDirty.set(p.id, { fields });
    row.outerHTML = paramRow(p);
    updateSaveBar();
    return;
  }
  const d = e.target.closest('[data-pdef]');
  if (d) {
    const p = findP(d.dataset.pdef);
    if (p.value === p.def) pDirty.delete(p.id);
    else pDirty.set(p.id, p.schema ? { value: p.def, fields: p.defFields } : { value: p.def });
    $(`[data-row="${p.id}"]`).outerHTML = paramRow(p);
    updateSaveBar();
    return;
  }
  const x = e.target.closest('[data-pdel]');
  if (x) confirmBox(`删除参数 0x${x.dataset.pdel}？`).then(ok => ok && act('deleteParam', { id: x.dataset.pdel }, x, '参数已删除').then(r => r && reloadParams()));
  if (e.target.id === 'addCan') addCanParam();
});

async function addCanParam() {
  const used = new Set(P.params.map(p => parseInt(p.id, 16)));
  let id = 0x0111;
  while (used.has(id) && id <= 0x01FF) id++;
  if (id > 0x01FF) return toast('0x0111~0x01FF 已全部使用');
  const base = findP('0110');
  const p = { ...base, id: hex4(id), name: `CAN 总线 ID 单独采集设置 #${id - 0x0110 + 1}`, value: base.def, known: true, fields: base.defFields };
  P.params.push(p);
  pDirty.set(p.id, { fields: p.fields });
  renderParamsPage();
  toast(`已添加 0x${p.id}，填写后保存`, 'info');
}

$('#pDiscard').onclick = reloadParams;
$('#pSave').onclick = async e => {
  const bad = $$('#pList input.invalid');
  if (bad.length) { bad[0].focus(); return toast('有字段超出取值范围，请先修正'); }
  const values = {}, fields = {};
  for (const [id, d] of pDirty) {
    if (d.fields) fields[id] = d.fields; else values[id] = d.value;
  }
  if (await act('setParams', { values, fields }, e.target, `已保存 ${pDirty.size} 项参数`)) reloadParams();
};
$('#pReset').onclick = async e => {
  if (!(await confirmBox(e.target.dataset.confirm))) return;
  if (await act('resetParams', {}, e.target, '已恢复默认参数')) reloadParams();
};
