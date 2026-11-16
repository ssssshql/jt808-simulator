// 地图（Leaflet，已内置在 web/vendor）：轨迹、车辆、区域。底图用高德在线瓦片（标准 / 卫星），离线时只是没有底图，轨迹和区域照常绘制。
// 终端坐标是 WGS-84，高德瓦片是 GCJ-02（国测局加偏）：画到地图上的点先 toMap() 转换，地图上点选的位置再 fromMap() 转回 WGS-84。
'use strict';
const AREA_KIND = { 1: '圆形区域', 2: '矩形区域', 3: '多边形区域', 4: '路线' };
const maps = {};

// ---- WGS-84 ⇄ GCJ-02（中国境外不加偏） ----
const GA = 6378245.0, GEE = 0.00669342162296594323;
function outOfChina(lat, lng) { return lng < 72.004 || lng > 137.8347 || lat < 0.8293 || lat > 55.8271; }
function gcjDelta(lat, lng) {
  const x = lng - 105, y = lat - 35, PI = Math.PI;
  let dLat = -100 + 2 * x + 3 * y + 0.2 * y * y + 0.1 * x * y + 0.2 * Math.sqrt(Math.abs(x));
  dLat += (20 * Math.sin(6 * x * PI) + 20 * Math.sin(2 * x * PI)) * 2 / 3;
  dLat += (20 * Math.sin(y * PI) + 40 * Math.sin(y / 3 * PI)) * 2 / 3;
  dLat += (160 * Math.sin(y / 12 * PI) + 320 * Math.sin(y * PI / 30)) * 2 / 3;
  let dLng = 300 + x + 2 * y + 0.1 * x * x + 0.1 * x * y + 0.1 * Math.sqrt(Math.abs(x));
  dLng += (20 * Math.sin(6 * x * PI) + 20 * Math.sin(2 * x * PI)) * 2 / 3;
  dLng += (20 * Math.sin(x * PI) + 40 * Math.sin(x / 3 * PI)) * 2 / 3;
  dLng += (150 * Math.sin(x / 12 * PI) + 300 * Math.sin(x / 30 * PI)) * 2 / 3;
  const rad = lat / 180 * PI, m = 1 - GEE * Math.sin(rad) ** 2, sm = Math.sqrt(m);
  return [dLat * 180 / ((GA * (1 - GEE)) / (m * sm) * PI), dLng * 180 / (GA / sm * Math.cos(rad) * PI)];
}
function toMap(p) {           // [lat, lng] WGS-84 → GCJ-02
  const lat = +p[0], lng = +p[1];
  if (outOfChina(lat, lng)) return [lat, lng];
  const d = gcjDelta(lat, lng); return [lat + d[0], lng + d[1]];
}
function fromMap(lat, lng) {  // GCJ-02 → WGS-84（迭代求逆，误差 < 1e-7°）
  if (outOfChina(lat, lng)) return [lat, lng];
  let w = [lat, lng];
  for (let i = 0; i < 5; i++) { const g = toMap(w); w = [w[0] - (g[0] - lat), w[1] - (g[1] - lng)]; }
  return w;
}

// 底图：高德标准 / 高德卫星（带路网注记），选择记在本机
const AMAP_SUB = ['1', '2', '3', '4'];
const BASES = {
  std: () => L.tileLayer('https://webrd0{s}.is.autonavi.com/appmaptile?lang=zh_cn&size=1&scale=1&style=8&x={x}&y={y}&z={z}', { subdomains: AMAP_SUB, maxZoom: 18, attribution: '© 高德地图' }),
  sat: () => L.layerGroup([
    L.tileLayer('https://webst0{s}.is.autonavi.com/appmaptile?style=6&x={x}&y={y}&z={z}', { subdomains: AMAP_SUB, maxZoom: 18, attribution: '© 高德地图' }),
    L.tileLayer('https://wprd0{s}.is.autonavi.com/appmaptile?x={x}&y={y}&z={z}&lang=zh_cn&size=1&scl=1&style=8&ltype=11', { subdomains: AMAP_SUB, maxZoom: 18 }),
  ]),
};
let baseKind = localStorage.getItem('jt808.mapBase') === 'sat' ? 'sat' : 'std';
function tileLayers(layer) { return layer.getLayers ? layer.getLayers() : [layer]; }

