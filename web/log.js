// 报文日志（SSE /api/log，主屏常驻）：过滤、暂停、放大、展开解析（消息头 / 常用消息体）与 hex
'use strict';
const LOG_MAX = 3000, LOG_SHOW = 800;
let logs = [], logPaused = false, lastSeq = 0, logDir = '';
const logEl = $('#log');
let logStick = true;   // 用户停留在底部时自动跟随新报文
logEl.addEventListener('scroll', () => { logStick = logEl.scrollHeight - logEl.scrollTop - logEl.clientHeight < 60; });

// ---------- 帧解析 ----------
function hexToBytes(h) { const b = []; for (let i = 0; i + 1 < h.length; i += 2) b.push(parseInt(h.substr(i, 2), 16)); return b; }
function unescapeFrame(b) {
  const out = [];
  for (let i = 0; i < b.length; i++) {
    if (b[i] === 0x7d && i + 1 < b.length) { out.push(b[i + 1] === 0x01 ? 0x7d : 0x7e); i++; } else out.push(b[i]);
  }
  return out;
}
const u16 = (b, p) => (b[p] << 8) | b[p + 1];
const u32 = (b, p) => ((b[p] << 24) >>> 0) + (b[p + 1] << 16) + (b[p + 2] << 8) + b[p + 3];
const bcdS = (b, p, n) => b.slice(p, p + n).map(x => x.toString(16).padStart(2, '0')).join('');
const hexS = (b, p, n) => b.slice(p, p + n).map(x => x.toString(16).padStart(2, '0')).join('').toUpperCase();
const gbkDec = (() => { try { return new TextDecoder('gbk'); } catch (e) { return new TextDecoder(); } })();
const strS = (b, p, n) => gbkDec.decode(new Uint8Array(b.slice(p, p + n))).replace(/\0+$/, '');

const ATTR = { 0x01: '里程', 0x02: '油量', 0x03: '行驶记录速度', 0x04: '人工确认报警事件ID', 0x05: '胎压', 0x06: '车厢温度', 0x11: '超速报警附加', 0x12: '进出区域/路线报警附加',
  0x13: '路段行驶时间不足/过长', 0x14: '视频相关报警', 0x15: '视频信号丢失', 0x16: '视频信号遮挡', 0x17: '存储器故障', 0x18: '异常驾驶行为', 0x25: '扩展车辆信号状态位', 0x2A: 'IO 状态位',
  0x2B: '模拟量', 0x30: '无线通信网络信号强度', 0x31: 'GNSS 定位卫星数', 0x64: 'ADAS 报警', 0x65: 'DSM 报警', 0x66: 'TPMS 报警', 0x67: 'BSD 报警', 0xF1: '安装异常信息', 0xF2: '算法异常信息' };
const RESULT = { 0: '成功/确认', 1: '失败', 2: '消息有误', 3: '不支持', 4: '报警处理确认' };

function attrValue(id, b, p, n) {
  const v = n === 4 ? u32(b, p) : n === 2 ? u16(b, p) : n === 1 ? b[p] : null;
  switch (id) {
    case 0x01: return (v / 10).toFixed(1) + ' km';
    case 0x02: return (v / 10).toFixed(1) + ' L';
    case 0x03: return (v / 10).toFixed(1) + ' km/h';
    case 0x13: return n === 7 ? `路段 ${u32(b, p)}，行驶 ${u16(b, p + 4)} 秒，${b[p + 6] ? '过长' : '不足'}` : hexS(b, p, n);
    case 0x12: return n === 6 ? `${AREA_KIND[b[p]] || '类型' + b[p]} ${u32(b, p + 1)}，${b[p + 5] ? '出' : '进'}` : hexS(b, p, n);
    case 0x25: case 0x14: return '0x' + hex8(v);
    case 0x2A: return '0x' + hex4(v);
    case 0x2B: return `AD0=${v & 0xFFFF}，AD1=${v >>> 16}`;
    case 0x30: case 0x31: return String(v);
  }
  return hexS(b, p, n);
}

