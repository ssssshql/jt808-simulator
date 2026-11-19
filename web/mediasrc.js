// 媒体源：图片 / 视频文件 / 电脑摄像头作为报警附件的画面来源，按通道独立设置
// （64 ADAS / 65 DSM / 66 TPMS / 67 BSD，0 = 全部通道；通道未设置时继承「全部通道」）。
// 视频与摄像头在前端循环播放，每秒 canvas 抓一帧 JPEG 回传后端（POST frame，按通道留最近 30 帧）；
// 同时 MediaRecorder 每约 6 秒录一段回传（POST videoclip），后端只留最近一段。
// 平台 9208 触发附件上传时，后端按通道从帧历史取不同时刻的画面做图片附件（水印时间递减），
// 视频附件用最近一段录制，没有录制则用该通道上传的源视频，最后回退内置 alarm.mp4。
'use strict';

let msCh = '0';           // 当前设置的通道：'0' = 全部通道
let msStream = null;      // 摄像头 MediaStream
let msTimer = null;       // 抓帧定时器
let msH264Timer = null;   // 15fps H.264 实时编码推流定时器
let msH264Enc = null;     // WebCodecs VideoEncoder
let msH264Dims = '';
let msPending = '';       // 文件选择器等待的类型（image/video）
let msUpTick = 0;         // 上传时间戳：变化时刷新预览地址
let msLoaded = '';        // 已加载预览的签名（避免轮询时重置播放）
let msDevId = '';         // 选中的摄像头设备
let msRec = null;         // 当前 MediaRecorder
let msRecWant = false;    // 是否应持续录制（视频/摄像头源）
let msRecFail = 0;        // 连续录不出数据的段数
let msRecDead = false;    // 录制已放弃（换源后重置）
let msCapStream = null;   // <video> captureStream 得到的流（复用，避免重复创建）
let msCapFor = '';        // 上面的流对应的媒体地址
const msCamTried = new Set(); // 本次页面加载已自动尝试开启摄像头的通道（避免每次轮询重试）
const msCanvas = document.createElement('canvas');
const MS_NAME = { '': '默认合成图', image: '图片文件', video: '视频文件', camera: '电脑摄像头' };
const MS_SRC = { frame: '当前播放画面（抓帧）', image: '上传的图片文件', synthetic: '合成彩条测试图' };
const MS_VSRC = { clip: '最近一段录制', source: '上传的源视频', builtin: '内置 alarm.mp4（3 秒彩条）' };
// 录制：每段 6 个 1 秒分片 ≈ 6 秒，停下后立刻回传并重开，后端只留最近一段做视频附件。
// MP4 优先（协议附件惯例是 .mp4），不支持时退 WebM；候选格式逐个真录一遍才算数
// （isTypeSupported 说支持也可能 start 就报错：WebView2 缺 H.264 编码器、codecs 串不被接受等）
const MS_REC_CHUNKS = 6;
const MS_REC_CANDS = ['video/mp4;codecs=avc1.42E01E', 'video/mp4', 'video/webm;codecs=vp8', 'video/webm;codecs=vp9', 'video/webm'];
let msRecMime = '';           // 实测能出数据的录制格式（出过一段成功后固定）
let msRecBad = new Set();     // 录不出数据的格式（本次会话内不再尝试）
let msRecErr = '';            // 最近一次失败原因（页面显示用）

const msChName = () => (msCh === '0' ? '全部通道' : '通道 ' + msCh);
// 媒体源接口统一带通道参数
const msUrl = (p, extra) => api(p) + '?ch=' + msCh + (extra ? '&' + extra : '');

function msStop() {
  msRecStop();
  msStopH264();
  if (msCapStream) { msCapStream.getTracks().forEach(t => t.stop()); msCapStream = null; msCapFor = ''; }
  if (msStream) { msStream.getTracks().forEach(t => t.stop()); msStream = null; }
  if (msTimer) { clearInterval(msTimer); msTimer = null; }
  const v = $('#msVideo');
  v.pause(); v.srcObject = null; v.removeAttribute('src');
  $('#msImg').removeAttribute('src');
  const pv = $('#msVid');
  pv.pause();
  if (pv.dataset.url) { URL.revokeObjectURL(pv.dataset.url); delete pv.dataset.url; }
  pv.removeAttribute('src');
  $('#msVidWrap').hidden = true;
  $('#msVidTag').textContent = '';
  $('#msCamWrap').hidden = true;
  msLoaded = '';
}

