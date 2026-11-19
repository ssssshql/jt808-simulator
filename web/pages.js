// 主屏（Bento）与弹出面板各页：运动与位置、状态与报警、上行报文、主动安全、多媒体与视频、区域、终端配置
'use strict';

// ================= 主屏（Bento） =================
const MODE = { stop: '停车', drive: '随机行驶', circle: '绕圈', route: '沿路线', track: '轨迹回放' };
const KIND_ICON = { text: '文本', question: '提问', event: '事件', menu: '点播', info: '信息', call: '回拨', phonebook: '电话本', control: '控制', param: '参数',
  upgrade: '升级', area: '区域', media: '多媒体', av: '音视频', alarm: '报警', attach: '附件', recorder: '记录仪', ptz: '云台', rsa: 'RSA', passthrough: '透传', track: '跟踪' };
const speedLimit = s => +(s.params.find(p => p.id === '0055')?.value || 0);

// 深色车速环：0~160 km/h 占 3/4 圆；超速报警时变红，限速位置画红色刻度
function renderRing(s) {
  if (!$('#ring')) return;
  const v = s.loc.speed, max = 160, r = 86, C = 2 * Math.PI * r, f = Math.min(v, max) / max * .75;
  const over = s.alarms.find(b => b.bit === 1)?.on, lim = speedLimit(s);
  let mark = '';
  if (lim > 0 && lim <= max) {
    const a = (135 + 270 * lim / max) * Math.PI / 180;
    mark = `<line x1="${(100 + 74 * Math.cos(a)).toFixed(1)}" y1="${(100 + 74 * Math.sin(a)).toFixed(1)}" x2="${(100 + 98 * Math.cos(a)).toFixed(1)}" y2="${(100 + 98 * Math.sin(a)).toFixed(1)}" stroke="#ef4444" stroke-width="3" stroke-linecap="round"/>`;
  }
  $('#ring').innerHTML = `<svg viewBox="0 0 200 200"><g transform="rotate(135 100 100)">
    <circle cx="100" cy="100" r="${r}" fill="none" stroke="#2c2c2e" stroke-width="14" stroke-dasharray="${(C * .75).toFixed(1)} ${C.toFixed(1)}" stroke-linecap="round"/>
    ${v > 0.2 ? `<circle cx="100" cy="100" r="${r}" fill="none" stroke="${over ? '#ef4444' : '#c6f432'}" stroke-width="14" stroke-dasharray="${(C * f).toFixed(1)} ${C.toFixed(1)}" stroke-linecap="round"/>` : ''}</g>${mark}</svg>
    <div class="c"><b class="${over ? 'over' : ''}">${v.toFixed(0)}</b><span>km/h</span></div>`;
}

// 主屏状态位 / 报警位：常用位 + 其它已置位的位；完整列表在“状态与报警”
const OV_STATUS = [0, 1, 4, 10, 11, 12, 13, 15, 18, 19];
const OV_ALARM = [0, 1, 2, 3, 5, 7, 8, 13, 14, 20, 29];
const OV_SHORT = { 3: '危险驾驶', 4: 'GNSS 故障', 5: '天线未接/剪断', 6: '天线短路' };
const COMPUTED = new Set([1, 2, 3, 22]);   // 状态位中由模拟器根据位置/速度计算的位（后端 Bit.auto 也会标出）
const AUTO_TIP = '由模拟器自动判定';
// 位的提示文本：自动判定 / 已锁定
function bitTip(b, ro) {
  return `bit${b.bit} ${b.name}${ro ? '（模拟器根据位置/速度计算，不能手动设置）' : b.locked ? '（已锁定为手动值，不再自动判定）' : b.auto ? '（' + AUTO_TIP + '；可在「状态与报警」锁定）' : ''}`;
}
const autoIc = b => b.auto && !b.locked ? `<i class="auto-ic" aria-hidden="true"></i>` : b.locked ? `<i class="lock-ic" aria-hidden="true"></i>` : '';
function chips(sel, bits, pick, type, short) {
  const list = bits.filter(b => pick.includes(b.bit) || b.on);
  const sig = type + JSON.stringify(list);
  const el = $(sel);
  if (el.dataset.sig === sig) return;
  el.dataset.sig = sig;
  el.innerHTML = list.map(b => {
    const ro = type === 'setStatus' && (COMPUTED.has(b.bit) || b.auto);
    const name = short ? OV_SHORT[b.bit] || b.name.replace(/报警$/, '') || b.name : b.name;
    return `<button class="${b.on ? 'x' : ''} ${ro ? 'ro' : ''}" title="${esc(bitTip(b, ro))}" aria-pressed="${b.on}" data-type="${type}" data-bit="${b.bit}" data-on="${b.on ? 0 : 1}" ${ro ? 'disabled' : ''}>${autoIc(b)}${esc(name)}</button>`;
  }).join('');
}

// 快捷上行：无字段的直接发送，有字段的打开“上行报文”并选中
const QUICK = [['0002', '终端心跳'], ['0301', '事件报告'], ['0702', '驾驶员信息'], ['0704', '批量位置'], ['0800', '多媒体事件'], ['0900', '透传上行'], ['0108', '升级结果']];
$('#ups').innerHTML = QUICK.map(([id, n]) => `<button data-quick="${id}"><b>${id}</b><span>${n}</span></button>`).join('') +
  '<button class="go" data-act="report"><b>0200</b><span>立即上报</span></button>';   // 主操作：蓝色
$('#ups').addEventListener('click', e => {
  const b = e.target.closest('[data-quick]');
  if (!b) return;
  const id = b.dataset.quick;
  if (!UP[id][1].length) { act('send', { msgId: id, params: {} }, b, id + ' 已发送'); return; }
  upSel = id; renderUp(); go('uplink');
});

