// PC Remote — phone-first viewer/controller.
//
// Two pointing models. DIRECT (default) matches the instinct: tap where you
// want to click, pinch-zoom first when the target is small. TOUCHPAD glides a
// visible cursor with your finger (like a laptop trackpad) and clicks where the
// cursor sits — better when the fingertip would cover the target.

const stage = document.getElementById('stage');
const videoEl = document.getElementById('screenv');
const hidden = document.getElementById('hidden');
const msg = document.getElementById('msg');
const cursorEl = document.getElementById('cursor');

let scale = 1, offX = 0, offY = 0;   // view transform
let natW = 0, natH = 0;              // video size in pixels
let curX = 0.5, curY = 0.5;          // virtual cursor, normalized 0..1
let mode = localStorage.getItem('pcr_mode') || 'direct'; // 'direct' | 'pad'
let dragging = false;                // left button held (drag mode)
const mods = { ctrl: false, alt: false, shift: false, meta: false };
let rightMode = false;

const TAP_MS = 600;      // press shorter than this (and still) counts as a tap
const TAP_SLOP = 10;     // px of movement still considered a tap
const PAD_SPEED = 1.6;   // finger travel -> cursor travel multiplier

function toast(t) {
  msg.textContent = t;
  msg.style.display = 'block';
  clearTimeout(toast._t);
  toast._t = setTimeout(() => (msg.style.display = 'none'), 1600);
}

function applyTransform() {
  videoEl.style.transform = `translate(${offX}px, ${offY}px) scale(${scale})`;
  drawCursor();
}

// 'fit' shows the whole desktop. Portrait phones are width-limited against a
// 16:10 desktop, so fitting is all that makes sense automatically; reading text
// there is what the 2x zoom button is for.
let fitMode = 'fit';
let userZoomed = false;   // manual pinch: don't refit behind the user's back

function fitToStage(mode) {
  if (!natW || !natH) return;
  if (mode) fitMode = mode;
  userZoomed = false;
  const sw = stage.clientWidth, sh = stage.clientHeight;
  scale = fitMode === 'width' ? sw / natW : Math.min(sw / natW, sh / natH);
  offX = (sw - natW * scale) / 2;
  // Keep the cursor's row in view when the picture is taller than the screen.
  const h = natH * scale;
  offY = h <= sh ? (sh - h) / 2 : clampOff(sh / 2 - curY * h, sh, h);
  applyTransform();
}

function clampOff(v, viewport, content) {
  return Math.min(0, Math.max(viewport - content, v));
}

function autoFit() { fitToStage('fit'); }

// Zoom around the cursor (or the middle) — the fastest way to make text
// readable on a phone without pinching repeatedly.
function zoomAround(factor) {
  const sw = stage.clientWidth, sh = stage.clientHeight;
  const fx = curX, fy = curY;                 // keep this point where it is
  const px = offX + fx * natW * scale, py = offY + fy * natH * scale;
  scale = Math.min(6, Math.max(0.05, scale * factor));
  userZoomed = true;
  offX = px - fx * natW * scale;
  offY = py - fy * natH * scale;
  // Nudge back inside if the zoom pushed the picture off-screen.
  const w = natW * scale, h = natH * scale;
  offX = w <= sw ? (sw - w) / 2 : clampOff(offX, sw, w);
  offY = h <= sh ? (sh - h) / 2 : clampOff(offY, sh, h);
  applyTransform();
}

// Virtual cursor position -> on-screen pixels, so the marker tracks pan/zoom.
function drawCursor() {
  if (!natW) return;
  cursorEl.style.left = `${offX + curX * natW * scale}px`;
  cursorEl.style.top = `${offY + curY * natH * scale}px`;
  cursorEl.style.display = mode === 'pad' ? 'block' : 'none';
}

function clamp01(v) { return Math.min(1, Math.max(0, v)); }

function normFromClient(cx, cy) {
  const r = stage.getBoundingClientRect();
  return {
    x: clamp01((cx - r.left - offX) / (natW * scale)),
    y: clamp01((cy - r.top - offY) / (natH * scale)),
  };
}

// ---- input transport ----
// One open socket, not a request per event. A POST per mouse move costs a full
// round trip through the tunnel; a finger drag emits ~60 of them a second and
// they pile up, which is why the cursor used to lag seconds behind. Moves are
// also coalesced: only the newest position is worth sending, so a backlog is
// dropped rather than replayed.
let ws = null, wsReady = false, wsTries = 0;
let wsBlocked = false;          // give up on sockets, use POST pacing instead
const WS_GIVE_UP_AFTER = 3;

function wsURL() {
  const proto = location.protocol === 'https:' ? 'wss://' : 'ws://';
  return proto + location.host + '/ws';
}

function connectWS() {
  if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) return;
  try {
    ws = new WebSocket(wsURL());
  } catch { return; }
  ws.onopen = () => {
    wsReady = true;
    wsTries = 0;
    // Warm the path: an idle tunnel leg answers the first event ~500ms late
    // (cold congestion window), versus ~80ms once traffic is flowing. A couple
    // of pings up front means the first real gesture isn't the slow one.
    sendNow({ type: 'ping' });
    setTimeout(() => wsReady && sendNow({ type: 'ping' }), 150);
  };
  ws.onclose = () => {
    wsReady = false;
    if (++wsTries >= WS_GIVE_UP_AFTER && !wsBlocked) {
      wsBlocked = true;
      toast('Network keeps blocking the live link — switching to fallback mode');
      setTimeout(() => { wsBlocked = false; wsTries = 0; connectWS(); }, 60000);
      return;                        // stop hammering; POST carries input now
    }
    if (!wsBlocked) setTimeout(connectWS, Math.min(4000, 400 * wsTries));
  };
  ws.onerror = () => { try { ws.close(); } catch {} };
}
connectWS();
document.addEventListener('visibilitychange', () => { if (!document.hidden) connectWS(); });
// Idle NAT/proxy paths drop silent sockets; a tiny ping keeps ours alive.
setInterval(() => { if (wsReady && !document.hidden) sendNow({ type: 'ping' }); }, 5000);