function decodeFrame(hx) {
  const truncated = hx.endsWith('...');
  const raw = hexToBytes(hx.replace(/\.\.\.$/, ''));
  if (raw.length < 3) return null;
  const d = unescapeFrame(raw.slice(1, truncated ? raw.length : raw.length - 1));
  if (d.length < 12) return null;
  const head = [], body = [];
  const id = u16(d, 0), props = u16(d, 2);
  const len = props & 0x3FF, enc = (props >> 10) & 7, split = (props >> 13) & 1, v19 = (props >> 14) & 1;
  head.push(['消息 ID', '0x' + hex4(id)], ['消息体属性', `0x${hex4(props)}（长度 ${len}${split ? '，分包' : ''}${enc & 1 ? '，RSA 加密' : enc ? '，加密方式 ' + enc : ''}${v19 ? '，2019 版本标识' : ''}）`]);
  let p = 4;
  if (v19) { head.push(['协议版本号', d[4]], ['终端手机号', bcdS(d, 5, 10).replace(/^0+/, '')]); p = 15; }
  else { head.push(['终端手机号', bcdS(d, 4, 6).replace(/^0+/, '')]); p = 10; }
  head.push(['消息流水号', u16(d, p)]); p += 2;
  if (split) { head.push(['分包', `第 ${u16(d, p + 2)} / ${u16(d, p)} 包`]); p += 4; }
  const headLen = p;
  if (!truncated) {
    let x = 0; for (let i = 0; i < d.length - 1; i++) x ^= d[i];
    head.push(['校验码', `0x${d[d.length - 1].toString(16).padStart(2, '0').toUpperCase()} ${x === d[d.length - 1] ? '✓' : '✗ 应为 0x' + x.toString(16)}`]);
  }
  const b = d.slice(p, truncated ? d.length : d.length - 1);
  try {
    if (!(enc & 1) && !split && !truncated) decodeBody(id, b, body);
    else if (enc & 1) body.push(['说明', '消息体已加密，未解析']);
  } catch (e) { body.push(['解析', '消息体格式与预期不符']); }
  return { head, body, bytes: d, headLen };
}

function decodeLoc(b, p, out) {
  out.push(['报警标志', '0x' + hex8(u32(b, p))], ['状态', '0x' + hex8(u32(b, p + 4))],
    ['纬度', (u32(b, p + 8) / 1e6).toFixed(6)], ['经度', (u32(b, p + 12) / 1e6).toFixed(6)], ['高程', u16(b, p + 16) + ' m'],
    ['速度', (u16(b, p + 18) / 10).toFixed(1) + ' km/h'], ['方向', u16(b, p + 20) + '°'], ['时间', bcdS(b, p + 22, 6).replace(/(..)(..)(..)(..)(..)(..)/, '20$1-$2-$3 $4:$5:$6')]);
  for (let q = p + 28; q + 2 <= b.length;) {
    const id = b[q], n = b[q + 1];
    if (q + 2 + n > b.length) break;
    out.push([`附加 0x${id.toString(16).padStart(2, '0').toUpperCase()} ${ATTR[id] || ''}`, attrValue(id, b, q + 2, n)]);
    q += 2 + n;
  }
}

function decodeBody(id, b, out) {
  switch (id) {
    case 0x0200: decodeLoc(b, 0, out); break;
    case 0x0201: case 0x0500: out.push(['应答流水号', u16(b, 0)]); decodeLoc(b, 2, out); break;
    case 0x0001: case 0x8001: out.push(['应答流水号', u16(b, 0)], ['应答 ID', '0x' + hex4(u16(b, 2))], ['结果', `${b[4]} ${RESULT[b[4]] || ''}`]); break;
    case 0x8100: out.push(['应答流水号', u16(b, 0)], ['结果', b[2]]); if (b.length > 3) out.push(['鉴权码', strS(b, 3, b.length - 3)]); break;
    case 0x0102: out.push(['鉴权码', b.length > 0 && b[0] < b.length ? strS(b, 1, b[0]) + '（2019 格式）' : strS(b, 0, b.length)]); break;
    case 0x8103: case 0x0104: {
      let q = 0;
      if (id === 0x0104) { out.push(['应答流水号', u16(b, 0)]); q = 2; }
      const n = b[q++]; out.push(['参数个数', n]);
      for (let i = 0; i < n && q + 5 <= b.length; i++) { const pid = u32(b, q), l = b[q + 4]; out.push(['参数 0x' + hex4(pid), hexS(b, q + 5, l)]); q += 5 + l; }
      break;
    }
    case 0x8300: out.push(['标志', '0x' + b[0].toString(16)], ['内容', strS(b, b.length > 1 && b[1] <= 2 ? 2 : 1, b.length)]); break;
    case 0x0002: out.push(['说明', '心跳，消息体为空']); break;
  }
  if (b.length) out.push(['消息体', `${b.length} 字节`]);
}

function hexDump(bytes, hl) {
  const lines = [];
  for (let i = 0; i < bytes.length; i += 16) {
    const cells = bytes.slice(i, i + 16).map((x, j) => { const s = x.toString(16).padStart(2, '0').toUpperCase(); return i + j < hl ? `<span class="hl">${s}</span>` : s; });
    lines.push(i.toString(16).padStart(4, '0') + '  ' + cells.join(' '));
  }
  return lines.join('\n');
}