function renderOverview(s) {
  if (!$('#dash') || page !== 'overview') return;   // 非总览页时不重绘 SVG 与 DOM
  const l = s.loc, k = s.counters, c = s.conn, m = s.motion, lim = speedLimit(s);
  renderRing(s);
  $('#spLim').textContent = lim || '-';
  $('#spDir').textContent = l.dir + '°';
  $('#spMode').textContent = MODE[m.mode];
  $('#milV').innerHTML = `${l.mileage.toFixed(1)}<small>km</small>`;
  $('#milRule').textContent = `汇报 ${m.reportInterval ? m.reportInterval + ' s' : '按距离'} · ${m.reportRule}`;
  $('#milFoot').textContent = `当前 ${l.speed.toFixed(1)} km/h · 下次汇报 ${m.nextReportIn >= 0 ? m.nextReportIn + ' s' : '-'}`;
  $('#fuelV').innerHTML = `${l.fuel.toFixed(1)}<small>L</small>`;
  $('#fuelV').classList.toggle('low', l.fuel < 15);
  $('#fuelBar').style.width = Math.max(0, Math.min(100, l.fuel)) + '%';
  $('#fSats').textContent = l.sats; $('#fSig').textContent = l.signal;
  $('#ovPos').textContent = `${l.lng.toFixed(6)}, ${l.lat.toFixed(6)}`;
  const route = (s.areas || []).find(a => a.kind === 4);
  const rp = s.replay || {};
  const modeInfo = m.mode === 'route' && route ? (route.name || '路线 ' + route.id) : m.mode === 'track' && rp.loaded ? `回放「${rp.name}」${Math.round(rp.progress * 100)}% · ${rp.mult}×` : MODE[m.mode];
  $('#ovMapInfo').textContent = [`海拔 ${l.alt} m`, modeInfo, m.tempTrack ? '临时跟踪 ' + m.tempTrack : ''].filter(Boolean).join(' · ');

  chips('#ovStatus', s.status, OV_STATUS, 'setStatus');
  chips('#ovAlarms', s.alarms, OV_ALARM, 'setAlarm', true);
  $('#hexStatusOv').textContent = '0x' + hex8(word(s.status));
  const nOn = s.alarms.filter(b => b.on).length, nVid = s.videoAlarms.filter(b => b.on).length;
  $('#alCount').textContent = nOn + nVid ? `${nOn + nVid} 项报警中${nVid ? `（视频 ${nVid}）` : ''}` : '无报警';
  $('#alTile').classList.toggle('hot', nOn + nVid > 0);

  $$('#replySeg button').forEach(b => b.classList.toggle('x', b.dataset.r === s.reply.mode));
  setVal('replyDelay', s.reply.delay);
  const rk = [['待确认 / 重传', `${k.pendingAcks} / ${k.retrans}`], ['盲区缓存', `${k.blindQueue}/${k.blindCap} 条${k.blindDropped ? `（丢弃 ${k.blindDropped}）` : ''}`], ['重连次数', c.reconnects],
    ['RSA', s.rsa.platKey ? (s.rsa.upEncrypt ? '上行加密' : '已获平台公钥') : '未启用'], ['鉴权码', c.token || '无']];
  const rkSig = JSON.stringify(rk);
  if (rkSig !== renderOverview.rkSig) {
    renderOverview.rkSig = rkSig;
    $('#replyKV').innerHTML = rk.map(([a, v]) => `<div><span>${a}</span><b class="mono">${esc(v)}</b></div>`).join('');
  }

  renderInbox(s);
}

// ---------- 平台下发（日志卡片里的标签页，带未读角标） ----------
let logTab = 'log';
const seenKey = () => 'jt808.inboxSeen.' + TID;
function setLogTab(t) {
  logTab = t;
  const inbox = t === 'inbox';
  $('#tabLog').classList.toggle('on', !inbox); $('#tabLog').setAttribute('aria-selected', String(!inbox));
  $('#tabInbox').classList.toggle('on', inbox); $('#tabInbox').setAttribute('aria-selected', String(inbox));
  $('#log').hidden = inbox; $('#inbox').hidden = !inbox;
  $('.lt-log').hidden = inbox; $('.lt-inbox').hidden = !inbox;
  if (inbox) markInboxRead();
  if (S) renderInbox(S);
}
function markInboxRead() {
  if (!S || !S.inbox.length) return;
  localStorage.setItem(seenKey(), String(Math.max(...S.inbox.map(m => m.seq || 0))));
}
$('#tabLog').onclick = () => setLogTab('log');
$('#tabInbox').onclick = () => setLogTab('inbox');
$('.ltabs').addEventListener('keydown', e => {
  if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
  setLogTab(logTab === 'log' ? 'inbox' : 'log');
  (logTab === 'log' ? $('#tabLog') : $('#tabInbox')).focus();
});
$('#inboxRead').onclick = () => { markInboxRead(); S && renderInbox(S); };
onSwitch(() => setLogTab('log'));
function renderInbox(s) {
  if (logTab === 'inbox' && !document.hidden) markInboxRead();
  const seen = +(localStorage.getItem(seenKey()) || 0);
  const unread = s.inbox.filter(m => (m.seq || 0) > seen).length, ask = s.inbox.some(m => m.answers && !m.answered);
  const badge = $('#inboxBadge');
  badge.hidden = !unread && !ask;
  badge.textContent = unread || '!';
  badge.classList.toggle('info', !ask);
  badge.title = ask ? '有待回答的提问' : `${unread} 条未读`;
  $('#tabInbox').setAttribute('aria-label', `平台下发${unread ? `，${unread} 条未读` : ''}${ask ? '，有待回答的提问' : ''}`);
  $('#inboxN').textContent = s.inbox.length ? s.inbox.length + ' 条' : '';
  $('#inboxRead').hidden = !unread;
  const sig = JSON.stringify(s.inbox.slice(0, 60));
  if (sig === renderInbox.sig) return;
  renderInbox.sig = sig;
  $('#inbox').innerHTML = s.inbox.slice(0, 60).map(m => `<div class="ib k-${m.kind} ${m.answers && !m.answered ? 'q' : ''}"><span class="ic">${KIND_ICON[m.kind] || '下发'}</span><div class="b">
      <div class="h"><b>${esc(m.title)}</b><span class="tm mono">${fmtT(m.t)}</span></div>${m.content ? `<p>${esc(m.content)}</p>` : ''}
      ${m.answers && !m.answered ? `<div class="ans">${m.answers.map(a => `<button data-ans="${a.id}" data-serial="${m.serial}">${a.id} ${esc(a.text)}</button>`).join('')}</div>` : ''}
      ${m.answered ? '<p><span class="tag ok">已回答</span></p>' : ''}</div></div>`).join('')
    || '<div class="empty-sm">暂无平台下发。文本、提问、控制、参数设置等指令会显示在这里</div>';
}
onSwitch(() => { renderInbox.sig = ''; });
onState(renderOverview);
onPage('overview', () => S && renderOverview(S));
$('#replySeg').onclick = e => { const b = e.target.closest('[data-r]'); if (b) act('setReply', { mode: b.dataset.r }, null, '应答策略：' + b.firstChild.textContent); };
$('#replyDelaySet').onclick = e => act('setReply', { delay: $('#replyDelay').value }, e.target, '应答延迟已设置');
$('#replyDelay').addEventListener('keydown', e => { if (e.key === 'Enter') $('#replyDelaySet').click(); });
document.addEventListener('click', e => {
  const ans = e.target.closest('[data-ans]');
  if (ans) act('answer', { serial: +ans.dataset.serial, answerId: +ans.dataset.ans }, ans, '已回复 0302');
});