// 录制用的视频流：摄像头直接用 getUserMedia 的流；视频文件用 <video>.captureStream()（同一地址复用）
function msRecStream() {
  if (msStream) return msStream;
  const v = $('#msVideo');
  const key = v.currentSrc || v.src || '';
  if (!key) return null;
  if (msCapStream && msCapFor === key) return msCapStream;
  if (typeof v.captureStream !== 'function') return null;
  try { msCapStream = v.captureStream(); msCapFor = key; } catch (_) { return null; }
  return msCapStream;
}

// 开始一段录制：录满 MS_REC_CHUNKS 秒自动停下 → 回传后端 → 继续录下一段
function msRecStart() {
  if (msRec || msRecDead || !msRecWant) return;
  if (typeof MediaRecorder === 'undefined') { msRecDead = true; msRecErr = '浏览器不支持 MediaRecorder'; return; }
  const stream = msRecStream();
  if (!stream || !stream.getVideoTracks().length) return;
  const cands = (msRecMime ? [msRecMime] : MS_REC_CANDS).filter(m => !msRecBad.has(m));
  if (!cands.length) { msRecDead = true; msRecErr = '所有录制格式都录不出数据'; console.warn(msRecErr); poll(); return; }
  for (const mime of cands) {
    if (msRecOpen(stream, mime)) return; // 成功开录
  }
  msRecDead = true;
  msRecErr = '录制启动失败：' + (msRecErr || '未知原因');
  console.warn(msRecErr + '，视频附件用内置 alarm.mp4');
  poll();
}

// 用指定格式开录；构造/start 失败返回 false（换下一个候选）
function msRecOpen(stream, mime) {
  let rec;
  try {
    rec = new MediaRecorder(stream, { mimeType: mime, videoBitsPerSecond: 1500000 });
  } catch (e) { msRecErr = mime + ' 构造失败：' + e.message; return false; }
  const ch = msCh; // 捕获当前通道：中途切通道也不会把片段传到别的通道
  const chunks = [];
  let n = 0;
  // 看门狗：有的环境 start() 与 state 都正常，但编码器不工作、一个分片都不给（mp4 编码器缺失时就是这样），
  // 只靠 ondataavailable 计数会永远等不到 stop，于是永远没有片段 → 视频附件永远是内置彩条片。
  // 2 秒内分片数没增长就主动停掉，交给 onstop 判定该格式不可用并换下一个。
  let lastN = 0;
  const wd = setInterval(() => {
    if (msRec !== rec) { clearInterval(wd); return; }
    if (n === lastN) {
      clearInterval(wd);
      if (n === 0) msRecErr = mime + ' 录制没有数据';
      try { rec.stop(); } catch (_) { } // 有数据但出得慢：停下来照样能上传，只是片段短一点
    }
    lastN = n;
  }, 2000);
  rec.onerror = e => {
    clearInterval(wd);
    msRecErr = '录制出错：' + ((e.error && (e.error.name || e.error.message)) || 'unknown');
    if (msRec === rec) msRec = null;
    if (mime === msRecMime) msRecMime = ''; // 曾成功的格式失效了，下次重新挑
    msRecBad.add(mime);                     // 这个格式这次肯定不行，换下一个
    if (msRecWant) setTimeout(msRecStart, 200); else poll();
  };
  rec.ondataavailable = e => {
    if (e.data && e.data.size) chunks.push(e.data);
    if (++n >= MS_REC_CHUNKS && rec.state !== 'inactive') { clearInterval(wd); try { rec.stop(); } catch (_) { } }
  };
  rec.onstop = async () => {
    clearInterval(wd);
    if (msRec === rec) msRec = null;
    if (!msRecWant) return; // 拆流（切源/切通道/关页面）时不回传半截片段，后端保留上一段完整的
    const blob = new Blob(chunks, { type: rec.mimeType || mime });
    if (blob.size > 2000) {
      msRecFail = 0;
      if (msRecMime !== mime) { msRecMime = mime; console.info('录制格式确定为 ' + mime); }
      try {
        await fetch(api('videoclip') + '?ch=' + ch, { method: 'POST', headers: { 'Content-Type': blob.type }, body: blob });
        poll(); // 让页面马上显示「最近一段录制」
      } catch (_) { }
    } else {
      if (!msRecMime) { // 这个格式一次都没录出数据（编码器不可用）：换下一个候选，不计入放弃阈值
        msRecBad.add(mime);
        msRecErr = mime + ' 录不出数据';
        msRecFail = 0;
      } else if (++msRecFail >= 3) { // 曾经能录，突然连录三段空（流断了）就放弃
        msRecDead = true; msRecWant = false;
        console.warn((msRecErr || '录制无数据') + '，视频附件用内置 alarm.mp4');
        poll();
        return;
      }
    }
    if (msRecWant) setTimeout(msRecStart, 100);
  };
  try { rec.start(1000); } catch (e) { msRecErr = mime + ' 启动失败：' + e.message; return false; }
  msRec = rec;
  return true;
}