function sendNow(ev) {
  const payload = JSON.stringify({ ...ev, ...mods });
  if (wsReady) {
    try { ws.send(payload); return; } catch { wsReady = false; }
  }
  // Fallback for the moments the socket is down (or a proxy blocks WS).
  fetch('input', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: payload,
    keepalive: true,
  }).then(res => {
    if (res.status === 401) location.href = '/';
  }).catch(() => {});
}

// Flow control. Coalescing alone wasn't enough: one move per animation frame
// is still ~60 messages a second, and a scroll gesture emitted one message per
// notch with no coalescing at all. Over a tunnel that accepts them faster than
// it delivers them, the socket's own buffer becomes the queue and every gesture
// arrives late — the backlog, not the network, is the latency.
//
// So: cap the rate, merge what's mergeable, and above all refuse to enqueue
// while the socket is still draining. A dropped intermediate position costs
// nothing (a newer one supersedes it); a queued one costs delay on everything
// behind it.
const MOVE_MIN_MS = 30;        // ~33 updates/s is past the point of visibility
const MOVE_MIN_MS_POST = 120;  // POST fallback: bursts get rate-limited
function moveInterval() { return wsReady ? MOVE_MIN_MS : MOVE_MIN_MS_POST; }
const BUFFER_LIMIT = 8 * 1024; // socket still draining => skip this update

let pendingMove = null, pendingScroll = 0, flushTimer = null, lastFlushAt = 0;

const BUSY_MAX_MS = 1500;   // beyond this the socket isn't draining, it's dead
let busySince = 0;

function socketBusy() {
  const busy = ws && ws.readyState === WebSocket.OPEN && ws.bufferedAmount > BUFFER_LIMIT;
  if (!busy) {
    busySince = 0;
    return false;
  }
  if (!busySince) busySince = Date.now();
  if (Date.now() - busySince > BUSY_MAX_MS) {
    // Not congestion — a stuck connection. Drop it so the reconnect (or the
    // POST fallback) can take over instead of queueing behind a corpse.
    busySince = 0;
    wsReady = false;
    try { ws.close(); } catch {}
    return false;
  }
  return true;
}

let pendingSince = 0;

function flushPending() {
  flushTimer = null;
  // Hard ceiling on staleness: if a move has waited this long, send it however
  // we can rather than keep deferring.
  const stale = pendingSince && Date.now() - pendingSince > 800;
  if (!stale && socketBusy()) {       // still draining: try again shortly
    scheduleFlush(moveInterval());
    return;
  }
  pendingSince = 0;
  lastFlushAt = Date.now();
  if (pendingMove) {
    const m = pendingMove;
    pendingMove = null;
    sendNow(m);
  }
  if (pendingScroll) {
    const d = pendingScroll;
    pendingScroll = 0;
    sendNow({ type: 'scroll', x: curX, y: curY, delta: d });
  }
}

function scheduleFlush(delay) {
  if (flushTimer) return;
  flushTimer = setTimeout(flushPending, delay);
}

function send(ev) {
  // Continuous, superseding events: keep only the newest (moves) or the sum
  // (scroll), and let the pacer decide when it goes out.
  if (ev.type === 'move' || ev.type === 'scroll') {
    if (ev.type === 'move') pendingMove = ev;
    else pendingScroll += ev.delta || 0;
    if (!pendingSince) pendingSince = Date.now();

    const iv = moveInterval();
    const since = Date.now() - lastFlushAt;
    if (since >= iv && !flushTimer && !socketBusy()) flushPending();
    else scheduleFlush(Math.max(0, iv - since));
    return;
  }

  // Discrete events (clicks, keys) are never dropped, and must land after the
  // movement that positioned them.
  if (pendingMove || pendingScroll) {
    if (flushTimer) { clearTimeout(flushTimer); flushTimer = null; }
    flushPending();
  }
  sendNow(ev);
}

// Move the virtual cursor by a finger delta (touchpad feel) and mirror it to
// the PC. Sending absolute normalized coords keeps the server stateless.
function nudgeCursor(dxPx, dyPx) {
  if (!natW) return;
  curX = clamp01(curX + (dxPx * PAD_SPEED) / (natW * scale));
  curY = clamp01(curY + (dyPx * PAD_SPEED) / (natH * scale));
  drawCursor();
  drawLoupe();
  send({ type: 'move', x: curX, y: curY });
}

// Move the view, keeping the picture from drifting off-screen.
function panBy(dx, dy) {
  const sw = stage.clientWidth, sh = stage.clientHeight;
  const w = natW * scale, h = natH * scale;
  offX = w <= sw ? (sw - w) / 2 : clampOff(offX + dx, sw, w);
  offY = h <= sh ? (sh - h) / 2 : clampOff(offY + dy, sh, h);
  applyTransform();
}

function setCursorAt(nx, ny) {
  curX = nx; curY = ny;
  drawCursor();
}

function flashCursor() {
  cursorEl.style.display = 'block';
  cursorEl.classList.add('hit');
  setTimeout(() => {
    cursorEl.classList.remove('hit');
    drawCursor();
  }, 220);
}