// ================= 运动与位置 =================
$('#modeSeg').onclick = e => { const m = e.target.dataset.m; if (m) act('setMotion', { mode: m }); };
$('#spd').oninput = e => $('#spdV').textContent = e.target.value + ' km/h';
$('#spd').onchange = e => act('setMotion', { speed: +e.target.value });
$('#setPos').onclick = e => {
  const lng = Number($('#pLng').value), lat = Number($('#pLat').value);
  if (!Number.isFinite(lng) || lng < -180 || lng > 180 || !Number.isFinite(lat) || lat < -90 || lat > 90) {
    toast('经度范围 -180~180，纬度范围 -90~90'); return;
  }
  act('setPosition', { lng: $('#pLng').value, lat: $('#pLat').value }, e.target, '位置已设定');
};
$('#setVeh').onclick = e => act('setVehicle', { mileage: $('#vMil').value, fuel: $('#vFuel').value, tires: $('#vTires').value, cabinTemp: $('#vTemp').value }, e.target, '车辆数据已更新');
$('#cardIn').onclick = e => act('send', { msgId: '0702', params: { status: 1 } }, e.target, '已插卡（0702）');
$('#cardOut').onclick = e => act('send', { msgId: '0702', params: { status: 2 } }, e.target, '已拔卡（0702）');
$('#fitMap').onclick = () => maps.mapMotion && S && fitMap(maps.mapMotion, S);
$('#clearTrack').onclick = async () => { if (S && await confirmBox('清空当前地图轨迹？')) act('setPosition', { lng: S.loc.lng.toFixed(6), lat: S.loc.lat.toFixed(6) }, null, '轨迹已清空'); };