function msRecStop() {
  msRecWant = false;
  msRecFail = 0;
  msRecDead = false;
  msRecBad = new Set(); // 换了源/重新开摄像头，格式再试一遍
  const rec = msRec;
  msRec = null;
  if (rec && rec.state !== 'inactive') { try { rec.stop(); } catch (_) { } }
}

function msStopH264() {
  if (msH264Timer) { clearInterval(msH264Timer); msH264Timer = null; }
  if (msH264Enc) { try { msH264Enc.close(); } catch (_) { } msH264Enc = null; }
  msH264Dims = '';
}

function msStartH264() {
  if (msH264Timer || typeof VideoEncoder === 'undefined' || typeof VideoFrame === 'undefined') return;
  const ch = msCh;
  let frameIdx = 0;
  msH264Timer = setInterval(() => {
    const v = $('#msVideo');
    if (!v.videoWidth || v.readyState < 2) return;
    // 宽高按偶数对齐，最大宽度 640
    const w = Math.min(v.videoWidth, 640) & ~1;
    const h = Math.round(v.videoHeight * w / v.videoWidth) & ~1;
    if (w < 16 || h < 16) return;
    const dims = `${w}x${h}`;
    if (!msH264Enc || msH264Enc.state === 'closed' || msH264Dims !== dims) {
      if (msH264Enc) { try { msH264Enc.close(); } catch (_) { } }
      msH264Dims = dims;
      frameIdx = 0;
      try {
        msH264Enc = new VideoEncoder({
          output: chunk => {
            const buf = new Uint8Array(chunk.byteLength);
            chunk.copyTo(buf);
            fetch(api('h264') + '?ch=' + ch + '&key=' + (chunk.type === 'key' ? 1 : 0), { method: 'POST', body: buf }).catch(() => { });
          },
          error: e => { console.warn('H264 编码异常:', e); msStopH264(); },
        });
        msH264Enc.configure({
          codec: 'avc1.42E01E',
          width: w,
          height: h,
          bitrate: 500000,
          framerate: 15,
          latencyMode: 'realtime',
          avc: { format: 'annexb' },
        });
      } catch (e) {
        console.warn('VideoEncoder 初始化失败，1078 推流回退彩条:', e);
        msStopH264();
        return;
      }
    }
    if (msH264Enc.state !== 'configured' || msH264Enc.encodeQueueSize > 3) return;
    try {
      const vf = new VideoFrame(v, { timestamp: Math.round(performance.now() * 1000) });
      msH264Enc.encode(vf, { keyFrame: frameIdx % 30 === 0 });
      vf.close();
      frameIdx++;
    } catch (_) { }
  }, 67);
}

// 抓帧：视频/摄像头画面 → JPEG → 后端该通道的帧历史（窗口隐藏时也继续，保证随时能截到当前画面）
function msStartCapture() {
  msStartH264();
  if (msTimer) return;
  msTimer = setInterval(async () => {
    const v = $('#msVideo');
    if (!v.videoWidth || v.readyState < 2) return;
    const w = Math.min(v.videoWidth, 640), h = Math.round(v.videoHeight * w / v.videoWidth);
    msCanvas.width = w; msCanvas.height = h;
    msCanvas.getContext('2d').drawImage(v, 0, 0, w, h);
    const blob = await new Promise(res => msCanvas.toBlob(res, 'image/jpeg', 0.85));
    if (!blob) return;
    try { await fetch(msUrl('frame'), { method: 'POST', body: blob }); } catch (_) { }
  }, 1000);
}