function clickAtCursor(button) {
  flashCursor();
  navigator.vibrate?.(15);
  send({ type: 'click', x: curX, y: curY, button: button || (rightMode ? 'right' : 'left') });
  if (rightMode) setRightMode(false);
  clearMods();
}

// ---- stream ----
// The picture arrives as H.264 over a WebSocket and plays through MediaSource.
// A dead socket is silent — the phone sleeping, switching apps or hopping
// wifi<->4G kills it without an error — so treat the arriving frames as a
// heartbeat and reconnect when they stop.
let live = false;
let lastFrameAt = 0;
// No stale verdict until this time: a fresh connection has no frame yet, and
// the PC may be waking its display before it can capture one. Without the
// grace the watchdog fired 2.5s after every connect — exactly while the server
// was waiting for the panel — and reconnected into the same wait, forever.
let graceUntil = 0;

const STALE_MS = 6000;
const CONNECT_GRACE_MS = 10000;
function streamStale() {
  return Date.now() >= graceUntil && Date.now() - lastFrameAt > STALE_MS;
}
setInterval(() => {
  if (document.hidden) return;
  if (streamStale()) {
    setStatus('offline');
    h264Start();
  }
}, 2500);

// Adopt the video's real size the moment it's known. A <video> doesn't size
// itself from its content, so the element needs the numbers explicitly.
function adoptMediaSize() {
  const w = videoEl.videoWidth, h = videoEl.videoHeight;
  if (!w || !h) return false;
  if (natW !== w || natH !== h) {
    natW = w; natH = h;
    videoEl.style.width = w + 'px';
    videoEl.style.height = h + 'px';
    autoFit();
  }
  return true;
}

let sizeWaitTicks = 0;
const sizeWatch = setInterval(() => {
  if (document.hidden) return;
  if (adoptMediaSize()) { sizeWaitTicks = 0; return; }
  if (++sizeWaitTicks === 20) toast('No picture yet — try reloading the page');
}, 400);

function reconnectIfStale() {
  if (document.hidden) return;
  if (streamStale()) h264Start();
}
document.addEventListener('visibilitychange', () => { if (!document.hidden) reconnectIfStale(); });
window.addEventListener('pageshow', reconnectIfStale);   // back from bfcache
window.addEventListener('focus', reconnectIfStale);
window.addEventListener('online', reconnectIfStale);

function setStatus(state) {
  const dot = document.getElementById('dot');
  if (dot) dot.className = state;
}

// ---- touch ----
let t0 = null;          // {x,y,at} of the first finger
let last = null;        // last position for delta tracking
let moved = false;
let panBase = null;     // view offset when a two-finger gesture started
let startDist = 0, startScale = 1, twoFinger = false, pinching = false;
let longPressTimer = null;
let lastTouchAt = 0;   // suppress the synthetic click a tap generates

stage.addEventListener('touchstart', (e) => {
  if (e.touches.length === 1) {
    const t = e.touches[0];
    t0 = { x: t.clientX, y: t.clientY, at: Date.now() };
    last = { x: t.clientX, y: t.clientY };
    moved = false;
    twoFinger = false;
    // Aim assist: in direct mode the cursor jumps to the fingertip, so the
    // loupe shows what the finger is hiding.
    if (mode === 'direct') {
      const p0 = normFromClient(t.clientX, t.clientY);
      setCursorAt(p0.x, p0.y);
    }
    showLoupe();
    // Long press = right click (pad mode) or press-and-hold drag start.
    longPressTimer = setTimeout(() => {
      if (!moved) {
        if (mode === 'direct') {
          const p = normFromClient(t0.x, t0.y);
          setCursorAt(p.x, p.y);
        }
        send({ type: 'click', x: curX, y: curY, button: 'right' });
        navigator.vibrate?.(30);
        toast('Right click');
      }
    }, 700);
  } else if (e.touches.length === 2) {
    clearTimeout(longPressTimer);
    twoFinger = true;
    pinching = false;
    moved = true;
    startDist = dist(e.touches[0], e.touches[1]);
    startScale = scale;
    panBase = { x: offX, y: offY, mid: midpoint(e.touches[0], e.touches[1]) };
  }
}, { passive: true });

stage.addEventListener('touchmove', (e) => {
  if (e.touches.length === 1 && t0 && !twoFinger) {
    const t = e.touches[0];
    const dx = t.clientX - last.x, dy = t.clientY - last.y;
    if (Math.abs(t.clientX - t0.x) > TAP_SLOP || Math.abs(t.clientY - t0.y) > TAP_SLOP) {
      moved = true;
      clearTimeout(longPressTimer);
    }
    if (moved) {
      if (mode === 'pad') {
        nudgeCursor(dx, dy);           // trackpad: finger glides the cursor
      } else {
        panBy(dx, dy);                 // direct: finger drags the picture
        const pm = normFromClient(t.clientX, t.clientY);
        setCursorAt(pm.x, pm.y);
      }
      drawLoupe();
    }
    last = { x: t.clientX, y: t.clientY };
  } else if (e.touches.length === 2 && panBase) {
    const d = dist(e.touches[0], e.touches[1]);
    const mid = midpoint(e.touches[0], e.touches[1]);
    // Distance changing => pinch zoom; fingers moving together => scroll/pan.
    if (!pinching && Math.abs(d - startDist) > 25) pinching = true;
    if (pinching) {
      const before = normFromClient(mid.x, mid.y);
      scale = Math.min(6, Math.max(0.1, startScale * (d / startDist)));
      userZoomed = true;
      const r = stage.getBoundingClientRect();
      offX = mid.x - r.left - before.x * natW * scale;
      offY = mid.y - r.top - before.y * natH * scale;
      applyTransform();
    } else {
      const dy = mid.y - panBase.mid.y;
      if (Math.abs(dy) > 18) {
        // Delta proportional to the swipe: distance, not repetition, decides
        // how far the page moves. Coalescing sums these into one message.
        const notches = Math.max(1, Math.round(Math.abs(dy) / 18));
        send({ type: 'scroll', x: curX, y: curY, delta: (dy > 0 ? 120 : -120) * notches });
        panBase.mid = mid;
      }
    }
  }
}, { passive: true });

