// 地图（Leaflet，已内置在 web/vendor）：轨迹、车辆、区域。底图用 OSM 在线瓦片，离线时只是没有底图，轨迹和区域照常绘制
'use strict';
const AREA_KIND = { 1: '圆形区域', 2: '矩形区域', 3: '多边形区域', 4: '路线' };
const maps = {};

function carIcon(dir) {
  return L.divIcon({ className: 'car-icon', iconSize: [30, 30], iconAnchor: [15, 15],
    html: `<svg viewBox="0 0 30 30" style="transform:rotate(${dir}deg)"><circle cx="15" cy="15" r="13" fill="#1d1d1f" stroke="#c6f432" stroke-width="3"/>
      <path d="M15 6l6 14-6-3.5L9 20z" fill="#c6f432"/></svg>` });
}

function makeMap(id, opts = {}) {
  if (maps[id]) return maps[id];
  const el = document.getElementById(id);
  const m = L.map(el, { zoomControl: false, attributionControl: true, preferCanvas: false }).setView([23.1065, 113.3245], 15);
  L.control.zoom({ position: opts.zoomPos || 'topleft' }).addTo(m);
  L.control.scale({ imperial: false }).addTo(m);
  const tiles = L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', { maxZoom: 19, attribution: '© OpenStreetMap' }).addTo(m);
  let fails = 0, loaded = 0;
  const note = document.createElement('div');
  note.className = 'map-note'; note.hidden = true; note.textContent = '底图瓦片加载失败（离线？），轨迹与区域仍正常绘制';
  el.appendChild(note);
  tiles.on('tileerror', () => { if (++fails > 3 && !loaded) note.hidden = false; });
  tiles.on('tileload', () => { loaded++; note.hidden = true; });
  const o = {
    map: m, id,
    areas: L.featureGroup().addTo(m),
    track: L.polyline([], { color: '#1d1d1f', weight: 4, opacity: .8, lineCap: 'round' }).addTo(m),
    car: L.marker([23.1065, 113.3245], { icon: carIcon(0), zIndexOffset: 1000 }).addTo(m),
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
    const pts = (a.points || []).map(p => [p[0], p[1]]);
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
  const pos = [s.loc.lat, s.loc.lng];
  const tr = s.track.map(p => [p.lat, p.lng]);
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
    o.route.setLatLngs(r.points);
    const n = r.points.length;
    if (n) {
      L.circleMarker(r.points[0], { radius: 6, color: '#fff', weight: 2, fillColor: '#1fae5b', fillOpacity: 1 }).bindTooltip('起点 · ' + esc(r.name)).addTo(o.routeEnds);
      L.circleMarker(r.points[n - 1], { radius: 6, color: '#fff', weight: 2, fillColor: '#ef4444', fillOpacity: 1 }).bindTooltip('终点').addTo(o.routeEnds);
      if (S) fitMap(o, S);
    }
  } catch (_) { }
}

function fitMap(o, s, withAreas = true) {
  const b = L.latLngBounds([[s.loc.lat, s.loc.lng]]);
  if (o.route.getLatLngs().length) b.extend(o.route.getBounds());
  s.track.forEach(p => b.extend([p.lat, p.lng]));
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
      const { lat, lng } = e.latlng;
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
