// 压力测试（进程级，/api/stress）：参数、启动/停止、实时统计与速率曲线
'use strict';
let STRESS = null;
const ST_FIELDS = [
  ['host', '目标服务器', 'text', '127.0.0.1'], ['port', '端口', 'number', 7611],
  ['transport', '传输方式', 'select', 'tcp', { tcp: 'TCP', udp: 'UDP' }], ['version', '协议版本', 'select', '2019', { 2019: 'JT/T 808-2019', 2013: 'JT/T 808-2013' }],
  ['count', '终端数量（≤2000）', 'number', 100], ['phoneStart', '起始手机号（依次递增）', 'text', '19900000001'],
  ['interval', '位置汇报间隔（秒）', 'number', 10], ['heartbeat', '心跳间隔（秒）', 'number', 30],
  ['ramp', '启动速率（个/秒，≤500）', 'number', 50], ['ackTimeout', '应答超时（秒）', 'number', 10],
  ['duration', '运行时长（秒，0 = 手动停止）', 'number', 0],
];
$('#stForm').innerHTML = ST_FIELDS.map(([k, n, type, d, opts]) => `<div class="field"><label class="lbl" for="st_${k}">${n}</label>${type === 'select'
  ? `<select id="st_${k}">${Object.entries(opts).map(([v, t]) => `<option value="${v}">${t}</option>`).join('')}</select>`
  : `<input id="st_${k}" ${type === 'number' ? 'type="number" min="0"' : ''} class="${['port', 'phoneStart', 'host'].includes(k) ? 'mono' : ''}">`}</div>`).join('');
const stSaved = JSON.parse(localStorage.getItem('jt808.stress') || '{}');
ST_FIELDS.forEach(([k, , , d]) => $('#st_' + k).value = stSaved[k] ?? d);
function stCfg() {
  const c = {};
  ST_FIELDS.forEach(([k, , type]) => { const v = $('#st_' + k).value.trim(); c[k] = type === 'number' ? +v : v; });
  return c;
}
$('#stFromTerm').onclick = () => {
  if (!S) return;
  $('#st_host').value = S.conn.host; $('#st_port').value = S.conn.port;
  $('#st_transport').value = S.conn.transport || 'tcp'; $('#st_version').value = S.conn.version;
  toast(`已填入终端 #${S.id} 的网关 ${S.conn.host}:${S.conn.port}`, 'info');
};
async function stressOp(body, btn) {
  if (btn) btn.disabled = true;
  try {
    const j = await (await fetch('api/stress', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })).json();
    if (!j.ok) { toast(j.error || '操作失败'); return false; }
    return true;
  } catch (e) { toast('请求失败：' + e.message); return false; }
  finally { if (btn) btn.disabled = false; pollStress(); }
}
$('#stStart').onclick = async e => {
  const c = stCfg();
  localStorage.setItem('jt808.stress', JSON.stringify(c));
  if (c.count > 500 && !(await confirmBox(`启动 ${c.count} 个虚拟终端连接 ${c.host}:${c.port}？请确认目标网关允许压测。`))) return;
  if (await stressOp({ op: 'start', ...c }, e.target)) toast(`压力测试已开始：${c.count} 个终端`, 'ok');
};
$('#stStop').onclick = async e => { if (await stressOp({ op: 'stop' }, e.target)) toast('压力测试已停止', 'ok'); };