stage.addEventListener('touchend', (e) => {
  lastTouchAt = Date.now();
  clearTimeout(longPressTimer);
  if (e.touches.length > 0) return; // still mid-gesture
  const wasTap = t0 && !moved && Date.now() - t0.at < TAP_MS && !twoFinger;
  if (wasTap) {
    if (mode === 'direct') {
      const p = normFromClient(t0.x, t0.y);
      setCursorAt(p.x, p.y);
    }
    clickAtCursor();
  }
  t0 = null; last = null; twoFinger = false; pinching = false; panBase = null;
  hideLoupeSoon();
}, { passive: true });

function dist(a, b) { return Math.hypot(a.clientX - b.clientX, a.clientY - b.clientY); }
function midpoint(a, b) { return { x: (a.clientX + b.clientX) / 2, y: (a.clientY + b.clientY) / 2 }; }

// ---- mouse (desktop browser) ----
stage.addEventListener('click', (e) => {
  if (Date.now() - lastTouchAt < 800) return; // synthetic click from a tap
  const p = normFromClient(e.clientX, e.clientY);
  setCursorAt(p.x, p.y);
  clickAtCursor();
});
stage.addEventListener('contextmenu', (e) => {
  e.preventDefault();
  const p = normFromClient(e.clientX, e.clientY);
  setCursorAt(p.x, p.y);
  send({ type: 'click', x: p.x, y: p.y, button: 'right' });
});
stage.addEventListener('dblclick', (e) => {
  const p = normFromClient(e.clientX, e.clientY);
  send({ type: 'dblclick', x: p.x, y: p.y, button: 'left' });
});
stage.addEventListener('wheel', (e) => {
  e.preventDefault();
  const p = normFromClient(e.clientX, e.clientY);
  send({ type: 'scroll', x: p.x, y: p.y, delta: e.deltaY > 0 ? -120 : 120 });
}, { passive: false });

// ---- keyboard ----
hidden.addEventListener('focus', () => {
  typingMode = true;
  clearTimeout(loupeHideTimer);
  showLoupe();
});
hidden.addEventListener('blur', () => {
  typingMode = false;
  hideLoupeSoon(400);
});

hidden.addEventListener('input', () => {
  const v = hidden.textContent || '';
  if (v) {
    send({ type: 'text', text: v });
    hidden.textContent = '';
  }
});
hidden.addEventListener('keydown', (e) => {
  const named = ['Enter', 'Backspace', 'Tab', 'Escape', 'ArrowUp', 'ArrowDown', 'ArrowLeft',
                 'ArrowRight', 'Home', 'End', 'PageUp', 'PageDown', 'Delete'];
  if (named.includes(e.key)) { e.preventDefault(); send({ type: 'key', key: e.key }); }
});
document.addEventListener('keydown', (e) => {
  if (document.activeElement === hidden) return;
  const named = ['Enter','Backspace','Tab','Escape','ArrowUp','ArrowDown','ArrowLeft','ArrowRight','Delete'];
  if (e.key.length === 1 || named.includes(e.key)) {
    e.preventDefault();
    send(e.key.length === 1
      ? { type: 'text', text: e.key, ctrl: e.ctrlKey, alt: e.altKey, shift: e.shiftKey, meta: e.metaKey }
      : { type: 'key', key: e.key, ctrl: e.ctrlKey, alt: e.altKey, shift: e.shiftKey, meta: e.metaKey });
  }
});

// ---- toolbar ----
const btnMode = document.getElementById('mode');
function setMode(m) {
  mode = m;
  localStorage.setItem('pcr_mode', m);
  btnMode.textContent = m === 'pad' ? '🖱 Touchpad' : '👆 Direct';
  drawCursor();
  toast(m === 'pad' ? 'Touchpad: drag to move the cursor, tap to click' : 'Direct: tap where you want to click');
}
btnMode.addEventListener('click', () => setMode(mode === 'pad' ? 'direct' : 'pad'));

const btnDrag = document.getElementById('drag');
btnDrag.addEventListener('click', () => {
  dragging = !dragging;
  btnDrag.classList.toggle('on', dragging);
  send({ type: dragging ? 'down' : 'up', x: curX, y: curY, button: 'left' });
  toast(dragging ? 'Holding left button — move, then tap again to release' : 'Button released');
});

document.getElementById('lclick').addEventListener('click', () => clickAtCursor('left'));
document.getElementById('dbl').addEventListener('click', () => {
  send({ type: 'dblclick', x: curX, y: curY, button: 'left' });
});
document.getElementById('kbd').addEventListener('click', () => hidden.focus());
document.getElementById('fit').addEventListener('click', () => fitToStage('fit'));
document.getElementById('fitw').addEventListener('click', () => {
  // Toggle between a readable zoom and the whole screen.
  const fitScale = Math.min(stage.clientWidth / natW, stage.clientHeight / natH);
  if (scale > fitScale * 1.4) fitToStage('fit');
  else zoomAround(2.2);
});