// 摄像头设备列表：拿到权限后 label 才可读；只有一路视频输入时不显示下拉
async function msListDevices() {
  const sel = $('#msCam'), wrap = $('#msCamWrap');
  if (!navigator.mediaDevices?.enumerateDevices) { wrap.hidden = true; return; }
  let list = [];
  try {
    list = (await navigator.mediaDevices.enumerateDevices()).filter(d => d.kind === 'videoinput');
  } catch (_) { }
  if (list.length < 2) { wrap.hidden = true; return; }
  sel.innerHTML = list.map((d, i) =>
    `<option value="${esc(d.deviceId)}">${esc(d.label || '摄像头 ' + (i + 1))}</option>`).join('');
  if (!msDevId || !list.some(d => d.deviceId === msDevId)) msDevId = list[0].deviceId;
  sel.value = msDevId;
  wrap.hidden = false;
}

async function msCamera(quiet) {
  if (!navigator.mediaDevices?.getUserMedia) {
    return quiet ? undefined : toast('当前环境不支持摄像头（需 localhost 或 HTTPS）');
  }
  try {
    const video = { width: { ideal: 1280 } };
    if (msDevId) video.deviceId = { exact: msDevId };
    const stream = await navigator.mediaDevices.getUserMedia({ video, audio: false });
    msStop();
    msStream = stream;
    const v = $('#msVideo');
    v.srcObject = stream;
    await v.play();
    const j = await fetchJSON(msUrl('mediasrc', 'kind=camera'), { method: 'POST' });
    if (!j.ok) { msStop(); return toast(j.error || '设置失败'); }
    await msListDevices();
    if (!quiet) toast(`${msChName()} 已开启摄像头，循环抓帧中`, 'ok');
    msStartCapture();
    msRecWant = true; msRecStart(); // 同时录最近约 6 秒做视频附件
    poll();
  } catch (e) {
    if (!quiet) toast('摄像头打开失败：' + e.message + '（请允许摄像头权限）');
    else console.warn('自动恢复摄像头失败：', e.message);
  }
}

// 程序重启后：配置里仍是「电脑摄像头」，但视频流必须重新获取（重启会让后端抓帧缓存清空）。
// 权限已被浏览器记住时可静默恢复；不支持或被拒则让页面显示「待开启」+ 开启按钮，而不是一个黑框。
async function msAutoCamera() {
  if (msStream || msCamTried.has(msCh) || !navigator.mediaDevices?.getUserMedia) return;
  msCamTried.add(msCh);
  try {
    if (navigator.permissions?.query) {
      const p = await navigator.permissions.query({ name: 'camera' }).catch(() => null);
      if (p && p.state === 'denied') return;
    }
    await msCamera(true);
  } catch (e) { console.warn('自动恢复摄像头失败：', e.message); }
}

// 预览该通道此刻会用于附件的画面（后端响应头标注来源）
async function msPreview() {
  try {
    const r = await fetch(msUrl('attach-preview'), { cache: 'no-store' });
    if (!r.ok) return toast('预览失败：HTTP ' + r.status);
    const src = r.headers.get('X-JT808-Source') || '';
    const blob = await r.blob();
    const img = $('#msPrevImg');
    if (img.dataset.url) URL.revokeObjectURL(img.dataset.url);
    img.dataset.url = URL.createObjectURL(blob);
    img.src = img.dataset.url;
    $('#msPrevWrap').hidden = false;
    $('#msPrevTag').textContent = `${msChName()} · ${MS_SRC[src] || src} · ${(blob.size / 1024).toFixed(0)} KB`;
  } catch (e) { toast('预览失败：' + e.message); }
}

// 预览该通道此刻会用于附件的视频：录制的最近一段 / 上传的源视频 / 内置 3 秒彩条片
async function msPreviewVideo() {
  try {
    const r = await fetch(msUrl('videoclip') + '&t=' + Date.now(), { cache: 'no-store' });
    if (!r.ok) return toast('预览失败：HTTP ' + r.status);
    const src = r.headers.get('X-JT808-Source') || '';
    const blob = await r.blob();
    const v = $('#msVid');
    if (v.dataset.url) URL.revokeObjectURL(v.dataset.url);
    v.dataset.url = URL.createObjectURL(blob);
    v.src = v.dataset.url;
    $('#msVidWrap').hidden = false;
    $('#msVidTag').textContent = `${msChName()} · ${MS_VSRC[src] || src} · ${(blob.size / 1024).toFixed(0)} KB · ${blob.type}`
      + (src === 'builtin' ? '（说明这个通道还没有可用录制，附件里的视频就是它）' : '');
  } catch (e) { toast('预览失败：' + e.message); }
}

