// 桌面版（Wails / WebView2 无边框窗口）：自绘标题栏的拖动、双击最大化、窗口按钮与边缘缩放。
// 只在 WebView2 中生效（window.chrome.webview）；在普通浏览器中打开页面（开发调试）时不显示窗口按钮。
'use strict';
(() => {
  const wv = window.chrome && window.chrome.webview;
  if (!wv) return;
  const send = m => { try { wv.postMessage(m); } catch (_) { } };
  document.documentElement.classList.add('desktop');
  $('#winctl').hidden = false;

  const maxed = () => window.outerWidth >= screen.availWidth - 2 && window.outerHeight >= screen.availHeight - 2;
  const syncMax = () => document.documentElement.classList.toggle('win-max', maxed());
  window.addEventListener('resize', syncMax); syncMax();

  $('#winctl').onclick = e => {
    const b = e.target.closest('[data-win]'); if (!b) return;
    send({ min: 'Wm', max: 'Wt', close: 'Q' }[b.dataset.win]);
  };

  // 标题栏空白处拖动窗口；按钮、输入框等可交互元素除外
  const bar = $('#titlebar');
  const interactive = el => el.closest('button, a, input, select, textarea, .status, .ver, [data-copy]');
  bar.addEventListener('mousedown', e => {
    if (e.button !== 0 || e.detail > 1 || interactive(e.target) || edge(e)) return;
    e.preventDefault(); send('drag');
  });
  bar.addEventListener('dblclick', e => { if (!interactive(e.target)) send('Wt'); });

  // 边缘缩放：鼠标靠近窗口边缘时显示缩放光标，按下交给系统处理
  const B = 5;
  function edge(e) {
    if (maxed()) return '';
    const x = e.clientX, y = e.clientY, w = window.innerWidth, h = window.innerHeight;
    const v = y < B ? 'n' : y > h - B ? 's' : '', hz = x < B ? 'w' : x > w - B ? 'e' : '';
    return v || hz ? v + hz + '-resize' : '';
  }
  let cur = '';
  window.addEventListener('mousemove', e => {
    const c = e.buttons ? '' : edge(e);
    if (c !== cur) { cur = c; document.documentElement.style.cursor = c; }
  });
  window.addEventListener('mousedown', e => {
    const c = e.button === 0 && edge(e);
    if (c) { e.preventDefault(); e.stopPropagation(); send('resize:' + c); }
  }, true);
})();