// Hide the toolbar to reclaim the screen (especially in landscape).
const barToggle = document.getElementById('barToggle');
function setBar(on) {
  document.body.classList.toggle('bar-off', !on);
  barToggle.textContent = on ? '▾' : '▴';
  localStorage.setItem('pcr_bar', on ? '1' : '0');
  setTimeout(() => { lastGeom = null; onViewportChange(); }, 60);
}
barToggle.addEventListener('click', () => setBar(document.body.classList.contains('bar-off')));
setBar(localStorage.getItem('pcr_bar') !== '0');

function setRightMode(on) {
  rightMode = on;
  document.getElementById('mRight').classList.toggle('on', on);
}
document.getElementById('mRight').addEventListener('click', () => {
  setRightMode(!rightMode);
  if (rightMode) toast('Next tap is a right click');
});

document.querySelectorAll('[data-mod]').forEach((b) => {
  b.addEventListener('click', () => {
    const k = b.dataset.mod;
    mods[k] = !mods[k];
    b.classList.toggle('on', mods[k]);
  });
});
document.querySelectorAll('[data-key]').forEach((b) => {
  b.addEventListener('click', () => { send({ type: 'key', key: b.dataset.key }); clearMods(); });
});
function clearMods() {
  Object.keys(mods).forEach((k) => (mods[k] = false));
  document.querySelectorAll('[data-mod]').forEach((b) => b.classList.remove('on'));
}

// The on-screen keyboard resizes the viewport too; refitting then would yank
// the picture out from under the user mid-typing. Only react to real geometry
// changes (rotation, window resize), and keep the point you were looking at.
let lastGeom = null;
function onViewportChange() {
  const w = stage.clientWidth, h = stage.clientHeight;
  const keyboardOnly = lastGeom && lastGeom.w === w && h < lastGeom.h;
  if (keyboardOnly) return;
  const rotated = lastGeom && (lastGeom.w > lastGeom.h) !== (w > h);
  lastGeom = { w, h };
  if (rotated || !userZoomed) autoFit();
  else applyTransform();
}
window.addEventListener('resize', () => setTimeout(() => {
  onViewportChange();
  if (typingMode) showLoupe();   // keyboard opened/closed: re-place the loupe
}, 60));
window.addEventListener('orientationchange', () => setTimeout(() => {
  lastGeom = null;          // force a refit for the new orientation
  onViewportChange();
  toast(stage.clientWidth > stage.clientHeight ? 'Landscape — full screen' : 'Portrait — fit to width');
}, 250));
fetch('screen').then((r) => r.json()).then((s) => {
  toast(`${s.width}×${s.height} · display ${s.monitor + 1}/${s.monitors}`);
}).catch(() => {});
setMode(mode);
window.addEventListener('load', () => setTimeout(autoFit, 300));


// ---- settings sheet ----
// Resolution/rate/quality are a bandwidth trade the user makes on the spot:
// wifi at home wants sharp, mobile data wants cheap. The server applies changes
// to the running stream, so nothing here reconnects.
let settings = null;

function markSelected() {
  if (!settings) return;
  document.querySelectorAll('#optSize .opt').forEach(b =>
    b.classList.toggle('on', +b.dataset.w === settings.max_width));
  document.querySelectorAll('#optFps .opt').forEach(b =>
    b.classList.toggle('on', +b.dataset.fps === settings.fps));
  document.querySelectorAll('#optQ .opt').forEach(b =>
    b.classList.toggle('on', +b.dataset.q === settings.quality));
  document.querySelectorAll('#optMonitor .opt').forEach(b =>
    b.classList.toggle('on', +b.dataset.mon === settings.monitor));

  // The server reports the bitrate it actually gives the encoder, so quote that
  // rather than guessing from pixel counts.
  const w = settings.h264?.width || settings.max_width || settings.width;
  const h = settings.h264?.height || Math.round(w * settings.height / settings.width);
  const mbps = (settings.h264?.kbps || 0) / 1000;
  document.getElementById('est').textContent =
    `≈ ${w}x${h} · up to ${mbps.toFixed(1)} Mbps · ~${Math.round(mbps * 450 / 8)} MB/hour`;
}

async function loadSettings() {
  try {
    settings = await (await fetch('settings')).json();
    const row = document.getElementById('rowMonitor');
    if (settings.monitors > 1) {
      row.style.display = '';
      document.getElementById('optMonitor').innerHTML =
        Array.from({ length: settings.monitors }, (_, i) =>
          `<button class="opt" data-mon="${i}">Display ${i + 1}</button>`).join('');
      document.querySelectorAll('#optMonitor .opt').forEach(b =>
        b.addEventListener('click', () => patchSettings({ monitor: +b.dataset.mon }, true)));
    }
    markSelected();
  } catch { /* offline; the sheet just shows stale values */ }
}

// refit=true for changes that alter the image geometry (size, monitor): the
// stream restarts so the <video> picks up the new dimensions.
async function patchSettings(patch, refit) {
  try {
    settings = await (await fetch('settings', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(patch),
    })).json();
    markSelected();
    if (refit) { natW = 0; natH = 0; h264Start(); }
    toast('Applied');
  } catch { toast('Could not change settings'); }
}

document.querySelectorAll('#optSize .opt').forEach(b =>
  b.addEventListener('click', () => patchSettings({ max_width: +b.dataset.w }, true)));