// 切换通道：停掉当前预览/抓帧，重新按新通道的状态渲染
$('#msCh').onclick = e => {
  const ch = e.target.dataset.ch;
  if (ch === undefined || ch === msCh) return;
  msCh = ch;
  msStop();
  msUpTick++;
  $('#msPrevWrap').hidden = true;
  $('#msPrevTag').textContent = '';
  if (S) renderMs(S);
};

$('#msKind').onclick = async e => {
  const kind = e.target.dataset.ms;
  if (kind === undefined) return;
  const cur = ((S?.mediaSrc || {})[msCh] || {}).src || '';
  if (kind === cur && (kind !== 'camera' || msStream)) return; // 摄像头在刷新/切换终端后需重新开启
  if (kind === 'camera') { msCamera(); return; }
  if (kind === 'image' || kind === 'video') {
    msPending = kind;
    const f = $('#msFile');
    f.accept = kind === 'image' ? 'image/jpeg,image/png,image/gif' : 'video/mp4,.m4v';
    f.click();
    return;
  }
  // 恢复默认（只影响当前通道；全部通道时才是全局默认）
  msStop();
  try {
    const j = await fetchJSON(msUrl('mediasrc'), { method: 'DELETE' });
    if (j.ok) { toast(`${msChName()} 已恢复默认合成画面`, 'ok'); poll(); } else toast(j.error || '操作失败');
  } catch (err) { toast('操作失败：' + err.message); }
};

$('#msFile').onchange = async e => {
  const f = e.target.files[0];
  e.target.value = '';
  if (!f || !msPending) return;
  if (f.size > 64 << 20) return toast('文件过大（最大 64 MB）');
  const ext = (f.name.match(/\.[^.]+$/)?.[0] || '').toLowerCase();
  const kind = msPending;
  try {
    const j = await fetchJSON(msUrl('mediasrc', 'kind=' + kind + '&ext=' + encodeURIComponent(ext)), { method: 'POST', body: f });
    if (!j.ok) return toast(j.error || '上传失败');
    msStop();
    msUpTick = Date.now();
    toast(`${msChName()} 已设置为${MS_NAME[kind]}「${f.name}」`, 'ok');
    poll();
  } catch (err) { toast('上传失败：' + err.message); }
};

$('#msCam').onchange = e => { msDevId = e.target.value; msCamera(); };
$('#msPrev').onclick = msPreview;
$('#msVidPrev').onclick = msPreviewVideo;
$('#msCamOn').onclick = () => msCamera();
// 时间水印开关（服务端在生成附件图片/预览时烧入，改完立即生效）
$('#msStamp').onchange = async e => {
  const on = e.target.checked;
  try {
    const j = await fetchJSON(api('mediasrc/stamp') + '?on=' + (on ? 1 : 0), { method: 'POST' });
    if (!j.ok) return toast(j.error || '设置失败');
    toast(on ? '附件画面已烧入时间水印' : '已关闭时间水印', 'ok');
    poll();
  } catch (err) { toast('设置失败：' + err.message); }
};