// ---------- 渲染 ----------
function logMatch(e) {
  const id = $('#lId').value.trim().toUpperCase(), q = $('#lText').value.trim().toLowerCase();
  if (logDir && e.dir !== logDir) return false;
  if (id && !(e.msgId || '').includes(id)) return false;
  if (q && !((e.summary || '') + (e.name || '')).toLowerCase().includes(q)) return false;
  if ($('#lHb').checked && (e.msgId === '0002' || (e.msgId === '8001' && (e.summary || '').startsWith('0002')))) return false;
  return true;
}
const DIRN = { up: '上行', down: '下行', sys: '系统', err: '错误' };
function logRow(e) {
  const d = document.createElement('div');
  d.className = `le ${e.dir}${e.hex ? ' hasx' : ''}`;
  const pkt = e.dir === 'up' || e.dir === 'down';
  d.innerHTML = `<div class="row"><span class="ts">${fmtT(e.t)}</span><span class="dir">${DIRN[e.dir] || e.dir}</span>${pkt
    ? `<span class="id">${e.msgId}</span><span class="nm">${esc(e.name)}</span><span class="sn">#${e.serial}</span><span class="sm">${esc(e.summary)}<em>${e.len} B</em></span>`
    : `<span class="sm">${esc(e.summary)}</span>`}</div>`;
  if (e.hex) d.firstChild.onclick = () => toggleDetail(d, e);
  return d;
}
function toggleDetail(d, e) {
  const open = d.querySelector('.detail');
  if (open) { open.remove(); d.classList.remove('open'); return; }
  const f = decodeFrame(e.hex);
  const tbl = rows => `<table>${rows.map(([k, v]) => `<tr><td>${esc(k)}</td><td class="v">${esc(v)}</td></tr>`).join('')}</table>`;
  const x = document.createElement('div');
  x.className = 'detail';
  x.innerHTML = f ? `<div><h4>消息头</h4>${tbl(f.head)}</div><div><h4>消息体</h4>${f.body.length ? tbl(f.body) : '<p class="muted">无可解析字段</p>'}</div>
    <div><h4>反转义后 hex（高亮为消息头）</h4><pre class="hexdump">${hexDump(f.bytes, f.headLen)}</pre><h4 style="margin-top:10px">原始帧</h4><pre class="hexdump wrap">${esc(e.hex.replace(/(..)/g, '$1 ').toUpperCase())}</pre></div>`
    : `<pre class="hexdump">${esc(e.hex)}</pre>`;
  d.appendChild(x);
  d.classList.add('open');
  x.scrollIntoView({ block: 'nearest' });
}
function redrawLog() {
  logEl.innerHTML = '';
  const frag = document.createDocumentFragment();
  logs.filter(logMatch).slice(-LOG_SHOW).forEach(e => frag.appendChild(logRow(e)));
  logEl.appendChild(frag);
  logEl.scrollTop = logEl.scrollHeight;
  $('#lCount').textContent = `${logs.length} 条`;
}
function addLog(e) {
  if (e.seq <= lastSeq) return;
  lastSeq = e.seq;
  logs.push(e);
  if (logs.length > LOG_MAX) logs.splice(0, logs.length - LOG_MAX);
  if (e.dir === 'err' && page !== 'overview') toast(e.summary, 'err');   // 主屏日志可见时不再弹出
  if (logPaused || !logMatch(e)) return;
  logEl.appendChild(logRow(e));
  while (logEl.childElementCount > LOG_SHOW) logEl.firstChild.remove();
  if (logStick) logEl.scrollTop = logEl.scrollHeight;
  $('#lCount').textContent = `${logs.length} 条`;
}
let logES = null, logTimer = 0;
function connectLog() {
  clearTimeout(logTimer);
  if (logES) logES.close();
  const es = logES = new EventSource(api('log'));
  es.onmessage = m => { try { addLog(JSON.parse(m.data)); } catch (_) { } };
  es.onerror = () => { es.close(); if (logES === es) logTimer = setTimeout(connectLog, 2000); };
}
connectLog();
// 切换终端：清空并订阅新终端的日志（各终端流水号独立）
onSwitch(() => { logs = []; lastSeq = 0; logEl.innerHTML = ''; $('#lCount').textContent = ''; connectLog(); });

$('#lDir').onclick = e => { const b = e.target.closest('[data-d]'); if (!b) return; logDir = b.dataset.d; $$('#lDir button').forEach(x => x.classList.toggle('on', x === b)); redrawLog(); };
['#lId', '#lText', '#lHb'].forEach(s => $(s).addEventListener('input', redrawLog));
$('#lPause').onclick = e => { logPaused = !logPaused; e.target.textContent = logPaused ? '继续' : '暂停'; e.target.classList.toggle('on', logPaused); if (!logPaused) redrawLog(); };
$('#lClear').onclick = () => { logs = []; logEl.innerHTML = ''; $('#lCount').textContent = ''; };
function setLogMax(on) {
  $('#logTile').classList.toggle('max', on);
  $('#lMax').textContent = on ? '还原' : '放大';
  logEl.scrollTop = logEl.scrollHeight;
}
$('#lMax').onclick = () => setLogMax(!$('#logTile').classList.contains('max'));
document.addEventListener('keydown', e => { if (e.key === 'Escape' && $('#logTile').classList.contains('max') && page === 'overview') setLogMax(false); });