document.querySelectorAll('#optFps .opt').forEach(b =>
  b.addEventListener('click', () => patchSettings({ fps: +b.dataset.fps }, false)));
document.querySelectorAll('#optQ .opt').forEach(b =>
  b.addEventListener('click', () => patchSettings({ quality: +b.dataset.q }, false)));

const PRESETS = {
  data:     { max_width: 854,  fps: 3,  quality: 35 },
  balanced: { max_width: 1280, fps: 10, quality: 60 },
  sharp:    { max_width: 1920, fps: 15, quality: 85 },
};
document.querySelectorAll('[data-preset]').forEach(b =>
  b.addEventListener('click', () => patchSettings(PRESETS[b.dataset.preset], true)));

function setSheet(on) { document.body.classList.toggle('sheet-on', on); if (on) loadSettings(); }
document.getElementById('gear').addEventListener('click', () => setSheet(true));
document.getElementById('sheetClose').addEventListener('click', () => setSheet(false));
document.getElementById('shade').addEventListener('click', () => setSheet(false));

loadSettings().finally(() => h264Start());

// ---- loupe ----
// A 16:10 desktop never fills a phone screen: portrait leaves a tall band above
// and below, landscape leaves bars either side. That dead space is exactly what
// a magnifier needs, and a magnifier is exactly what touch pointing lacks —
// your fingertip covers the thing you are aiming at. So: park a zoomed view of
// the area around the cursor in the empty band, with a crosshair marking the
// precise hit point.
const loupeEl = document.getElementById('loupe');
const lctx = loupeEl.getContext('2d');
const LOUPE_ZOOM = 3;              // magnification relative to the fitted view
let loupeOn = localStorage.getItem('pcr_loupe') !== '0';
let loupeHideTimer = null;

// Largest empty band around the picture, in stage coordinates.
function letterbox() {
  const sw = stage.clientWidth, sh = stage.clientHeight;
  const w = natW * scale, h = natH * scale;
  const bands = [
    { side: 'top',    x: 0,          y: 0,          w: sw,           h: Math.max(0, offY) },
    { side: 'bottom', x: 0,          y: offY + h,   w: sw,           h: Math.max(0, sh - (offY + h)) },
    { side: 'left',   x: 0,          y: 0,          w: Math.max(0, offX), h: sh },
    { side: 'right',  x: offX + w,   y: 0,          w: Math.max(0, sw - (offX + w)), h: sh },
  ];
  return bands.sort((a, b) => b.w * b.h - a.w * a.h)[0];
}

let typingMode = false;

function positionLoupe() {
  const band = letterbox();
  // Too little room to be useful — better to skip than to cover the picture.
  if (band.w < 90 || band.h < 70) {
    // Exception: while typing there may be no letterbox left (the keyboard took
    // it), and seeing the caret matters more than an unobstructed view.
    return typingMode ? floatLoupe() : false;
  }
  const pad = 8;
  const cw = Math.min(240, band.w - pad * 2);
  const ch = Math.min(170, band.h - pad * 2);
  if (cw < 80 || ch < 60) return false;

  loupeEl.width = Math.round(cw);          // backing store == CSS size: crisp
  loupeEl.height = Math.round(ch);
  loupeEl.style.width = `${Math.round(cw)}px`;
  loupeEl.style.height = `${Math.round(ch)}px`;
  const r = stage.getBoundingClientRect();
  loupeEl.style.left = `${r.left + band.x + (band.w - cw) / 2}px`;
  loupeEl.style.top = `${r.top + band.y + (band.h - ch) / 2}px`;
  return true;
}

// Fallback placement when no empty band is available: a small panel pinned to
// a corner, kept away from the half of the picture the cursor is in.
function floatLoupe() {
  const sw = stage.clientWidth, sh = stage.clientHeight;
  const cw = Math.min(200, Math.round(sw * 0.5));
  const ch = Math.round(cw * 0.7);
  if (cw < 80 || ch < 56 || sh < ch + 20) return false;
  loupeEl.width = cw; loupeEl.height = ch;
  loupeEl.style.width = cw + 'px';
  loupeEl.style.height = ch + 'px';
  const r = stage.getBoundingClientRect();
  // Cursor high on screen -> park the loupe low, and vice versa.
  const top = curY < 0.5 ? r.top + sh - ch - 10 : r.top + 10;
  loupeEl.style.left = `${r.left + sw - cw - 10}px`;
  loupeEl.style.top = `${top}px`;
  return true;
}

function drawLoupe() {
  const src = videoEl;
  if (!loupeOn || !natW || !src.videoWidth) return;
  if (!positionLoupe()) { document.body.classList.remove('loupe-on'); return; }

  const cw = loupeEl.width, ch = loupeEl.height;
  // Source window in image pixels: shrink by the zoom factor relative to how
  // large the picture is currently drawn.
  const srcW = cw / (scale * LOUPE_ZOOM), srcH = ch / (scale * LOUPE_ZOOM);
  const sx = clamp01(curX) * natW - srcW / 2;
  const sy = clamp01(curY) * natH - srcH / 2;

  lctx.imageSmoothingEnabled = false;   // pixel-exact: text stays legible
  lctx.clearRect(0, 0, cw, ch);
  try {
    lctx.drawImage(src, sx, sy, srcW, srcH, 0, 0, cw, ch);
  } catch { return; }                    // frame mid-decode; next tick redraws

  // Crosshair at the exact click point.
  lctx.strokeStyle = 'rgba(59,130,246,.95)';
  lctx.lineWidth = 1;
  lctx.beginPath();
  lctx.moveTo(cw / 2, ch / 2 - 10); lctx.lineTo(cw / 2, ch / 2 - 3);
  lctx.moveTo(cw / 2, ch / 2 + 3);  lctx.lineTo(cw / 2, ch / 2 + 10);
  lctx.moveTo(cw / 2 - 10, ch / 2); lctx.lineTo(cw / 2 - 3, ch / 2);
  lctx.moveTo(cw / 2 + 3, ch / 2);  lctx.lineTo(cw / 2 + 10, ch / 2);
  lctx.stroke();
  lctx.strokeStyle = 'rgba(255,255,255,.9)';
  lctx.beginPath();
  lctx.arc(cw / 2, ch / 2, 2, 0, Math.PI * 2);
  lctx.stroke();
}