function renderMs(s) {
  const ms = (s.mediaSrc || {})[msCh] || { src: '', own: true, frameAge: -1, frameSize: 0 };
  const src = ms.src || '';
  const inherited = msCh !== '0' && !ms.own && !!src; // 该通道没有单独设置，沿用「全部通道」
  const camIdle = src === 'camera' && !msStream;      // 配置是摄像头但流还没拿到（程序重启 / 权限被拒）
  $$('#msCh button').forEach(b => b.classList.toggle('on', b.dataset.ch === msCh));
  $$('#msKind button').forEach(b => b.classList.toggle('on', b.dataset.ms === src));
  $('#msTag').textContent = camIdle ? '电脑摄像头（待开启）' : (MS_NAME[src] || src) + (inherited ? '（继承全部）' : '');
  $('#msTag').className = 'tag ' + (src && !camIdle ? 'ok' : '');
  $('#msPreviewWrap').hidden = !src;
  $('#msVideo').hidden = src !== 'video' && !(src === 'camera' && msStream);
  $('#msImg').hidden = src !== 'image';
  $('#msCamOff').hidden = !camIdle;
  if (document.activeElement !== $('#msStamp')) $('#msStamp').checked = s.mediaStamp !== false;
  if (camIdle) msAutoCamera();

  // 加载/刷新预览（签名未变不动它，避免打断播放）
  const sig = TID + ':' + msCh + ':' + src + ':' + msUpTick;
  if (src && sig !== msLoaded) {
    msLoaded = sig;
    if (src === 'image') $('#msImg').src = msUrl('mediasrc') + '&t=' + msUpTick;
    if (src === 'video') {
      const v = $('#msVideo');
      v.src = msUrl('mediasrc') + '&t=' + msUpTick;
      v.play().catch(() => { });
      msStartCapture();
      msRecWant = true; msRecStart();
    }
  }
  if (src === 'camera' && msStream) {
    $('#msCamWrap').hidden = $('#msCam').options.length < 2;
    msStartCapture();
    msRecWant = true; msRecStart();
  }

  const stampTip = s.mediaStamp === false ? '' : '画面左下角烧入时间与通道号。';
  const frameText = ms.frameAge >= 0 ? `最近一帧 ${ms.frameAge} 秒前（${(ms.frameSize / 1024).toFixed(0)} KB）` : '';
  const clipText = ms.clipAge >= 0
    ? `最近一段录制 ${ms.clipAge} 秒前（${(ms.clipSize / 1024).toFixed(0)} KB${ms.clipMime ? '，' + ms.clipMime.split(';')[0] : ''}）`
    : '后端还没有收到录制片段';
  const recNote = msRecDead ? (msRecErr || '录制不可用') + '，视频附件用内置 alarm.mp4'
    : (typeof MediaRecorder === 'undefined') ? '当前浏览器不支持 MediaRecorder，视频附件用内置 alarm.mp4'
      : (msRecMime ? `视频附件取最近约 6 秒录制（${msRecMime.includes('mp4') ? 'MP4' : 'WebM，文件按 .webm 上传'}）`
        : '视频附件取最近约 6 秒录制（正在试可用格式：MP4 优先，不行自动退 WebM）');
  // 抓帧靠浏览器定时器，窗口最小化 / 标签页在后台会被节流；此时附件仍沿用最后一帧，不退回合成图
  const staleTip = ms.stale ? `前端抓帧已暂停 ${ms.frameAge} 秒（窗口最小化或标签页在后台会被节流），附件仍沿用这一帧。` : '';
  const inhTip = inherited ? '当前继承「全部通道」设置，在「全部通道」或本通道重新选择即可覆盖。' : '';
  let body;
  if (src === '') {
    body = '合成彩条测试图 + 内置 alarm.mp4（3 秒彩条：附件里看到它就说明这个通道没设真实媒体源）。'
      + '选择图片/视频文件或摄像头后，图片附件截取当前画面（多张按 2 秒间隔取不同时刻），视频附件取最近约 6 秒录制。';
  } else if (src === 'image') {
    body = '图片附件用上传的这张图片（多张时按 2 秒间隔错开时间水印）；图片源没有播放画面，视频附件仍用内置 alarm.mp4。';
  } else if (src === 'camera') {
    body = camIdle
      ? '摄像头未开启：程序重启后浏览器不会自动恢复视频流（也可能权限被拒），此时附件图片会用合成彩条图、视频用内置 alarm.mp4。点上面的「电脑摄像头」或下面的「开启摄像头」恢复。'
      : (frameText ? `摄像头循环抓帧，${frameText}。` : '等待摄像头画面…') + staleTip
        + `图片附件取当前画面（多张按 2 秒间隔取不同时刻）；${recNote}；${clipText}。`;
  } else {
    body = (frameText ? `视频循环播放，${frameText}。` : '视频加载中…') + staleTip
      + `图片附件截取当前播放画面（多张按 2 秒间隔取不同时刻）；${recNote}；${clipText}。`;
  }
  $('#msHint').textContent = msChName() + '：' + body + stampTip + inhTip;
}

onState(s => page === 'media' && renderMs(s));
onPage('media', () => S && renderMs(S));
onSwitch(() => { msStop(); msUpTick = 0; msCamTried.clear(); });