// 轨迹回放
$('#rpImport').onclick = () => $('#rpFile').click();
$('#rpFile').onchange = async e => {
  const f = e.target.files[0];
  e.target.value = '';
  if (!f) return;
  if (f.size > 16 << 20) return toast('文件过大（最大 16 MB）');
  try {
    const name = f.name.replace(/\.[^.]+$/, '');
    const r = await fetch(api('route') + '?name=' + encodeURIComponent(name), { method: 'POST', body: await f.text() });
    const j = await r.json();
    if (!j.ok) return toast(j.error || '导入失败');
    const v = j.replay;
    toast(`已导入「${v.name}」${v.points} 点 · ${v.distanceKm} km${v.hasTime ? ' · 按时间回放' : ''}`, 'ok');
    poll();
  } catch (err) { toast('导入失败：' + err.message); }
};
$('#rpMult').onchange = e => act('setReplay', { mult: +e.target.value }, null, '回放倍速 ' + e.target.value + '×');
$('#rpLoop').onchange = e => act('setReplay', { loop: e.target.checked ? 1 : 0 }, null, e.target.checked ? '循环回放' : '回放到终点后停止');
$('#rpSeek').onchange = e => act('setReplay', { seek: +e.target.value / 1000 });
$('#rpRestart').onclick = e => act('setReplay', { restart: 1 }, e.target, '已回到起点');
$('#rpPlay').onclick = e => act('setMotion', { mode: S && S.motion.mode === 'track' ? 'stop' : 'track' }, e.target);
$('#rpClear').onclick = async e => { if (await confirmBox('移除导入的轨迹？')) act('clearReplay', {}, e.target, '轨迹已移除'); };
const fmtSec = s => s >= 3600 ? `${Math.floor(s / 3600)}:${String(Math.floor(s / 60) % 60).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}` : `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
function renderReplay(s) {
  const r = s.replay || {}, on = s.motion.mode === 'track';
  $('#rpCtl').hidden = !r.loaded;
  $('#rpTag').className = 'tag ' + (on ? (r.done ? 'warn' : 'ok') : '');
  $('#rpTag').textContent = !r.loaded ? '' : on ? (r.done ? '已到终点' : '回放中') : '已导入';
  $('#rpInfo').innerHTML = r.loaded
    ? `<b>${esc(r.name)}</b><span>${r.format.toUpperCase()} · ${r.points} 点 · ${r.distanceKm} km${r.hasTime ? ' · 时长 ' + fmtSec(r.durationS) : ' · 无时间戳'}</span><span class="mono">${(r.progress * 100).toFixed(1)}%</span>`
    : '<span class="muted">尚未导入轨迹</span>';
  if (r.loaded) {
    if (!editing.has('rpSeek')) $('#rpSeek').value = Math.round(r.progress * 1000);
    if (!editing.has('rpMult')) { const o = [...$('#rpMult').options].some(x => +x.value === r.mult); if (!o) $('#rpMult').add(new Option(r.mult + '×', r.mult)); $('#rpMult').value = String(r.mult); }
    $('#rpLoop').checked = r.loop;
    $('#rpPlay').textContent = on ? '停止回放' : '开始回放';
  }
}
onState(s => page === 'motion' && renderReplay(s));

function renderMotion(s) {
  $$('#modeSeg button').forEach(b => b.classList.toggle('on', b.dataset.m === s.motion.mode));
  if (!editing.has('spd')) { $('#spd').value = s.motion.speed; $('#spdV').textContent = s.motion.speed + ' km/h'; }
  setVal('pLng', s.loc.lng.toFixed(6)); setVal('pLat', s.loc.lat.toFixed(6));
  setVal('vMil', s.loc.mileage.toFixed(1)); setVal('vFuel', s.loc.fuel.toFixed(1));
  setVal('vTires', (s.loc.tires || []).join(',')); setVal('vTemp', s.loc.cabinTemp ?? '');
  $('#mPos').textContent = `${s.loc.lng.toFixed(6)}, ${s.loc.lat.toFixed(6)}`;
  const d = s.driver;
  $('#drvState').className = 'tag ' + (d.onDuty ? 'ok' : '');
  $('#drvState').textContent = d.onDuty ? '已插卡' : '未插卡';
  $('#drvKV').innerHTML = `<dt>姓名</dt><dd>${esc(d.name)}</dd><dt>从业资格证</dt><dd class="mono">${esc(d.licenseNo)}</dd>
    <dt>发证机构</dt><dd>${esc(d.org)}</dd><dt>有效期</dt><dd class="mono">${esc(d.validTo)}</dd>`;
  const l = s.loc, m = s.motion;
  $('#motionStats').innerHTML = [['速度', l.speed.toFixed(1) + ' km/h'], ['方向', l.dir + '°'], ['海拔', l.alt + ' m'], ['卫星', l.sats],
    ['汇报', m.reportInterval ? m.reportInterval + 's ' + m.reportRule : m.reportRule], ['下次汇报', m.nextReportIn >= 0 ? m.nextReportIn + ' 秒' : '-']]
    .map(([a, b]) => `<div><span>${a}</span><b>${esc(b)}</b></div>`).join('');
}
onState(s => page === 'motion' && renderMotion(s));

// ================= 状态与报警 =================
function renderBits(sel, bits, type) {
  const sig = type + JSON.stringify(bits);
  const el = $(sel);
  if (el.dataset.sig === sig) return;
  el.dataset.sig = sig;
  el.innerHTML = bits.map(b => {
    const ro = type === 'setStatus' && (COMPUTED.has(b.bit) || b.auto);
    const btn = `<button class="bit ${b.on ? 'on' : ''} ${ro ? 'ro' : ''} ${b.locked ? 'locked' : ''}" title="${esc(bitTip(b, ro))}" aria-pressed="${b.on}"
      data-type="${type}" data-bit="${b.bit}" data-on="${b.on ? 0 : 1}" ${ro ? 'disabled' : ''}><span class="n">${b.bit}</span><span class="t">${esc(b.name)}</span>${ro ? `<i class="auto-ic" title="${AUTO_TIP}" aria-hidden="true"></i>` : ''}</button>`;
    if (type !== 'setAlarm' || !b.auto) return btn;
    // 自动判定的报警位：右侧锁定按钮（锁定后保持手动值，不被模拟器改写）
    const lk = b.locked ? '解锁：恢复由模拟器自动判定' : '锁定为当前值：不再由模拟器自动判定';
    return `<div class="bitw ${b.locked ? 'locked' : ''}">${btn}<button class="bit-lock" data-lock="${b.bit}" data-locked="${b.locked ? 1 : 0}" aria-pressed="${!!b.locked}" aria-label="${esc(b.name)}：${lk}" title="${lk}">${b.locked ? '<i class="lock-ic"></i>' : `<i class="auto-ic" title="${AUTO_TIP}"></i>`}</button></div>`;
  }).join('');
}
document.addEventListener('click', e => {
  const b = e.target.closest('[data-lock]');
  if (!b) return;
  const lock = b.dataset.locked !== '1';
  act('lockAlarm', { bit: +b.dataset.lock, lock: lock ? 1 : 0 }, b, lock ? '已锁定，模拟器不再自动判定该位' : '已解锁，恢复自动判定');
});
const word = bits => bits.reduce((v, b) => v + (b.on ? 2 ** b.bit : 0), 0);
function renderBitsPage(s) {
  renderBits('#bStatus', s.status, 'setStatus');
  renderBits('#bAlarm', s.alarms, 'setAlarm');
  renderBits('#bVideo', s.videoAlarms, 'setVideoAlarm');
  renderBits('#bSignal', s.signals, 'setSignal');
  renderBits('#bIO', s.io, 'setIO');
  $('#hexStatus').textContent = '0x' + hex8(word(s.status));
  $('#hexAlarm').textContent = '0x' + hex8(word(s.alarms));
  $('#hexSignal').textContent = '0x' + hex8(word(s.signals));
  setVal('ad0', s.analog.ad0); setVal('ad1', s.analog.ad1);
}
onState(s => page === 'bits' && renderBitsPage(s));
document.addEventListener('click', e => {
  const b = e.target.closest('[data-type][data-bit]');
  if (b && !b.disabled) act(b.dataset.type, { bit: +b.dataset.bit, on: +b.dataset.on });
});
$('#adSet').onclick = e => act('setAnalog', { ad0: +$('#ad0').value, ad1: +$('#ad1').value }, e.target, '模拟量已设置');

// ================= 上行报文 =================
const UP = {
  '0200': ['位置信息汇报', []], '0002': ['终端心跳', []], '0004': ['查询服务器时间', []],
  '0301': ['事件报告', [['eventId', '事件 ID', 1]]],
  '0303': ['信息点播/取消', [['type', '信息类型', 1], ['action', '操作', 1, { 0: '取消', 1: '点播' }]]],
  '0108': ['终端升级结果通知', [['type', '升级类型', 0, { 0: '终端', 12: '道路运输证 IC 卡读卡器', 52: '北斗卫星定位模块' }], ['result', '升级结果', 0, { 0: '成功', 1: '失败', 2: '取消' }]]],
  '0702': ['驾驶员身份信息', [['status', '状态', 1, { 1: '插卡（上班）', 2: '拔卡（下班）' }], ['cardStatus', 'IC 卡读取结果', 0, { 0: '读卡成功', 1: '卡片密钥认证未通过', 2: '卡片已被锁定', 3: '卡片被拔出', 4: '数据校验错误' }],
    ['name', '驾驶员姓名', '张三'], ['licenseNo', '从业资格证编码', '440100199001011234'], ['org', '发证机构', '广州市交通运输局'], ['validTo', '证件有效期 YYYYMMDD', '20301231']]],
  '0704': ['定位数据批量上传', [['count', '条数', 5], ['type', '位置数据类型', 0, { 0: '正常位置批量汇报', 1: '盲区补报' }]]],
  '0705': ['CAN 总线数据上传', [['count', '条数', 3]]],
  '0800': ['多媒体事件信息', [['type', '多媒体类型', 0, { 0: '图像', 1: '音频', 2: '视频' }], ['format', '编码格式', 0, { 0: 'JPEG', 1: 'TIF', 2: 'MP3', 3: 'WAV', 4: 'WMV' }], ['event', '事件项', 0, { 0: '平台下发指令', 1: '定时动作', 2: '抢劫报警触发', 3: '碰撞侧翻报警触发', 4: '门开拍照', 5: '门关拍照', 6: '车门由开变关，车速超过 20km/h', 7: '定距拍照' }], ['channel', '通道 ID', 1]]],
  '0801': ['多媒体数据上传', [['type', '多媒体类型', 0, { 0: '图像', 1: '音频', 2: '视频' }], ['event', '事件项', 0, { 0: '平台下发指令', 1: '定时动作', 2: '抢劫报警触发', 3: '碰撞侧翻报警触发' }], ['channel', '通道 ID', 1]]],
  '0900': ['数据上行透传', [['type', '透传消息类型', 240, { 0: '0x00 GNSS 模块详细定位数据（NMEA）', 11: '0x0B 道路运输证 IC 卡信息', 65: '0x41 串口 1 透传', 66: '0x42 串口 2 透传', 240: '0xF0 用户自定义' }],
    ['content', '透传内容（文本；空 = 按类型自动生成）', 'HELLO'], ['hex', '透传内容 hex（优先于文本）', ''], ['size', '按大小填充（字节，0=使用内容）', 0]]],
  '0901': ['数据压缩上报', [['size', '原文重复次数', 20]]],
  '0701': ['电子运单上报', [['content', '电子运单内容', '电子运单：广州 -> 深圳，货物 10 吨']]],
  '1005': ['乘客流量（1078）', [['up', '上车人数', 3], ['down', '下车人数', 1]]],
  '0003': ['终端注销', []],
};
let upSel = '0200';
const UP_ORDER = ['0200', '0002', '0004', '0301', '0303', '0108', '0702', '0704', '0705', '0800', '0801', '0900', '0901', '0701', '1005', '0003']; // 对象的数字键会被重排，单独给出顺序
function renderUp() {
  $('#upList').innerHTML = UP_ORDER.map(id => [id, UP[id]]).map(([id, [n]]) => `<button class="${id === upSel ? 'on' : ''}" data-up="${id}"><span class="mono">${id}</span>${n}</button>`).join('');
  const [name, fields] = UP[upSel];
  $('#upTitle').textContent = name; $('#upId').textContent = upSel;
  $('#upForm').innerHTML = fields.length ? fields.map(([k, n, d, o]) => `<div class="field"><label class="lbl">${n}</label>${o
    ? `<select id="u_${k}">${Object.entries(o).map(([v, t]) => `<option value="${v}" ${+v === d ? 'selected' : ''}>${v} · ${esc(t)}</option>`).join('')}</select>`
    : `<input id="u_${k}" value="${esc(d)}" ${typeof d === 'number' ? 'type="number"' : ''}>`}</div>`).join('')
    : '<p class="hint span2" style="margin:0">该消息没有可设置的字段。</p>';
  $('#upSend').textContent = '发送 ' + upSel;
}
$('#upList').onclick = e => { const b = e.target.closest('[data-up]'); if (b) { upSel = b.dataset.up; renderUp(); } };
$('#upSend').onclick = async e => {
  if (upSel === '0003' && !(await confirmBox('发送 0003 注销并清除鉴权码？'))) return;
  const params = {};
  UP[upSel][1].forEach(([k, , d]) => { const v = $('#u_' + k).value; params[k] = typeof d === 'number' ? +v : v; });
  act('send', { msgId: upSel, params }, e.target, upSel + ' 已发送');
};
$('#rawSend').onclick = e => {
  const line = $('#rawLine').value.trim();
  if (!line) { toast('请输入原始报文内容'); $('#rawLine').focus(); return; }
  act('raw', { line }, e.target, '原始报文已发送');
};
renderUp();

// ================= 主动安全 =================
// 报警类型表由后端按当前标准（苏标 / 粤标）提供：S.safety.types[外设ID][类型]
const ADAS_ATTR = { adas: 100, dsm: 101, tpms: 102, bsd: 103 };
// 上次选的报警类别与表单值（附件数量等）一并记住，重启后不用每次重填
let adasKind = localStorage.getItem('jt808.adasKind') || 'adas', adasSig = '', gdPer = 100;
function fillAdas(s) {
  $$('#adasKind button').forEach(b => b.classList.toggle('on', b.dataset.k === adasKind));
  const types = (s && s.safety && s.safety.types[ADAS_ATTR[adasKind]]) || {};
  const sig = adasKind + JSON.stringify(types);
  if (sig === adasSig) return;
  adasSig = sig;
  const keep = $('#aType').value || $('#aType').dataset.keepVal || ''; // 上次选的类型（选项是动态生成的）
  $('#aType').innerHTML = Object.entries(types).sort((x, y) => x[0] - y[0])
    .map(([v, n]) => `<option value="${esc(v)}">0x${(+v).toString(16).toUpperCase().padStart(2, '0')} · ${esc(n)}</option>`).join('');
  if ([...$('#aType').options].some(o => o.value === keep)) $('#aType').value = keep;
  const lv = adasKind === 'adas' || adasKind === 'dsm';
  $('#aLevel').disabled = !lv;
}
$('#adasKind').onclick = e => {
  if (!e.target.dataset.k) return;
  adasKind = e.target.dataset.k;
  localStorage.setItem('jt808.adasKind', adasKind);
  fillAdas(S);
};
// 上报表单是「操作参数」而非终端状态，记住上次输入（否则重启后附件数量等回默认值）
// 注意只放纯前端字段：随终端状态刷新的（如导出上限/保留天数）不在此列
keepFields(['aType', 'aLevel', 'aFiles', 'aFlag', 'rawLine', 'exIds']);
$('#adasStd').onclick = async e => {
  const std = e.target.dataset.std;
  if (!std || (S && S.safety.std === std)) return;
  if (await act('setConfig', { safetyStd: std }, e.target, std === 'gd' ? '已切换为粤标' : '已切换为苏标')) { adasSig = ''; window.reloadParams && page === 'params' && reloadParams(); }
};
$('#aSend').onclick = e => {
  const files = +$('#aFiles').value;
  if (!Number.isFinite(files) || files < 0 || files > 10) { toast('附件数量范围 0~10'); return; }
  act('adas', { kind: adasKind, alarmType: +$('#aType').value, level: +$('#aLevel').value, files, flag: +$('#aFlag').value }, e.target, '主动安全报警已上报');
};
$('#adasAuto').onchange = e => act('setConfig', { autoAttach: e.target.checked ? 1 : 0 }, null, e.target.checked ? '已开启自动上传附件' : '已关闭自动上传附件');
$('#adasHostSet').onclick = e => act('setConfig', { attachHost: $('#adasHost').value }, e.target, '附件服务器 IP 已保存');

const GP_KEYS = ['company', 'model', 'hwVer', 'swVer', 'devId', 'customer'];
let gpSig = '';
function curPer(s) { return (s.safety.gd.periph || []).find(p => p.id === gdPer) || {}; }
function renderGD(s, force) {
  const sf = s.safety;
  $$('#gdPer button').forEach(b => b.classList.toggle('on', +b.dataset.p === gdPer));
  if (!$('#gpWork').options.length) {
    $('#gpWork').innerHTML = Object.entries(sf.workNames).map(([v, n]) => `<option value="${esc(v)}">0x${(+v).toString(16).toUpperCase().padStart(2, '0')} · ${esc(n)}</option>`).join('');
    $('#gdSim').innerHTML = Object.entries(sf.upgradeSims).map(([v, n]) => `<option value="${esc(v)}">${esc(n)}</option>`).join('');
  }
  const p = curPer(s), sig = gdPer + JSON.stringify(p);
  if (force || sig !== gpSig) {
    gpSig = sig;
    if (!editing.has('gpWork')) $('#gpWork').value = p.work;
    $('#gpAlarm').innerHTML = Object.entries(sf.alarmBits).map(([b, n]) =>
      `<label title="bit${esc(b)} ${esc(n)}"><input type="checkbox" data-gpbit="${esc(b)}" ${(Math.floor((p.alarm || 0) / 2 ** b) % 2) ? 'checked' : ''}><em>${esc(b)}</em><span>${esc(n)}</span></label>`).join('');
    GP_KEYS.forEach(k => setVal('gp_' + k, p[k]));
  }
  setVal('gdInst', sf.gd.installErr ? '0x' + sf.gd.installErr.toString(16).toUpperCase() : '0');
  setVal('gdAlgo', sf.gd.algoErr ? '0x' + sf.gd.algoErr.toString(16).toUpperCase() : '0');
  if (!editing.has('gdSim')) $('#gdSim').value = sf.gd.upgradeSim || '';
}
$('#gdPer').onclick = e => { if (e.target.dataset.p) { gdPer = +e.target.dataset.p; S && renderGD(S, true); } };
$('#gpSave').onclick = e => {
  const p = { id: gdPer, work: +$('#gpWork').value, alarm: $$('#gpAlarm [data-gpbit]').reduce((a, el) => a + (el.checked ? 2 ** +el.dataset.gpbit : 0), 0) };
  GP_KEYS.forEach(k => p[k] = $('#gp_' + k).value.trim());
  act('gdPeriph', p, e.target, '外设已保存');
};
$('#gpF7').onclick = e => act('gdReport', { passType: 0xF7 }, e.target, '已上报外设状态（0900 F7）');
$('#gpF8').onclick = e => act('gdReport', { passType: 0xF8 }, e.target, '已上报外设信息（0900 F8）');
const hexNum = v => { v = String(v).trim(); const n = /^0x/i.test(v) ? parseInt(v, 16) : Number(v); return Number.isFinite(n) && n >= 0 && n <= 0xFFFFFFFF ? n : NaN; };
$('#gdSetBtn').onclick = e => {
  const inst = hexNum($('#gdInst').value || 0), algo = hexNum($('#gdAlgo').value || 0);
  if (Number.isNaN(inst) || Number.isNaN(algo)) return toast('异常信息需为 0~0xFFFFFFFF');
  act('gdSet', { installErr: inst, algoErr: algo, upgradeSim: $('#gdSim').value }, e.target, '已保存');
};

function renderAdas(s) {
  const sf = s.safety, gd = sf.std === 'gd';
  $$('#adasStd button').forEach(b => b.classList.toggle('on', b.dataset.std === sf.std));
  $('#stdTag').textContent = sf.name;
  $('#stdTag').className = 'tag ' + (gd ? 'info' : '');
  $('#stdHint').textContent = gd
    ? '粤标：0x64~0x67 按 T/GDRTA 002-2020 表 5-15~5-20 编码（40 字节报警标识号、TPMS 位置 BYTE[2]、车辆状态按表 6-9）；参数 F364~F367 按表 5-10~5-13，另有只读 F370；支持 8900/0900 外设查询、8105 升级进度 0x1FC4、8801 外设拍照、智能视频通道 0x64/0x65/0x67。'
    : '苏标（默认）：报警标识 2013 版 16 字节 / 2019 版 40 字节，类型表与参数按 JSATL12。';
  $('#stdWarn').hidden = !sf.verForced;
  $('#stdWarn').textContent = '终端配置的协议版本是 JT/T 808-2013，粤标基于 JT/T 808-2019：模拟器将强制按 2019 帧格式收发（在“终端配置”中改为 2019 可消除此提示）。';
  $('#attachHint').textContent = '上传流程：1210 报警附件信息 → 1211 文件信息 → 文件数据码流 → 1212 文件上传完成，按 9212 补传。' + (gd
    ? '粤标命名 文件类型_通道_外设ID+类型_序号_报警编号.后缀（通道 64/65/67，TPMS 填 0），附件 ≥3 个时含视频 mp4 与车辆状态数据记录文件 bin；上传中断开会重连并以 1210 信息类型 0x01 补传剩余文件。'
    : '文件名按 JSATL12 规则生成（jpg + mp4）。') + '参数（F364~F367）可在“终端参数 → 主动安全”中逐项配置。';
  fillAdas(s);
  $('#adasAuto').checked = s.config.autoAttach;
  setVal('adasHost', s.config.attachHost);
  $('#gdCards').hidden = !gd;
  if (gd) renderGD(s);
}
onState(s => page === 'adas' && renderAdas(s));
onPage('adas', () => S && renderAdas(S));
onSwitch(() => { adasSig = ''; gpSig = ''; });

// ================= 多媒体与视频 =================
$('#photoBtns').innerHTML = [1, 2, 3, 4].map(c => `<button class="btn sm" data-photo="${c}">◉ 通道 ${c} 拍照</button>`).join('');
document.addEventListener('click', e => {
  const p = e.target.closest('[data-photo]');
  if (p) act('photo', { channel: +p.dataset.photo }, p, `通道 ${p.dataset.photo} 拍照上传中`);
  const st = e.target.closest('[data-stop]');
  if (st) act('stopStream', { channel: +st.dataset.stop }, st, '推流已停止');
});
function renderMedia(s) {
  $('#streamN').textContent = s.streams.length ? s.streams.length + ' 路' : '';
  $('#streams').innerHTML = s.streams.length ? `<div class="table-wrap"><table><thead><tr><th>通道</th><th>类型</th><th>目标</th><th class="r">已发帧</th><th></th></tr></thead><tbody>${
    s.streams.map(x => `<tr><td><span class="tag info">通道 ${x.channel}</span></td><td>${esc(x.kind)}</td><td class="mono">${esc(x.target)}</td>
      <td class="r mono">${x.frames}</td><td class="r"><button class="btn xs ghost danger" data-stop="${x.channel}">停止</button></td></tr>`).join('')}</tbody></table></div>`
    : '<div class="empty">没有推流。平台下发 9101 实时音视频或 9201 回放后开始推送 JT/T 1078 RTP 流</div>';
  $('#mediaN').textContent = s.media.length ? s.media.length + ' 条' : '';
  const sig = JSON.stringify(s.media);
  if (sig === renderMedia.sig) return;
  renderMedia.sig = sig;
  $('#mBody').innerHTML = s.media.map(m => `<tr><td class="mono">${m.id}</td><td>${esc(m.type)}</td><td>${m.channel}</td><td>${esc(m.event)}</td>
    <td class="mono">${fmtDT(m.t)}</td><td class="r mono">${(m.size / 1024).toFixed(1)} KB</td></tr>`).join('') || '<tr><td colspan="6" class="empty">暂无多媒体</td></tr>';
}
onState(s => page === 'media' && renderMedia(s));

// ================= 区域 =================
function renderAreas(s) {
  const sig = JSON.stringify(s.areas);
  if (sig === renderAreas.sig) return;
  renderAreas.sig = sig;
  const t = a => a.start && !a.start.startsWith('0001') ? `${fmtDT(a.start)} ~ ${fmtDT(a.end)}` : '-';
  $('#arBody').innerHTML = (s.areas || []).map(a => `<tr><td><span class="tag ${a.kind === 4 ? 'info' : 'ok'}">${esc(AREA_KIND[a.kind])}</span></td><td class="mono">${esc(a.id)}</td>
    <td>${esc(a.name || '-')}</td><td class="mono">0x${hex4(a.attr)}</td><td>${(a.points || []).length}</td><td>${a.maxSpeed ? a.maxSpeed + ' km/h' : '-'}</td>
    <td class="muted">${t(a)}</td><td class="r"><button class="btn xs ghost danger" data-delarea="${esc(a.kind)}:${esc(a.id)}">删除</button></td></tr>`).join('')
    || '<tr><td colspan="8" class="empty">平台未下发区域（8600/8602/8604/8606）</td></tr>';
}
onState(s => page === 'areas' && renderAreas(s));
document.addEventListener('click', async e => {
  const b = e.target.closest('[data-delarea]');
  if (!b) return;
  const [kind, id] = b.dataset.delarea.split(':').map(Number);
  if (!(await confirmBox(`删除${AREA_KIND[kind] || '区域'} #${id}？`))) return;
  act('deleteArea', { kind, id }, b, '区域已删除');
});
$('#areaClear').onclick = async e => { if (await confirmBox(e.target.dataset.confirm)) act('deleteArea', {}, e.target, '已删除全部区域'); };

