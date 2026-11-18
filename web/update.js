// 检查更新：启动后和每小时检查一次 GitHub 最新发布；页面重新可见时若距上次检查超过 10 分钟也补一次。
// 点击版本号可手动检查（强制绕过服务端缓存）。
// 安装版可在程序内下载并安装（校验 SHA256 后启动安装程序，本程序退出）；其它情况打开发布页下载。
'use strict';
(() => {
  const SKIP = 'jt808.updSkip';
  let info = null, polling = null;
  let lastCheck = 0, lastErr = ''; // 上次成功的检查时间 / 最近一次错误（自动检查失败不弹窗，只写进版本号提示）
  const badge = $('#updBadge'), modal = $('#updModal');
  const mb = n => (n / 1048576).toFixed(1) + ' MB';

  // 把检查结果写进版本号的悬停提示（替换上一次的记录，避免累积）
  function note(text) {
    const el = $('#verTag');
    if (!el) return;
    el.title = (el.title.replace(/\n上次检查更新[^\n]*/g, '') + '\n' + text).trim();
  }

  async function check(manual) {
    try {
      const j = await fetchJSON('api/update' + (manual ? '?force=1' : ''), { cache: 'no-store' });
      if (!j.ok) {
        lastErr = j.error || '检查失败';
        note(`上次检查更新失败：${lastErr}（点击版本号重试）`);
        if (manual) toast(j.error || '检查更新失败');
        return;
      }
      info = j.info;
      lastErr = ''; lastCheck = Date.now();
      note(`上次检查更新 ${new Date().toLocaleTimeString('zh-CN')} → 最新 ${info.latest}`);
      badge.hidden = !info.newer;
      badge.textContent = `新版本 ${info.latest}`;
      badge.title = `当前 ${info.current}，点击查看更新`;
      if (!info.newer) { if (manual) toast(/^v?\d/.test(info.current) ? `已是最新版本（${info.current}）` : `最新发布 ${info.latest}；当前为开发版 ${info.current}`, 'ok'); return; }
      if (manual || localStorage.getItem(SKIP) !== info.latest) show();
    } catch (e) {
      lastErr = e.message;
      note(`上次检查更新失败：${lastErr}（点击版本号重试）`);
      if (manual) toast('检查更新失败：' + e.message);
    }
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
      const j = await fetchJSON('api/open', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ url }) });
      if (!j.ok) window.open(url, '_blank');
    } catch (_) { window.open(url, '_blank'); }
  }

  async function install() {
    setBusy(true);
    $('#updProg').hidden = false;
    $('#updProgText').textContent = '准备下载…';
    try {
      const j = await fetchJSON('api/update/install', { method: 'POST' });
      if (!j.ok) { toast(j.error); setBusy(false); return; }
    } catch (e) { toast('更新失败：' + e.message); setBusy(false); return; }
    clearInterval(polling);
    polling = setInterval(async () => {
      let p; try { p = await fetchJSON('api/update/progress', { cache: 'no-store' }); } catch (_) { return; }
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
  setInterval(() => check(false), 3600 * 1000); // 每小时检查一次
  // 窗口恢复可见时补查：桌面版常年开着，只靠定时器最多要等一小时才知道有新版本
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden && Date.now() - lastCheck > 10 * 60 * 1000) check(false);
  });
})();
