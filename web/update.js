// 检查更新：启动后和每 6 小时检查一次 GitHub 最新发布；点击版本号可手动检查。
// 安装版可在程序内下载并安装（校验 SHA256 后启动安装程序，本程序退出）；其它情况打开发布页下载。
'use strict';
(() => {
  const SKIP = 'jt808.updSkip';
  let info = null, polling = null;
  const badge = $('#updBadge'), modal = $('#updModal');
  const mb = n => (n / 1048576).toFixed(1) + ' MB';

  async function check(manual) {
    try {
      const j = await (await fetch('api/update' + (manual ? '?force=1' : ''), { cache: 'no-store' })).json();
      if (!j.ok) { if (manual) toast(j.error || '检查更新失败'); return; }
      info = j.info;
      badge.hidden = !info.newer;
      badge.textContent = `新版本 ${info.latest}`;
      badge.title = `当前 ${info.current}，点击查看更新`;
      if (!info.newer) { if (manual) toast(/^v?\d/.test(info.current) ? `已是最新版本（${info.current}）` : `最新发布 ${info.latest}；当前为开发版 ${info.current}`, 'ok'); return; }
      if (manual || localStorage.getItem(SKIP) !== info.latest) show();
    } catch (e) { if (manual) toast('检查更新失败：' + e.message); }
  }

  function show() {
    if (!info) return;
    $('#updTitle').textContent = `发现新版本 ${info.latest}`;
    const d = info.published ? new Date(info.published).toLocaleDateString('zh-CN') : '';
    $('#updSub').textContent = `当前 ${info.current}${d ? ' · 发布于 ' + d : ''}${info.setupSize ? ' · ' + mb(info.setupSize) : ''}`;
    const notes = (info.notes || '').trim();
    $('#updNotes').textContent = notes; $('#updNotes').hidden = !notes;
    $('#updGo').hidden = !info.canInstall;
    $('#updPage').textContent = info.canInstall ? '发布页' : '去下载';
    $('#updPage').classList.toggle('primary', !info.canInstall);
    $('#updProg').hidden = true;
    setBusy(false);
    modal.hidden = false;
  }
  const hide = () => { modal.hidden = true; };
  const setBusy = b => ['#updGo', '#updSkip', '#updLater', '#updPage'].forEach(s => ($(s).disabled = b));

  async function openPage() {
    const url = info?.url || 'https://github.com/ssssshql/jt808-simulator/releases/latest';
    try {
      const j = await (await fetch('api/open', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ url }) })).json();
      if (!j.ok) window.open(url, '_blank');
    } catch (_) { window.open(url, '_blank'); }
  }

  async function install() {
    setBusy(true);
    $('#updProg').hidden = false;
    $('#updProgText').textContent = '准备下载…';
    try {
      const j = await (await fetch('api/update/install', { method: 'POST' })).json();
      if (!j.ok) { toast(j.error); setBusy(false); return; }
    } catch (e) { toast('更新失败：' + e.message); setBusy(false); return; }
    clearInterval(polling);
    polling = setInterval(async () => {
      let p; try { p = await (await fetch('api/update/progress', { cache: 'no-store' })).json(); } catch (_) { return; }
      const pct = p.total ? Math.min(100, p.done / p.total * 100) : 0;
      $('#updBar').style.width = (p.state === 'downloading' ? pct : 100) + '%';
      $('#updProgText').textContent = {
        downloading: `正在下载 ${mb(p.done)}${p.total ? ' / ' + mb(p.total) : ''}`,
        verifying: '正在校验安装包…',
        launching: '正在启动安装程序，模拟器即将关闭…',
        error: '更新失败：' + (p.error || ''),
      }[p.state] || '';
      if (p.state === 'error') { clearInterval(polling); setBusy(false); }
    }, 400);
  }

  badge.onclick = show;
  $('#verTag').onclick = () => check(true);
  $('#verTag').style.cursor = 'pointer';
  $('#updLater').onclick = hide;
  $('#updSkip').onclick = () => { if (info) localStorage.setItem(SKIP, info.latest); hide(); };
  $('#updPage').onclick = openPage;
  $('#updGo').onclick = install;
  modal.addEventListener('mousedown', e => { if (e.target === modal && !$('#updGo').disabled) hide(); });

  setTimeout(() => check(false), 4000);
  setInterval(() => check(false), 6 * 3600 * 1000);
})();
