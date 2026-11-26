// 报文分析与手动组包工具页
$('#tpDemo').onclick = () => {
  $('#tpHex').value = '7E 02 00 00 1C 01 38 00 00 00 01 00 01 00 00 00 00 00 00 00 02 01 5F 7A 64 06 C7 14 2C 00 14 01 90 00 5A 26 11 25 18 00 00 C8 7E';
  $('#tpParse').click();
};
$('#tpParse').onclick = async e => {
  const hex = $('#tpHex').value.trim();
  if (!hex) return toast('请输入十六进制报文');
  try {
    const r = await fetch('api/tool/parse', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ hex }) });
    const j = await r.json();
    if (!j.ok) { $('#tpMeta').textContent = '解析失败'; $('#tpMeta').className = 'tag err'; $('#tpBody').innerHTML = ''; return toast(j.error || '解析失败'); }
    $('#tpMeta').textContent = `${j.proto} · ${j.msgId} ${j.msgName} · ${j.crcOK ? '校验通过' : '校验失败'}`;
    $('#tpMeta').className = 'tag ' + (j.crcOK ? 'ok' : 'err');
    $('#tpBody').innerHTML = (j.fields || []).map(f => `<tr><td>${esc(f.name)}</td><td class="mono">${esc(f.hex)}</td><td>${esc(f.val)}</td></tr>`).join('');
  } catch (err) { toast('请求失败：' + err.message); }
};

const TB_TPLS = {
  '0002': { id: '0002', body: '' },
  '0102': { id: '0102', body: '31 32 33 34 35 36' },
  '0900': { id: '0900', body: 'F0 48 45 4C 4C 4F' },
  '0004': { id: '0004', body: '' },
};
$('#tbTpl').onchange = e => {
  const t = TB_TPLS[e.target.value];
  if (!t) return;
  $('#tbId').value = t.id;
  $('#tbBody').value = t.body;
};
async function buildToolFrame() {
  const r = await fetch('api/tool/build', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      version: +$('#tbVer').value,
      msgId: $('#tbId').value.trim(),
      seq: +$('#tbSeq').value || 1,
      phone: $('#tbPhone').value.trim(),
      bodyHex: $('#tbBody').value.trim(),
    }),
  });
  const j = await r.json();
  if (!j.ok) { toast(j.error || '组包失败'); return null; }
  $('#tbOut').value = j.hex;
  return j.hex;
}
$('#tbBuild').onclick = buildToolFrame;
$('#tbSend').onclick = async e => {
  const hex = await buildToolFrame();
  if (!hex) return;
  act('raw', { line: hex }, e.target, '已通过当前终端发送');
};
onPage('tools', () => {
  if (S) {
    setVal('tbPhone', S.conn.phone || '13800000001');
    $('#tbVer').value = S.conn.version === '2013' ? '2013' : S.conn.version === '2011' ? '2011' : '2019';
  }
  renderRef('msg');
});

const REF_DATA = {
  msg: [
    ['0x0100 / 0x8100', '终端注册 / 注册应答', '返回鉴权码（2019 制造商11/型号30/终端ID30）'],
    ['0x0102 / 0x8001', '终端鉴权 / 平台通用应答', '2019 带鉴权码长度 + IMEI(15) + 软件版本(20)'],
    ['0x0002 / 0x0004', '终端心跳 / 查询服务器时间', '8004 返回 6 字节 BCD 时间'],
    ['0x0200 / 0x0704', '位置汇报 / 批量补报', '离线盲区重连后自动以 0704 补报'],
    ['0x8103 / 0x8104', '设置 / 查询终端参数', '支持 100+ 项标准参数与主动安全 F364~F370'],
    ['0x9101 / 0x9102', '实时音视频请求 / 控制', '1078 RTP 流（01cd 头），支持双向语音对讲'],
    ['0x9208 / 0x1210~1212', '主动安全报警附件指令与上传', '苏标/粤标/黑标通用附件链路'],
  ],
  bits: [
    ['状态 bit0 / bit1', 'ACC 开 / 已定位', 'bit1=0 为未定位；bit0=0 为熄火'],
    ['状态 bit2 / bit3', '南纬 / 西经', '国内常用北纬东经均置 0'],
    ['状态 bit10 / bit11', '车辆油路断开 / 电路断开', '8500 车辆控制可联动'],
    ['报警 bit0 / bit1', '紧急报警 / 超速报警', 'bit0 需平台 8203 人工确认清除'],
    ['报警 bit2 / bit7', '疲劳驾驶 / 终端主电源欠压', 'bit2 可由模拟器自动判定'],
    ['报警 bit15 / bit20', '2019 违规行驶 / 进出区域', '0x0032 时段内行驶触发 bit15'],
  ],
  extra: [
    ['0x01 / 0x02 / 0x03', '里程(0.1km) / 油量(0.1L) / 速度', '0200 基础附加信息'],
    ['0x05 / 0x06', '胎压列表 / 车厢温度(℃)', 'JT/T 808-2019 新增'],
    ['0x25 / 0x2A / 0x2B', '扩展信号位 / IO 位 / 模拟量 AD0/1', '状态与报警页逐位开关'],
    ['0x64 ~ 0x67', 'ADAS / DSM / TPMS / BSD 报警', '主动安全附加信息'],
    ['附件命名规则', '类型_通道_报警类型_序号_报警编号.后缀', '如 00_64_6401_00_xxx.jpg（00图/01音频/02视频/03bin）'],
  ],
};
function renderRef(kind) {
  const el = $('#refTable');
  if (!el) return;
  $$('#refTabs button').forEach(b => b.classList.toggle('on', b.dataset.ref === kind));
  el.innerHTML = '<thead><tr><th>标识 / 位</th><th>名称</th><th>说明</th></tr></thead><tbody>' +
    (REF_DATA[kind] || []).map(([a, b, c]) => `<tr><td class="mono">${esc(a)}</td><td>${esc(b)}</td><td>${esc(c)}</td></tr>`).join('') + '</tbody>';
}
$('#refTabs')?.addEventListener('click', e => { const b = e.target.closest('button[data-ref]'); if (b) renderRef(b.dataset.ref); });