// Show while a finger is down (that's when aim matters), linger briefly after.
function showLoupe() {
  if (!loupeOn) return;
  clearTimeout(loupeHideTimer);
  if (positionLoupe()) {
    document.body.classList.add('loupe-on');
    drawLoupe();
  }
}
function hideLoupeSoon(ms = 1200) {
  clearTimeout(loupeHideTimer);
  loupeHideTimer = setTimeout(() => {
    if (typingMode) return;   // still typing: keep the caret in view
    document.body.classList.remove('loupe-on');
  }, ms);
}

// Keep it fresh: the stream keeps moving even when the finger doesn't.
setInterval(() => {
  if (document.body.classList.contains('loupe-on')) drawLoupe();
}, 120);

const loupeBtn = document.getElementById('loupeBtn');
function setLoupe(on) {
  loupeOn = on;
  localStorage.setItem('pcr_loupe', on ? '1' : '0');
  loupeBtn.classList.toggle('on', on);
  if (!on) document.body.classList.remove('loupe-on');
  toast(on ? 'Loupe: on (shows while you touch)' : 'Loupe: off');
}
loupeBtn.addEventListener('click', () => setLoupe(!loupeOn));
loupeBtn.classList.toggle('on', loupeOn);

// ---- trays ----
// Twenty-one buttons in one scrolling row meant hunting for the one you wanted.
// The main row now holds only what's used continuously; keys and view controls
// open on demand, one tray at a time so they never stack over the picture.
const trays = {
  keys: document.getElementById('keysTray'),
  view: document.getElementById('viewTray'),
};
const trayBtns = {
  keys: document.getElementById('trayKeys'),
  view: document.getElementById('trayView'),
};
let openTray = null;

function setTray(name) {
  openTray = openTray === name ? null : name;
  for (const k of Object.keys(trays)) {
    trays[k].classList.toggle('open', openTray === k);
    trayBtns[k].classList.toggle('on', openTray === k);
  }
  // The picture area doesn't change size, but the loupe's free space does.
  if (document.body.classList.contains('loupe-on')) drawLoupe();
}
trayBtns.keys.addEventListener('click', () => setTray('keys'));
trayBtns.view.addEventListener('click', () => setTray('view'));

// Tapping the picture puts the trays away — they're transient, not fixtures.
stage.addEventListener('touchstart', () => { if (openTray) setTray(openTray); }, { passive: true });

// ---- picture ----
// Fragmented MP4 arrives over a WebSocket and plays through MediaSource. The
// codec family is negotiated: AV1 and HEVC compress a near-static desktop far
// better than H.264, but browser support is uneven, so the client only asks for
// what it has checked it can decode.
let h264ws = null, mediaSource = null, sourceBuffer = null, sbQueue = [];

function h264Stop() {
  try { h264ws && h264ws.close(); } catch {}
  h264ws = null;
  sourceBuffer = null;
  sbQueue = [];
  if (mediaSource && mediaSource.readyState === 'open') {
    try { mediaSource.endOfStream(); } catch {}
  }
  mediaSource = null;
  videoEl.removeAttribute('src');
  videoEl.load();
}

function pumpBuffer() {
  if (!sourceBuffer || sourceBuffer.updating || !sbQueue.length) return;
  try {
    sourceBuffer.appendBuffer(sbQueue.shift());
  } catch (e) {
    // QuotaExceeded: drop what's already been played and retry.
    if (e.name === 'QuotaExceededError') {
      try {
        const end = Math.max(0, videoEl.currentTime - 2);
        if (end > 0) sourceBuffer.remove(0, end);
      } catch {}
    } else {
      h264Restart();
    }
  }
}

let h264Retry = null;
function h264Restart() {
  clearTimeout(h264Retry);
  h264Retry = setTimeout(h264Start, 1200);
}

// Best codec both ends support. MIME strings: AV1 Main L4.0, HEVC Main, H.264
// Baseline — in descending order of compression, ascending order of support.
const CODEC_CANDIDATES = [
  { family: 'av1',  mime: 'video/mp4; codecs="av01.0.05M.08"' },
  { family: 'hevc', mime: 'video/mp4; codecs="hvc1.1.6.L93.B0"' },
  { family: 'h264', mime: 'video/mp4; codecs="avc1.42E01E"' },
];

function chooseCodec() {
  const MS = window.ManagedMediaSource || window.MediaSource;
  const server = settings?.h264?.families || ['h264'];
  const prefer = localStorage.getItem('pcr_family');       // manual override
  const list = prefer ? CODEC_CANDIDATES.filter(c => c.family === prefer).concat(CODEC_CANDIDATES)
                      : CODEC_CANDIDATES;
  for (const c of list) {
    if (server.includes(c.family) && MS && MS.isTypeSupported(c.mime)) return c;
  }
  return CODEC_CANDIDATES[CODEC_CANDIDATES.length - 1];
}