// ================= 终端配置 =================
const PLATE_COLORS = { 0: '未上牌（上报 VIN）', 1: '蓝色', 2: '黄色', 3: '黑色', 4: '白色', 5: '绿色', 9: '其他' };
// [key, 标签, 类型, 说明, 校验(v, cfg) -> 错误]
const CFG_CONN = [
  ['host', '网关地址', 'text', 'IP 或域名', v => !v.trim() && '不能为空'],
  ['port', '端口', 'number', '', v => !(+v >= 1 && +v <= 65535) && '1~65535'],
  ['transport', '传输方式', 'select', 'UDP：一个数据报一条消息，按 0x0004/0x0005 超时重传', null, { tcp: 'TCP', udp: 'UDP' }],
  ['phone', '终端手机号', 'text', '= 平台 DEVICE_ID；2013 版最多 12 位，2019 版最多 20 位', (v, c) => (!/^\d*$/.test(v) && '只能是数字') || (v.length > (c.version === '2013' ? 12 : 20) && '位数过多')],
  ['version', '协议版本', 'select', '', null, { 2019: 'JT/T 808-2019', 2013: 'JT/T 808-2013' }],
  ['safetyStd', '主动安全标准', 'select', '粤标基于 JT/T 808-2019；选 2013 时强制按 2019 帧格式', null, { jsatl: '苏标 JSATL12（默认）', gd: '粤标 T/GDRTA 002-2020' }],
  ['token', '鉴权码', 'text', '为空时上线先注册（0100）', null],
];
const CFG_DEV = [
  ['plate', '车牌号', 'text', '', null], ['plateColor', '车牌颜色', 'select', '', null, PLATE_COLORS],
  ['vin', '车辆 VIN', 'text', '17 位；车牌颜色为 0 时注册上报', v => v && v.length !== 17 && '应为 17 位'],
  ['deviceId', '终端 ID', 'text', '2013 版 7 字节，2019 版 30 字节', (v, c) => v.length > (c.version === '2013' ? 7 : 30) && '过长'],
  ['maker', '制造商 ID', 'text', '2013 版 5 字节，2019 版 11 字节', (v, c) => v.length > (c.version === '2013' ? 5 : 11) && '过长'],
  ['model', '终端型号', 'text', '2013 版 20 字节，2019 版 30 字节', (v, c) => v.length > (c.version === '2013' ? 20 : 30) && '过长'],
  ['imei', '终端 IMEI', 'text', '15 位，2019 鉴权上报', v => v && !/^\d{15}$/.test(v) && '应为 15 位数字'],
  ['iccid', 'SIM 卡 ICCID', 'text', '20 位，0107 属性上报', v => v && !/^[0-9A-Fa-f]{1,20}$/.test(v) && '最多 20 位'],
  ['hwVer', '硬件版本号', 'text', '', null], ['fwVer', '固件版本号', 'text', '升级成功后自动更新', null],
];
function cfgInput([k, n, type, help, , opts]) {
  const ctl = type === 'select'
    ? `<select id="c_${k}" data-cfg="${k}">${Object.entries(opts).map(([v, t]) => `<option value="${v}">${esc(t)}</option>`).join('')}</select>`
    : `<input id="c_${k}" data-cfg="${k}" ${type === 'number' ? 'type="number"' : ''} class="${['phone', 'token', 'imei', 'iccid', 'vin', 'port'].includes(k) ? 'mono' : ''}">`;
  return `<div class="field"><label class="lbl" for="c_${k}">${n}</label>${ctl}${help ? `<span class="muted">${esc(help)}</span>` : ''}</div>`;
}
$('#cfgConn').innerHTML = CFG_CONN.map(cfgInput).join('');
$('#cfgDev').innerHTML = CFG_DEV.map(cfgInput).join('');
$('#cfgBehave').innerHTML = `
  <label class="switch"><input type="checkbox" id="b_autoReconnect"><span></span>断线自动重连（2s→60s 退避，主服务器失败 3 次后交替尝试备份服务器）</label>
  <label class="switch"><input type="checkbox" id="b_autoAlarms"><span></span>长时间运行自动触发报警（超速、疲劳驾驶、超时驾驶/停车）</label>
  <label class="switch"><input type="checkbox" id="b_autoAttach"><span></span>收到 9208 自动上传报警附件</label>
  <div class="field"><label class="lbl" for="b_blindCap">盲区补报队列上限</label><div class="field-row"><div class="input-unit" style="width:180px"><input id="b_blindCap" type="number" min="10" max="100000" step="100"><span>条</span></div><button class="btn sm" id="b_blindSet">应用</button><span class="muted" id="b_blindInfo"></span></div>
    <span class="muted">离线或 0200 重传失败的位置先存入队列，上线后以 0704 补报；超出上限丢弃最旧的并告警（默认 5000，范围 10~100000）</span></div>
  <div class="field"><label class="lbl" for="c_attachHost">附件服务器 IP 覆盖</label><input id="c_attachHost" data-cfg="attachHost" placeholder="为空时使用 9208 下发地址"></div>
  <label class="switch"><input type="checkbox" id="b_upRsa"><span></span>上行 RSA 加密（消息体属性 bit10，用平台 8A00 公钥加密）</label>
  <span class="muted" id="rsaNote"></span>
  <div class="form2">
    <div class="field"><label class="lbl">下行应答策略</label><select id="b_reply">${Object.entries({ auto: '正常应答', fail: '应答失败（结果 1）', unsupported: '不支持（结果 3）', none: '不应答（平台超时）' }).map(([v, t]) => `<option value="${v}">${t}</option>`).join('')}</select></div>
    <div class="field"><label class="lbl">应答延迟</label><div class="input-unit"><input id="b_delay" type="number" min="0" max="120" step="0.5"><span>秒</span></div></div>
    <div class="field span2"><label class="lbl" for="b_reply8500">8500 车辆控制应答方式</label><select id="b_reply8500">
      <option value="0500">只回 0500 车辆控制应答（默认）</option><option value="ack0500">先回 0001 通用应答，再回 0500</option></select></div>
  </div>`;
