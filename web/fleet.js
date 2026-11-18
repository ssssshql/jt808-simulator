// 多终端总览（/api/fleet）：表格 + 地图，轮询刷新，点击切换到该终端
'use strict';
let FLEET = null, flStatus = '';
const FL_COLOR = { online: '#1fae5b', connecting: '#f59e0b', reconnecting: '#f59e0b', offline: '#8e8e93' };
function fleetIcon(it) {
  const c = it.alarms.length ? '#ef4444' : FL_COLOR[it.status] || '#8e8e93';
  return L.divIcon({ className: 'fl-car' + (it.alarms.length ? ' alarm' : ''), iconSize: [26, 26], iconAnchor: [13, 13],
    html: `<svg viewBox="0 0 30 30" style="transform:rotate(${it.dir}deg)"><circle cx="15" cy="15" r="13" fill="#1d1d1f" stroke="${c}" stroke-width="3"/><path d="M15 6l6 14-6-3.5L9 20z" fill="${c}"/></svg><b>#${esc(it.id)} ${esc(it.plate || it.phone)}</b>` });
}
let flMap = null;
const flMarkers = {};
function fleetMap() {
  if (flMap) return flMap;
  flMap = makeMap('mapFleet', { follow: false });
  [flMap.car, flMap.track, flMap.route].forEach(l => flMap.map.removeLayer(l));   // 单终端图层不用
  flMap.fleet = L.featureGroup().addTo(flMap.map);
  return flMap;
}
function fitFleet() {
  const o = fleetMap(), ls = o.fleet.getLayers();
  if (!ls.length) return;
  const b = o.fleet.getBounds();
  if (b.getNorthEast().equals(b.getSouthWest())) o.map.setView(b.getCenter(), 15);
  else o.map.fitBounds(b.pad(0.2), { maxZoom: 16 });
}
function ago(ms) {
  if (!ms) return '-';
  const s = Math.max(0, Math.round((Date.now() - ms) / 1000));
  return s < 60 ? s + ' 秒前' : s < 3600 ? Math.floor(s / 60) + ' 分钟前' : fmtDT(ms).slice(5);
}
function renderFleet() {
  const v = FLEET; if (!v) return;
  $('#navFleet').hidden = !v.alarming; $('#navFleet').textContent = v.alarming || '';
  if (page !== 'fleet') return;
  const kpiHtml = [['在线 / 终端', `${v.online}<small>/ ${v.total}</small>`], ['报警中', `${v.alarming}<small>个终端</small>`],
    ['行驶中', `${v.terminals.filter(t => t.speed > 0.5).length}<small>个</small>`], ['场景运行', `${v.terminals.filter(t => t.scenario).length}<small>个</small>`]]
    .map(([a, b]) => `<div><span>${a}</span><b>${b}</b></div>`).join('');
  if (renderFleet.kpiSig !== kpiHtml) { renderFleet.kpiSig = kpiHtml; $('#flKpis').innerHTML = kpiHtml; }
  const q = $('#flFilter').value.trim().toLowerCase();
  const list = v.terminals.filter(t => (!q || [t.id, t.phone, t.plate].some(x => String(x).toLowerCase().includes(q))) &&
    (!flStatus || (flStatus === 'alarm' ? t.alarms.length : flStatus === 'offline' ? t.status !== 'online' : t.status === flStatus)));
  const bodyHtml = list.length ? list.map(t => `<tr data-term="${esc(t.id)}" tabindex="0" class="${t.id === TID ? 'on' : ''}">
    <td class="mono">#${esc(t.id)}${t.default ? ' <span class="tag">默认</span>' : ''}</td>
    <td><span class="fl-st ${esc(t.status)}" title="${esc(t.lastError || '')}"><i></i>${esc(ST[t.status] || t.status)}${t.status === 'online' && t.onlineSince ? ' · ' + dur(Date.now() - t.onlineSince) : ''}</span>${t.scenario ? ` <span class="tag info" title="场景">${esc(t.scenario)}</span>` : ''}</td>
    <td class="mono">${esc(t.phone)}</td><td>${esc(t.plate)}</td>
    <td class="mono">${t.lng.toFixed(6)}, ${t.lat.toFixed(6)}</td>
    <td class="r mono">${t.speed.toFixed(1)}<span class="muted"> km/h</span></td>
    <td class="al">${t.alarms.length ? esc(t.alarms.join('、')) : '<span class="muted">-</span>'}</td>
    <td>${t.last ? `<span class="mono">${t.last.dir === 'up' ? '↑' : '↓'} ${esc(t.last.msgId)}</span> ${esc(t.last.name)} <span class="muted">${ago(t.last.t)}</span>` : '<span class="muted">-</span>'}</td>
    <td class="r mono">${t.up} / ${t.down}</td>
    <td class="mono muted">${esc(t.host)}:${t.port}${t.transport === 'udp' ? '/UDP' : ''} · ${esc(t.version)}</td></tr>`).join('')
    : '<tr><td colspan="10" class="empty">没有符合条件的终端</td></tr>';
  if (renderFleet.bodySig !== bodyHtml) { renderFleet.bodySig = bodyHtml; $('#flBody').innerHTML = bodyHtml; }
  // 地图
  const o = fleetMap(), seen = new Set();
  v.terminals.forEach(t => {
    seen.add(t.id);
    const pos = toMap([t.lat, t.lng]), sig = [t.status, t.dir, t.alarms.length, t.plate, t.phone].join('|');
    let mk = flMarkers[t.id];
    if (!mk) {
      mk = flMarkers[t.id] = L.marker(pos, { icon: fleetIcon(t) }).addTo(o.fleet);
      mk.on('click', () => { setTerm(mk.tid); go('overview'); });
    } else mk.setLatLng(pos);
    mk.tid = t.id;
    if (mk.sig !== sig) { mk.setIcon(fleetIcon(t)); mk.sig = sig; }
    const tip = `#${esc(t.id)} ${esc(t.phone)} ${esc(t.plate)}<br>${esc(ST[t.status] || t.status)} · ${t.speed.toFixed(1)} km/h${t.alarms.length ? '<br>' + esc(t.alarms.join('、')) : ''}`;
    if (mk.getTooltip()) mk.setTooltipContent(tip); else mk.bindTooltip(tip);
  });
  Object.keys(flMarkers).forEach(id => { if (!seen.has(id)) { o.fleet.removeLayer(flMarkers[id]); delete flMarkers[id]; } });
  $('#flMapInfo').textContent = `${v.total} 个终端`;
  if (!o.fleetFitted) { o.fleetFitted = true; fitFleet(); }
}
let pollingFleet = false;
async function pollFleet() {
  if (document.hidden || pollingFleet) return;
  pollingFleet = true;
  try { FLEET = await fetchJSON('api/fleet', { cache: 'no-store' }); renderFleet(); } catch (_) { }
  finally { pollingFleet = false; }
}
$('#flBody').onclick = e => { const r = e.target.closest('[data-term]'); if (r) { setTerm(r.dataset.term); go('overview'); } };
$('#flBody').addEventListener('keydown', e => {
  if (e.key !== 'Enter' && e.key !== ' ') return;
  const r = e.target.closest('[data-term]');
  if (r) { e.preventDefault(); setTerm(r.dataset.term); go('overview'); }
});
$('#flFilter').oninput = renderFleet;
$('#flSt').onclick = e => {
  const b = e.target.closest('[data-s]'); if (!b) return;
  flStatus = b.dataset.s; $$('#flSt button').forEach(x => x.classList.toggle('on', x === b)); renderFleet();
};
$('#flFit').onclick = fitFleet;
$('#flExport').onclick = () => openExport('all');
async function fleetAll(type, btn) {
  btn.disabled = true;
  try {
    const ids = (FLEET?.terminals || []).filter(t => type === 'connect' ? t.status === 'offline' : t.status !== 'offline').map(t => t.id);
    const rs = await Promise.allSettled(ids.map(id => fetchJSON(`api/t/${encodeURIComponent(id)}/action`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ type }) })));
    const ok = rs.filter(r => r.status === 'fulfilled' && r.value?.ok).length;
    if (ok === ids.length) toast(`${type === 'connect' ? '上线' : '断开'} ${ok} 个终端`, 'ok');
    else toast(`${type === 'connect' ? '上线' : '断开'}完成：成功 ${ok}/${ids.length} 个`, 'err');
    pollFleet(); loadTerms();
  } catch (e) {
    toast('批量操作失败：' + e.message);
  } finally {
    btn.disabled = false;
  }
}
$('#flAllOn').onclick = e => fleetAll('connect', e.currentTarget);
$('#flAllOff').onclick = e => fleetAll('disconnect', e.currentTarget);
onPage('fleet', () => { fleetMap(); setTimeout(() => { flMap.map.invalidateSize(); pollFleet(); }, 30); });
setInterval(() => { if (page === 'fleet') pollFleet(); }, 2000);
setInterval(() => { if (page !== 'fleet') pollFleet(); }, 10000);   // 侧栏报警角标
pollFleet();