let activeCodec = null;

function h264Start() {
  h264Stop();
  graceUntil = Date.now() + CONNECT_GRACE_MS;
  lastFrameAt = Date.now();
  // iPhone Safari has no plain MediaSource; iOS 17+ exposes ManagedMediaSource
  // instead. Without either (older iOS), H.264 simply can't play here.
  const MS = window.ManagedMediaSource || window.MediaSource;
  if (!MS) {
    toast('This browser cannot play H.264 video');
    setStatus('offline');
    return;
  }
  mediaSource = new MS();
  // ManagedMediaSource needs the element to opt in to remote playback control.
  if (window.ManagedMediaSource && mediaSource instanceof window.ManagedMediaSource) {
    videoEl.disableRemotePlayback = true;
  }
  videoEl.src = URL.createObjectURL(mediaSource);

  mediaSource.addEventListener('sourceopen', () => {
    activeCodec = chooseCodec();
    const mime = activeCodec.mime;
    const MSc = window.ManagedMediaSource || window.MediaSource;
    if (!MSc.isTypeSupported(mime)) {
      toast('This browser cannot play ' + mime);
      setStatus('offline');
      return;
    }
    try {
      sourceBuffer = mediaSource.addSourceBuffer(mime);
    } catch {
      toast('Could not open the video decoder');
      setStatus('offline');
      return;
    }
    sourceBuffer.mode = 'sequence';   // timestamps come from the encoder
    sourceBuffer.addEventListener('updateend', pumpBuffer);

    const proto = location.protocol === 'https:' ? 'wss://' : 'ws://';
    h264ws = new WebSocket(proto + location.host + '/h264?codec=' + activeCodec.family);
    h264ws.binaryType = 'arraybuffer';
    h264ws.onmessage = (ev) => {
      lastFrameAt = Date.now();
      live = true;
      setStatus('live');
      sbQueue.push(new Uint8Array(ev.data));
      // Live view: never fall behind. If buffering drifts, jump to the edge.
      if (videoEl.buffered.length) {
        const end = videoEl.buffered.end(videoEl.buffered.length - 1);
        if (end - videoEl.currentTime > 1.5) videoEl.currentTime = end - 0.1;
      }
      pumpBuffer();
      if (videoEl.paused) videoEl.play().catch(() => {});
    };
    h264ws.onclose = () => { setStatus('offline'); h264Restart(); };
    h264ws.onerror = () => { try { h264ws.close(); } catch {} };
  });

  const onMeta = () => { adoptMediaSize(); };
  videoEl.addEventListener('loadedmetadata', onMeta);
  videoEl.addEventListener('resize', onMeta);      // resolution can change mid-stream
  setStatus('connecting');
}

// Name the codec actually in use in the settings sheet — the encoder is picked
// per browser, so it is worth showing which one won.
const _markSelected = markSelected;
markSelected = function () {
  _markSelected();
  const note = document.getElementById('codecNote');
  if (!note) return;
  if (!settings?.h264?.available) {
    note.textContent = 'ffmpeg not found on the PC — no picture';
    return;
  }
  const picked = activeCodec?.family || chooseCodec().family;
  const names = { av1: 'AV1', hevc: 'H.265', h264: 'H.264' };
  const enc = settings.h264.encoders?.[picked] || '';
  note.textContent = `Using ${names[picked] || picked}${enc ? ` (${enc})` : ''}`
    + (picked === 'av1' ? ' — best compression' : picked === 'hevc' ? ' — good compression' : ' — widest compatibility');
};

// ---- LAN shortcut ----
// Same page, but over the local network when you're on the same wifi: 4ms
// instead of ~78ms and no bandwidth ceiling. Clicking a plain http:// link
// from an https:// page is allowed (navigation, unlike fetch, isn't blocked as
// mixed content), so a tap is all it takes — and the links are generated from
// the machine's current addresses, so DHCP moving it doesn't strand you.
function renderLAN() {
  const box = document.getElementById('lanLinks');
  const note = document.getElementById('lanNote');
  if (!box || !settings) return;
  const urls = settings.lan_urls || [];
  const here = location.hostname;
  box.innerHTML = urls.length
    ? urls.map(u => {
        const host = u.replace(/^https?:\/\//, '').split('/')[0];
        const current = host.split(':')[0] === here;
        return `<a class="lanlink" href="${u}">${current ? '✓ ' : '➜ '}${host}${current ? ' (current)' : ''}</a>`;
      }).join('')
    : '';
  const onLan = urls.some(u => u.includes('//' + here + ':'));
  note.textContent = urls.length
    ? (onLan ? 'Connected over LAN' : 'Tap to switch to LAN (only works on the same Wi-Fi)')
    : 'The PC has no LAN address';
}

// Keep the links in step with whatever /settings last reported.
const _markSelected2 = markSelected;
markSelected = function () {
  _markSelected2();
  renderLAN();
};


// ---- control-channel indicator ----
// Green: socket carrying input. Amber: falling back to POSTs (works, slower).
// Red: nothing is getting through. Without this, a dead control channel is
// invisible — the picture streams on regardless and it just looks unresponsive.
const dot2 = document.getElementById('dot2');
setInterval(() => {
  if (!dot2) return;
  const cls = wsReady ? 'ok' : (wsBlocked ? 'slow' : 'dead');
  if (dot2.className !== cls) dot2.className = cls;
  dot2.title = wsReady ? 'Control: fast link'
    : wsBlocked ? 'Control: fallback mode (slower)'
    : 'Control: reconnecting';
}, 1000);