for (const k of ['autoReconnect', 'autoAlarms', 'autoAttach', 'upRsa'])
  $('#b_' + k).onchange = e => act('setConfig', { [k]: e.target.checked ? 1 : 0 }, null, '已保存');
$('#b_reply').onchange = e => act('setReply', { mode: e.target.value }, null, '应答策略已切换');
$('#b_delay').onchange = e => act('setReply', { delay: e.target.value }, null, '应答延迟已设置');
$('#b_blindSet').onclick = e => {
  const v = +$('#b_blindCap').value;
  if (!(v >= 10 && v <= 100000)) { toast('盲区队列上限范围 10~100000'); return; }
  act('setConfig', { blindCap: v }, e.target, '盲区队列上限已设为 ' + v + ' 条');
};
$('#b_blindCap').addEventListener('keydown', e => { if (e.key === 'Enter') $('#b_blindSet').click(); });
$('#b_reply8500').onchange = e => act('setConfig', { reply8500: e.target.value }, null, '8500 应答方式已保存');

const ALL_CFG = CFG_CONN.concat(CFG_DEV, [['attachHost']]);
function cfgValues() {
  const p = {};
  ALL_CFG.forEach(([k]) => p[k] = $('#c_' + k).value.trim());
  p.port = +p.port; p.plateColor = +p.plateColor;
  if (!p.transport) p.transport = 'tcp';
  return p;
}
function stdWarnCfg() {
  const el = $('#c_safetyStd'), warn = $('#c_version').value === '2013' && el.value === 'gd';
  el.classList.toggle('warn', warn);
  const help = el.parentElement.querySelector('.muted');
  help.textContent = warn ? '⚠ 当前协议版本 2013：粤标将强制按 JT/T 808-2019 帧格式收发' : '粤标基于 JT/T 808-2019；选 2013 时强制按 2019 帧格式';
  help.classList.toggle('warn-text', warn);
}
function validateCfg() {
  stdWarnCfg();
  const c = cfgValues();
  let ok = true;
  CFG_CONN.concat(CFG_DEV).forEach(([k, , , , check]) => {
    const el = $('#c_' + k), err = check && check(String(c[k] ?? ''), c);
    el.classList.toggle('invalid', !!err);
    el.title = err || '';
    const box = el.closest('.field');
    if (box) {
      let msg = box.querySelector('.field-err');
      if (err) {
        if (!msg) { msg = document.createElement('small'); msg.className = 'field-err'; box.appendChild(msg); }
        msg.textContent = err;
      } else if (msg) {
        msg.remove();
      }
    }
    if (err) ok = false;
  });
  return ok;
}
$('#page-config').addEventListener('input', e => {
  if (!e.target.dataset.cfg) return;
  e.target.dataset.dirty = 1;
  $('#cfgBar').hidden = false;
  validateCfg();
});
$('#page-config').addEventListener('change', e => { if (e.target.dataset.cfg) { e.target.dataset.dirty = 1; $('#cfgBar').hidden = false; validateCfg(); } });
function clearCfgDirty() { $$('[data-cfg]').forEach(el => { delete el.dataset.dirty; el.classList.remove('invalid'); }); $('#cfgBar').hidden = true; }
$('#cfgDiscard').onclick = async () => {
  if (!(await confirmBox('放弃未保存的终端配置修改？'))) return;
  clearCfgDirty(); S && renderConfig(S, true);
};
$('#cfgSave').onclick = async e => {
  if (!validateCfg()) { toast('请先修正标红的字段'); return; }
  if (await act('setConfig', cfgValues(), e.target, '终端配置已保存')) { clearCfgDirty(); loadTerms(); }
};

