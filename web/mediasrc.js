// 媒体源：图片 / 视频文件 / 电脑摄像头作为报警附件的画面来源。
// 视频与摄像头在前端循环播放，每秒 canvas 抓一帧 JPEG 回传后端（POST frame），
// 平台 9208 触发附件上传时，后端取最新一帧作为图片附件；视频附件用上传的源视频。
'use strict';

let msStream = null;      // 摄像头 MediaStream
let msTimer = null;       // 抓帧定时器
let msPending = '';       // 文件选择器等待的类型（image/video）
let msUpTick = 0;         // 上传时间戳：变化时刷新预览地址
let msLoaded = '';        // 已加载预览的签名（避免轮询时重置播放）
const msCanvas = document.createElement('canvas');

function msStop() {
  if (msStream) { msStream.getTracks().forEach(t => t.stop()); msStream = null; }
  if (msTimer) { clearInterval(msTimer); msTimer = null; }
  const v = $('#msVideo');
  v.pause(); v.srcObject = null; v.removeAttribute('src');
  $('#msImg').removeAttribute('src');
  msLoaded = '';
}

// 抓帧：视频/摄像头画面 → JPEG → 后端缓存（窗口隐藏时也继续，保证随时能截到当前画面）
function msStartCapture() {
  if (msTimer) return;
  msTimer = setInterval(async () => {
    const v = $('#msVideo');
    if (!v.videoWidth || v.readyState < 2) return;
    const w = Math.min(v.videoWidth, 640), h = Math.round(v.videoHeight * w / v.videoWidth);
    msCanvas.width = w; msCanvas.height = h;
    msCanvas.getContext('2d').drawImage(v, 0, 0, w, h);
    const blob = await new Promise(res => msCanvas.toBlob(res, 'image/jpeg', 0.85));
    if (!blob) return;
    try { await fetch(api('frame'), { method: 'POST', body: blob }); } catch (_) { }
  }, 1000);
}

async function msCamera() {
  if (!navigator.mediaDevices?.getUserMedia) return toast('当前环境不支持摄像头（需 localhost 或 HTTPS）');
  try {
    const stream = await navigator.mediaDevices.getUserMedia({ video: { width: { ideal: 1280 } }, audio: false });
    msStop();
    msStream = stream;
    const v = $('#msVideo');
    v.srcObject = stream;
    await v.play();
    const r = await fetch(api('mediasrc') + '?kind=camera', { method: 'POST' });
    const j = await r.json();
    if (!j.ok) { msStop(); return toast(j.error || '设置失败'); }
    toast('摄像头已开启，循环抓帧中', 'ok');
    msStartCapture();
    poll();
  } catch (e) { toast('摄像头打开失败：' + e.message + '（请允许摄像头权限）'); }
}

$('#msKind').onclick = async e => {
  const kind = e.target.dataset.ms;
  if (kind === undefined) return;
  const cur = S?.mediaSrc?.src || '';
  if (kind === cur && (kind !== 'camera' || msStream)) return; // 摄像头在刷新/切换终端后需重新开启
  if (kind === 'camera') { msCamera(); return; }
  if (kind === 'image' || kind === 'video') {
    msPending = kind;
    const f = $('#msFile');
    f.accept = kind === 'image' ? 'image/jpeg,image/png,image/gif' : 'video/mp4,.m4v';
    f.click();
    return;
  }
  // 恢复默认
  msStop();
  const r = await fetch(api('mediasrc'), { method: 'DELETE' });
  const j = await r.json();
  if (j.ok) { toast('已恢复默认合成画面', 'ok'); poll(); } else toast(j.error || '操作失败');
};

$('#msFile').onchange = async e => {
  const f = e.target.files[0];
  e.target.value = '';
  if (!f || !msPending) return;
  if (f.size > 64 << 20) return toast('文件过大（最大 64 MB）');
  const ext = (f.name.match(/\.[^.]+$/)?.[0] || '').toLowerCase();
  try {
    const r = await fetch(api('mediasrc') + '?kind=' + msPending + '&ext=' + encodeURIComponent(ext), { method: 'POST', body: f });
    const j = await r.json();
    if (!j.ok) return toast(j.error || '上传失败');
    msStop();
    msUpTick = Date.now();
    toast(`媒体源已设置为${msPending === 'image' ? '图片' : '视频'}「${f.name}」`, 'ok');
    poll();
  } catch (err) { toast('上传失败：' + err.message); }
};

const MS_NAME = { '': '默认合成图', image: '图片文件', video: '视频文件', camera: '电脑摄像头' };
function renderMs(s) {
  const ms = s.mediaSrc || { src: '', frameAge: -1 };
  const src = ms.src || '';
  $$('#msKind button').forEach(b => b.classList.toggle('on', b.dataset.ms === src));
  $('#msTag').textContent = MS_NAME[src] || src;
  $('#msTag').className = 'tag ' + (src ? 'ok' : '');
  $('#msPreviewWrap').hidden = !src;
  $('#msVideo').hidden = src !== 'video' && src !== 'camera';
  $('#msImg').hidden = src !== 'image';

  // 加载/刷新预览（签名未变不动它，避免打断播放）
  const sig = TID + ':' + src + ':' + msUpTick;
  if (src && sig !== msLoaded) {
    msLoaded = sig;
    if (src === 'image') $('#msImg').src = api('mediasrc') + '?t=' + msUpTick;
    if (src === 'video') {
      const v = $('#msVideo');
      v.src = api('mediasrc') + '?t=' + msUpTick;
      v.play().catch(() => { });
      msStartCapture();
    }
  }
  if (src === 'camera' && msStream) msStartCapture();

  const frameText = ms.frameAge >= 0 ? `最近一帧 ${ms.frameAge} 秒前（${(ms.frameSize / 1024).toFixed(0)} KB）` : '';
  // 抓帧靠浏览器定时器，窗口最小化 / 标签页在后台会被节流；此时附件仍沿用最后一帧，不退回合成图
  const staleTip = ms.stale ? `前端抓帧已暂停 ${ms.frameAge} 秒（窗口最小化或标签页在后台会被节流），附件仍沿用这一帧。` : '';
  $('#msHint').textContent = src === ''
    ? '默认：附件图片是合成彩条测试图，视频是内置 alarm.mp4。选择图片/视频文件或摄像头后，上传附件时截取当前播放画面（每秒抓帧，附件始终用最新一帧）。'
    : src === 'image'
      ? '图片附件将使用上传的这张图片；视频附件仍用内置 alarm.mp4。'
      : src === 'camera'
        ? (frameText ? `摄像头循环抓帧中，${frameText}。` : '等待摄像头画面…') + staleTip + '图片附件取当前画面；摄像头无源视频，视频附件仍用内置 alarm.mp4。'
        : (frameText ? `视频循环播放中，${frameText}。` : '视频加载中…') + staleTip + '图片附件截取当前播放画面，视频附件使用上传的源视频。';
}
onState(s => page === 'adas' && renderMs(s));
onPage('adas', () => S && renderMs(S));
onSwitch(() => { msStop(); msUpTick = 0; });