function carIcon(dir) {
  return L.divIcon({ className: 'car-icon', iconSize: [30, 30], iconAnchor: [15, 15],
    html: `<svg viewBox="0 0 30 30" style="transform:rotate(${dir}deg)"><circle cx="15" cy="15" r="13" fill="#1d1d1f" stroke="#c6f432" stroke-width="3"/>
      <path d="M15 6l6 14-6-3.5L9 20z" fill="#c6f432"/></svg>` });
}

function makeMap(id, opts = {}) {
  if (maps[id]) return maps[id];
  const el = document.getElementById(id);
  const m = L.map(el, { zoomControl: false, attributionControl: true, preferCanvas: false, maxZoom: 18 }).setView(toMap([23.1065, 113.3245]), 15);
  m.attributionControl.setPrefix(false);
  L.control.zoom({ position: opts.zoomPos || 'topleft' }).addTo(m);
  L.control.scale({ imperial: false }).addTo(m);
  let fails = 0, loaded = 0;
  const note = document.createElement('div');
  note.className = 'map-note'; note.hidden = true; note.textContent = '底图瓦片加载失败（离线？），轨迹与区域仍正常绘制';
  el.appendChild(note);
  let base = null;
  const setBase = k => {
    if (base) m.removeLayer(base);
    base = BASES[k]().addTo(m); base.bringToBack?.();
    fails = loaded = 0; note.hidden = true; el.classList.toggle('sat', k === 'sat');
    tileLayers(base).forEach(t => {
      t.on('tileerror', () => { if (++fails > 3 && !loaded) note.hidden = false; });
      t.on('tileload', () => { loaded++; note.hidden = true; });
    });
    sw.querySelectorAll('button').forEach(b => b.classList.toggle('on', b.dataset.base === k));
  };
  // 底图切换按钮（标准 / 卫星），所有地图同步
  const Sw = L.Control.extend({ options: { position: opts.zoomPos === 'topright' ? 'bottomright' : 'topright' }, onAdd() { return sw; } });
  const sw = L.DomUtil.create('div', 'map-base');
  sw.innerHTML = '<button data-base="std">标准</button><button data-base="sat">卫星</button>';
  L.DomEvent.disableClickPropagation(sw);
  sw.onclick = e => {
    const b = e.target.closest('[data-base]'); if (!b || b.dataset.base === baseKind) return;
    baseKind = b.dataset.base; localStorage.setItem('jt808.mapBase', baseKind);
    Object.values(maps).forEach(o => o.setBase(baseKind));
  };
  new Sw().addTo(m);
  setBase(baseKind);
  const o = {
    map: m, id,
    areas: L.featureGroup().addTo(m),
    track: L.polyline([], { color: '#1d1d1f', weight: 4, opacity: .8, lineCap: 'round' }).addTo(m),
    setBase,
    car: L.marker(toMap([23.1065, 113.3245]), { icon: carIcon(0), zIndexOffset: 1000 }).addTo(m),
    route: L.polyline([], { color: '#f97316', weight: 4, opacity: .75, lineJoin: 'round' }).addTo(m),
    routeEnds: L.featureGroup().addTo(m), routeRev: null,
    areaSig: '', fitted: false, follow: opts.follow !== false, dir: -1,
  };
  maps[id] = o;
  return o;
}

function drawAreas(o, areas) {
  const sig = JSON.stringify(areas.map(a => [a.kind, a.id, a.attr, a.points, a.radius]));
  if (sig === o.areaSig) return false;
  o.areaSig = sig;
  o.areas.clearLayers();
  areas.forEach(a => {
    const pts = (a.points || []).map(p => toMap(p));
    if (!pts.length) return;
    const col = a.kind === 4 ? '#5b5bd6' : '#1fae5b';
    const style = { color: col, weight: 2, fillColor: col, fillOpacity: .12 };
    let layer;
    if (a.kind === 1) layer = L.circle(pts[0], { ...style, radius: a.radius });
    else if (a.kind === 2 && pts.length >= 2) layer = L.rectangle([pts[0], pts[1]], style);
    else if (a.kind === 3) layer = L.polygon(pts, style);
    else layer = L.polyline(pts, { color: col, weight: 5, opacity: .7, dashArray: '8 6' });
    layer.bindPopup(`<b>${esc(AREA_KIND[a.kind])} ${a.id}</b><br>${esc(a.name || '未命名')}<br>属性 0x${hex4(a.attr)}${a.maxSpeed ? '<br>限速 ' + a.maxSpeed + ' km/h' : ''}`);
    layer.addTo(o.areas);
    if (a.kind === 4) pts.forEach((p, i) => L.circleMarker(p, { radius: 4, color: col, fillColor: '#fff', fillOpacity: 1, weight: 2 })
      .bindTooltip(`拐点 ${i + 1}${a.segs && a.segs[i] ? ' · 路段 ' + a.segs[i].id : ''}`).addTo(o.areas));
  });
  return true;
}