const stNum = n => Number(n || 0).toLocaleString('zh-CN');
function stChart(series) {
  const W = 600, H = 180, P = { l: 34, r: 30, t: 8, b: 18 };
  if (!series.length) return '<div class="empty">开始压测后显示每秒收发条数</div>';
  const n = 120, maxR = Math.max(1, ...series.map(p => Math.max(p.sent, p.recv)));
  const maxO = Math.max(1, ...series.map(p => p.online));
  const x = i => P.l + (W - P.l - P.r) * (i + n - series.length) / (n - 1);
  const yR = v => H - P.b - (H - P.t - P.b) * v / maxR, yO = v => H - P.b - (H - P.t - P.b) * v / maxO;
  const line = k => series.map((p, i) => `${x(i).toFixed(1)},${yR(p[k]).toFixed(1)}`).join(' ');
  const area = `${x(0).toFixed(1)},${H - P.b} ` + series.map((p, i) => `${x(i).toFixed(1)},${yO(p.online).toFixed(1)}`).join(' ') + ` ${x(series.length - 1).toFixed(1)},${H - P.b}`;
  return `<svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="none">
    <line x1="${P.l}" y1="${H - P.b}" x2="${W - P.r}" y2="${H - P.b}" stroke="var(--border)"/>
    <line x1="${P.l}" y1="${P.t}" x2="${W - P.r}" y2="${P.t}" stroke="var(--border)" stroke-dasharray="3 4"/>
    <polygon points="${area}" fill="var(--lime-2)" opacity=".18"/>
    <polyline points="${line('recv')}" fill="none" stroke="var(--primary)" stroke-width="2" vector-effect="non-scaling-stroke"/>
    <polyline points="${line('sent')}" fill="none" stroke="var(--ok)" stroke-width="2" vector-effect="non-scaling-stroke"/>
    <text class="ax" x="${P.l - 6}" y="${P.t + 8}" text-anchor="end">${stNum(Math.round(maxR))}</text><text class="ax" x="${P.l - 6}" y="${H - P.b}" text-anchor="end">0</text>
    <text class="ax" x="${W - P.r + 4}" y="${P.t + 8}">${stNum(maxO)}</text></svg>`;
}
function renderStress(v) {
  const s = v.stats, on = v.running;
  $('#tStressN').hidden = $('#navStress').hidden = !on;
  $('#tStressN').textContent = $('#navStress').textContent = on ? stNum(s.online) : '';
  if (page !== 'stress') return;
  $('#stState').className = 'tag ' + (on ? 'ok' : '');
  $('#stState').textContent = on ? '运行中' : (v.elapsed ? '已停止' : '未运行');
  $('#stStart').disabled = on; $('#stStop').disabled = !on;
  ST_FIELDS.forEach(([k]) => $('#st_' + k).disabled = on);
  if (on) ST_FIELDS.forEach(([k]) => { if (v.config[k] !== undefined) $('#st_' + k).value = v.config[k]; });
  const target = v.config.count || 0;
  $('#stKpis').innerHTML = [['在线 / 目标', `${stNum(s.online)}<small>/ ${stNum(target)}</small>`], ['发送', `${stNum(s.sentRate)}<small>条/秒</small>`],
    ['接收', `${stNum(s.recvRate)}<small>条/秒</small>`], ['应答延迟 P95', `${s.latP95 || 0}<small>ms</small>`]]
    .map(([a, b]) => `<div><span>${a}</span><b>${b}</b></div>`).join('');
  $('#stChart').innerHTML = stChart(v.series || []);
  $('#stElapsed').textContent = v.elapsed ? '运行 ' + dur(v.elapsed * 1000) : '';
  $('#stKV').innerHTML = [['已启动', s.spawned], ['已连接', s.connected], ['注册成功', s.regOK], ['注册失败', s.regFail],
    ['鉴权失败', s.authFail], ['连接失败', s.connFail], ['累计发送', s.sent], ['累计接收', s.recv], ['应答成功', s.acks], ['应答失败', s.nacks],
    ['应答超时', s.timeouts], ['重连', s.reconnects], ['发送错误', s.sendErrors], ['延迟 平均', s.latAvg + ' ms'], ['延迟 P50', s.latP50 + ' ms'], ['延迟 P99', s.latP99 + ' ms']]
    .map(([a, b]) => `<dt>${a}</dt><dd class="mono">${typeof b === 'number' ? stNum(b) : esc(b)}</dd>`).join('');
  $('#stErr').hidden = !v.lastError; $('#stErr').textContent = v.lastError ? '最近错误：' + v.lastError : '';
}
async function pollStress() {
  try { STRESS = await (await fetch('api/stress', { cache: 'no-store' })).json(); renderStress(STRESS); } catch (_) { }
}
let stTick = 0;
setInterval(() => { if (page === 'stress' || (STRESS && STRESS.running) || ++stTick % 5 === 0) pollStress(); }, 1000);
onPage('stress', pollStress);
pollStress();