const DRV = [['name', '驾驶员姓名'], ['licenseNo', '从业资格证编码'], ['org', '发证机构名称'], ['validTo', '证件有效期（YYYYMMDD）'], ['idCard', '驾驶员身份证号（2019）']];
$('#drvForm').innerHTML = DRV.map(([k, n]) => `<div class="field ${k === 'org' ? 'span2' : ''}"><label class="lbl">${n}</label><input id="d_${k}" class="${k === 'name' || k === 'org' ? '' : 'mono'}"></div>`).join('');
$('#drvSave').onclick = e => { const p = {}; DRV.forEach(([k]) => p[k] = $('#d_' + k).value.trim()); act('setDriver', p, e.target, '驾驶员信息已保存'); };
const REC = [['install', '初次安装日期（YYMMDDhhmmss）'], ['pulse', '脉冲系数'], ['initial', '初始里程（0.1 km）'], ['class', '车牌分类']];
$('#recForm').innerHTML = REC.map(([k, n]) => `<div class="field"><label class="lbl">${n}</label><input id="r_${k}" class="${k === 'class' ? '' : 'mono'}" ${k === 'pulse' || k === 'initial' ? 'type="number" min="0"' : ''}></div>`).join('');
$('#recSave').onclick = e => { const p = {}; REC.forEach(([k]) => p[k] = $('#r_' + k).value.trim()); p.pulse = +p.pulse; p.initial = +p.initial; act('setRecorder', p, e.target, '记录仪参数已保存'); };