function updateMap(o, s, { fitAreas = false } = {}) {
  const pos = toMap([s.loc.lat, s.loc.lng]);
  const tr = s.track.map(p => toMap([p.lat, p.lng]));
  o.track.setLatLngs(tr.length ? tr.concat([pos]) : []);
  const tc = document.documentElement.dataset.theme === 'dark' ? '#c6f432' : '#1d1d1f';
  if (o.tc !== tc) { o.track.setStyle({ color: tc }); o.tc = tc; }
  o.car.setLatLng(pos);
  if (o.dir !== s.loc.dir) { o.car.setIcon(carIcon(s.loc.dir)); o.dir = s.loc.dir; }
  const rp = s.replay || {};
  const rev = rp.loaded ? rp.rev : 0;
  if (o.routeRev !== rev) { o.routeRev = rev; loadRoute(o, rev); }
  const changed = drawAreas(o, s.areas || []);
  if (!o.fitted || changed) { fitMap(o, s, true); o.fitted = true; }
  else if (o.follow && !o.map.getBounds().pad(-0.15).contains(pos)) o.map.panTo(pos, { animate: true });
}

// 导入的回放轨迹（橙色），起点绿点、终点红点
async function loadRoute(o, rev) {
  o.route.setLatLngs([]); o.routeEnds.clearLayers();
  if (!rev) return;
  try {
    const r = await (await fetch(api('route'), { cache: 'no-store' })).json();
    if (o.routeRev !== rev || !r.loaded) return;
    const pts = r.points.map(toMap);
    o.route.setLatLngs(pts);
    const n = pts.length;
    if (n) {
      L.circleMarker(pts[0], { radius: 6, color: '#fff', weight: 2, fillColor: '#1fae5b', fillOpacity: 1 }).bindTooltip('起点 · ' + esc(r.name)).addTo(o.routeEnds);
      L.circleMarker(pts[n - 1], { radius: 6, color: '#fff', weight: 2, fillColor: '#ef4444', fillOpacity: 1 }).bindTooltip('终点').addTo(o.routeEnds);
      if (S) fitMap(o, S);
    }
  } catch (_) { }
}

function fitMap(o, s, withAreas = true) {
  const b = L.latLngBounds([toMap([s.loc.lat, s.loc.lng])]);
  if (o.route.getLatLngs().length) b.extend(o.route.getBounds());
  s.track.forEach(p => b.extend(toMap([p.lat, p.lng])));
  if (withAreas && o.areas.getLayers().length) b.extend(o.areas.getBounds());
  if (b.getNorthEast().equals(b.getSouthWest())) o.map.setView(b.getCenter(), 16);
  else o.map.fitBounds(b.pad(0.15), { maxZoom: 17 });
}

// 页面切换时创建/刷新地图尺寸
function showMap(id, opts) {
  const o = makeMap(id, opts);
  setTimeout(() => { o.map.invalidateSize(); if (S) updateMap(o, S, { fitAreas: id === 'mapAreas' }); }, 30);
  return o;
}
onPage('overview', () => showMap('mapOv', { zoomPos: 'topright' }));
onPage('motion', () => {
  const o = showMap('mapMotion');
  if (!o.clickBound) {
    o.clickBound = true;
    o.map.on('click', e => {
      if (!$('#pickMap').checked) return;
      const [lat, lng] = fromMap(e.latlng.lat, e.latlng.lng);
      act('setPosition', { lng: lng.toFixed(6), lat: lat.toFixed(6) }, null, `位置已设定 ${lng.toFixed(6)}, ${lat.toFixed(6)}`);
    });
  }
});
onPage('areas', () => showMap('mapAreas', { follow: false }));
onState(s => {
  if (maps.mapOv) updateMap(maps.mapOv, s);   // 主屏地图常驻
  if (page === 'motion' && maps.mapMotion) updateMap(maps.mapMotion, s);
  if (page === 'areas' && maps.mapAreas) updateMap(maps.mapAreas, s, { fitAreas: true });
});
onSwitch(() => Object.values(maps).forEach(o => { o.fitted = false; o.areaSig = null; o.routeRev = null; o.track.setLatLngs([]); }));
