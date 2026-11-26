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
});