function renderConfig(s, force) {
  const c = s.config;
  if (force || !$$('[data-cfg]').some(el => el.dataset.dirty)) ALL_CFG.forEach(([k]) => setVal('c_' + k, k === 'transport' ? (c.transport || 'tcp') : k === 'safetyStd' ? (c.safetyStd || 'jsatl') : c[k]));
  stdWarnCfg();
  const online = s.conn.status !== 'offline';
  $('#cfgLock').hidden = !online;
  ['host', 'port', 'transport', 'phone', 'version'].forEach(k => $('#c_' + k).disabled = online);
  $('#c_safetyStd').disabled = online && c.version === '2013'; // 2013 终端切换粤标会改变帧格式
  $('#b_autoReconnect').checked = c.autoReconnect; $('#b_autoAlarms').checked = !!c.autoAlarms; $('#b_autoAttach').checked = c.autoAttach; $('#b_upRsa').checked = c.upRsa;
  $('#rsaNote').textContent = s.rsa.platKey ? '已收到平台 RSA 公钥。' : '尚未收到平台 8A00 公钥：开启后在收到公钥前仍以明文发送。';
  $('#b_reply').value = s.reply.mode; setVal('b_delay', s.reply.delay);
  if (!editing.has('b_reply8500')) $('#b_reply8500').value = c.reply8500 || '0500';
  setVal('b_blindCap', s.counters.blindCap);
  $('#b_blindInfo').textContent = `当前 ${s.counters.blindQueue} 条${s.counters.blindDropped ? `，本次运行已丢弃 ${s.counters.blindDropped} 条` : ''}`;
  $('#b_blindInfo').classList.toggle('warn-text', s.counters.blindDropped > 0);
  DRV.forEach(([k]) => setVal('d_' + k, s.driver[k]));
  REC.forEach(([k]) => setVal('r_' + k, s.rec[k]));
}
onState(s => page === 'config' && renderConfig(s));
onPage('config', () => S && renderConfig(S));
onSwitch(() => clearCfgDirty());

// 档案导出 / 导入
$('#profExport').onclick = () => { downloadURL(api('profile')); };
$('#profImport').onclick = () => $('#profFile').click();
$('#profFile').onchange = async e => {
  const f = e.target.files[0];
  e.target.value = '';
  if (!f) return;
  if (!(await confirmBox(`导入「${f.name}」？当前终端配置、参数、区域将被档案内容替换（需离线）。`))) return;
  try {
    const r = await fetch(api('profile'), { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: await f.text() });
    const j = await r.json();
    if (!j.ok) return toast(j.error || '导入失败');
    toast('终端档案已导入', 'ok');
    clearCfgDirty(); poll(); loadTerms(); window.reloadParams && reloadParams();
  } catch (err) { toast('导入失败：' + err.message); }
};
