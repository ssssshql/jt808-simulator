// JT/T 809 上下级平台模拟页
async function j809Op(body, btn, okMsg) {
  if (btn) btn.disabled = true;
  try {
    const r = await fetch('api/jt809', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const j = await r.json();
    if (!j.ok) { toast(j.error || '操作失败'); return false; }
    if (okMsg) toast(okMsg, 'ok');
    await poll809();
    return true;
  } catch (e) { toast('请求失败：' + e.message); return false; }
  finally { if (btn) btn.disabled = false; }
}

async function poll809() {
  if (page !== 'jt809') return;
  try {
    const d = await (await fetch('api/jt809', { cache: 'no-store' })).json();
    const st = $('#j809CliTag');
    st.textContent = d.cliStatus === 'online' ? '已登录' : d.cliStatus === 'connecting' ? '连接中' : '离线';
    st.className = 'tag ' + (d.cliStatus === 'online' ? 'ok' : d.cliStatus === 'connecting' ? 'warn' : '');
    $('#j809CliStat').textContent = `上行 ${d.cliUp} · 下行 ${d.cliDn}`;
    const sv = $('#j809SrvTag');
    sv.textContent = d.srvRunning ? `监听 :${d.cfg.listenPort}（${d.srvClients} 连接）` : '已停止';
    sv.className = 'tag ' + (d.srvRunning ? 'ok' : '');
    $('#j809SrvStat').textContent = `收 ${d.srvRx} · 回 ${d.srvTx}`;
    const c = d.cfg;
    if (!editing.has('j809Host')) setVal('j809Host', c.host);
    if (!editing.has('j809Port')) setVal('j809Port', c.port);
    if (!editing.has('j809Center')) setVal('j809Center', c.centerId);
    if (!editing.has('j809User')) setVal('j809User', c.userId);
    if (!editing.has('j809Pass')) setVal('j809Pass', c.password);
    if (!editing.has('j809Plate')) setVal('j809Plate', c.plate);
    if (!editing.has('j809SrvPort')) setVal('j809SrvPort', c.listenPort);
    $('#j809Enc').checked = !!c.encrypt;
    $('#j809Logs').innerHTML = (d.logs || []).slice().reverse().map(x =>
      `<tr><td class="mono">${esc(x.t)}</td><td>${esc(x.role)}</td><td>${esc(x.dir)}</td><td class="mono">${esc(x.id)}</td><td>${esc(x.name)}</td><td class="r">${x.len}</td><td class="mono" style="max-width:340px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${esc(x.hex)}</td></tr>`
    ).join('') || '<tr><td colspan="7" class="muted" style="text-align:center">暂无 809 报文</td></tr>';
  } catch (_) { }
}

$('#j809CliStart').onclick = e => j809Op({
  op: 'startClient', host: $('#j809Host').value.trim(), port: +$('#j809Port').value,
  centerId: +$('#j809Center').value, userId: +$('#j809User').value, password: $('#j809Pass').value,
  plate: $('#j809Plate').value.trim(), encrypt: $('#j809Enc').checked ? 1 : 0,
}, e.currentTarget, '下级平台已发起连接');
$('#j809CliStop').onclick = e => j809Op({ op: 'stopClient' }, e.currentTarget, '已断开');
$('#j809SrvStart').onclick = e => j809Op({ op: 'startServer', listenPort: +$('#j809SrvPort').value }, e.currentTarget, '上级平台已启动');
$('#j809SrvStop').onclick = e => j809Op({ op: 'stopServer' }, e.currentTarget, '上级平台已停止');
$$('[data-j809]').forEach(b => b.onclick = () => j809Op({ op: 'send', subId: b.dataset.j809 }, b, '已发送 809 报文'));
$('#j809Clr').onclick = () => j809Op({ op: 'clearLog' }, null, '已清空');
onPage('jt809', poll809);
setInterval(poll809, 1500);
