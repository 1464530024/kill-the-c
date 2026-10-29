/* Kill the C —— 前端控制器
 *
 * 性能约定（重要）：
 *   1. 视图只在「切换页面」或「数据变化」时重建一次；
 *   2. 勾选、展开、改保留项等交互只做**定点 DOM 更新**，绝不重跑 renderView()；
 *   3. 长列表（文件清单）使用窗口化渲染，只创建可视区域内的行。
 */
'use strict';

const T = document.querySelector('meta[name=ktc-token]').content;
const MODE = document.querySelector('meta[name=ktc-mode]').content;

const CAT_GLYPH = {
  temp: '临', cache: '缓', log: '日', dump: '储', backup: '备', update: '更',
  installer: '包', video: '视', image: '图', audio: '音', archive: '压',
  document: '文', code: '码', program: '程', system: '系', other: '他', recycle: '站',
};
const RISK_CN = { safe: '安全', caution: '谨慎', risky: '高风险', protected: '受保护' };
const PRI_CN = { 0: 'P0', 1: 'P1', 2: 'P2', 3: 'P3' };
const CONF_CN = { high: '高置信', medium: '中置信', low: '低置信' };
const DUP_MIN_OPTS = [[1 << 20, '≥ 1 MB'], [10 << 20, '≥ 10 MB'], [50 << 20, '≥ 50 MB'], [100 << 20, '≥ 100 MB']];

const S = {
  sys: null,
  cfg: null,
  view: 'overview',
  mode: 'light',
  root: '',
  scan: null,
  selCats: new Set(),
  selFiles: new Set(),
  openCats: new Set(),
  sortKey: 'bytes',
  res: null,
  resDrive: '',
  resSel: new Set(),
  dup: null,
  dupSel: new Set(),
  dupScope: 'drive',
  dupMin: 0,
  openDups: new Set(),
  busy: false,
};

/* ── 基础工具 ─────────────────────────────────────────────────────── */

const $ = (s, r) => (r || document).querySelector(s);
const $$ = (s, r) => Array.from((r || document).querySelectorAll(s));

function h(tag, attrs, ...kids) {
  const n = document.createElement(tag);
  if (attrs) for (const k in attrs) {
    if (k === 'class') n.className = attrs[k];
    else if (k === 'html') n.innerHTML = attrs[k];
    else if (k === 'style') n.setAttribute('style', attrs[k]);
    else if (k.startsWith('on')) n.addEventListener(k.slice(2), attrs[k]);
    else if (attrs[k] !== null && attrs[k] !== undefined && attrs[k] !== false) n.setAttribute(k, attrs[k]);
  }
  for (const kid of kids.flat()) {
    if (kid === null || kid === undefined || kid === false) continue;
    n.appendChild(typeof kid === 'object' ? kid : document.createTextNode(String(kid)));
  }
  return n;
}
function svg(id, cls) {
  const s = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  if (cls) s.setAttribute('class', cls);
  const u = document.createElementNS('http://www.w3.org/2000/svg', 'use');
  u.setAttribute('href', '#' + id);
  s.appendChild(u);
  return s;
}
function clear(n) { while (n.firstChild) n.removeChild(n.firstChild); return n; }

function fmtSize(b) {
  if (!b || b < 0) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0, v = b;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return (i === 0 ? v.toFixed(0) : v.toFixed(v < 10 ? 2 : 1)) + ' ' + u[i];
}
function fmtNum(n) { return (n || 0).toLocaleString('zh-CN'); }
function fmtTime(t) {
  if (!t) return '—';
  const d = new Date(t * 1000);
  const p = (x) => String(x).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}
function fmtDur(ms) {
  if (ms < 1000) return ms + ' 毫秒';
  const s = ms / 1000;
  if (s < 60) return s.toFixed(1) + ' 秒';
  return Math.floor(s / 60) + ' 分 ' + Math.round(s % 60) + ' 秒';
}
function esc(s) { return String(s == null ? '' : s); }

function toast(msg, kind) {
  const box = $('#toasts');
  const t = h('div', { class: 'toast' + (kind ? ' ' + kind : '') }, msg);
  box.appendChild(t);
  setTimeout(() => { t.classList.add('out'); setTimeout(() => t.remove(), 260); }, kind === 'err' ? 5200 : 2600);
}

/* ── 接口 ─────────────────────────────────────────────────────────── */

async function api(path, body, method) {
  const opt = { method: method || (body === undefined ? 'GET' : 'POST'), headers: { 'X-KTC': T } };
  if (body !== undefined) { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
  const r = await fetch(path, opt);
  let j = null;
  try { j = await r.json(); } catch (e) { j = {}; }
  if (!r.ok) throw new Error(j.error || ('请求失败 (' + r.status + ')'));
  return j;
}

/* ── 窗口化列表（只渲染可视区域内的行）───────────────────────────── */

function vlist(rows, opt) {
  const rowH = opt.rowH || 30;
  const viewH = opt.height || 320;
  const spacer = h('div', { class: 'vspacer' });
  spacer.style.height = (rows.length * rowH + 4) + 'px';
  const win = h('div', { class: 'vlist' });
  win.style.height = viewH + 'px';
  const layer = h('div', { class: 'vlayer' });
  spacer.appendChild(layer);
  win.appendChild(spacer);

  let lastKey = '';
  let version = 0;
  const draw = () => {
    const first = Math.max(0, Math.floor(win.scrollTop / rowH) - 6);
    const count = Math.ceil(viewH / rowH) + 14;
    const end = Math.min(rows.length, first + count);
    const key = first + ':' + end + ':' + version;
    if (key === lastKey) return;
    lastKey = key;
    layer.style.transform = 'translateY(' + (first * rowH) + 'px)';
    const frag = document.createDocumentFragment();
    for (let i = first; i < end; i++) frag.appendChild(opt.row(rows[i], i));
    clear(layer);
    layer.appendChild(frag);
  };
  win.addEventListener('scroll', () => requestAnimationFrame(draw), { passive: true });
  requestAnimationFrame(draw);

  return {
    node: win,
    redraw() { version++; lastKey = ''; draw(); },
  };
}

/* 通用文件表（表头 + 窗口化表体） */
function fileTable(files, sel, onPick) {
  const rows = files.slice().sort((a, b) => b.size - a.size);
  if (!rows.length) {
    return h('div', { class: 'dim t12', style: 'padding:10px 2px' }, '该类目暂无可逐项选择的文件');
  }
  const row = (f) => {
    const cb = h('input', { type: 'checkbox', class: 'check' });
    cb.checked = sel.has(f.path);
    cb.disabled = !f.selectable;
    const r = h('div', { class: 'vrow' },
      h('div', { class: 'c' }, cb),
      h('div', { class: 'pth' }, h('span', { class: 'ellip', title: f.path }, f.name)),
      h('div', { class: 'num' }, fmtSize(f.size)),
      h('div', null, h('span', { class: 'badge ' + f.risk, title: (f.reasons || []).join('；') }, RISK_CN[f.risk] || f.risk)),
      h('div', { class: 'dim t11' }, fmtTime(f.mod)));
    cb.onclick = (e) => {
      e.stopPropagation();
      if (cb.checked) sel.add(f.path); else sel.delete(f.path);
      if (onPick) onPick(f);
    };
    r.onclick = (e) => { if (e.target !== cb) cb.click(); };
    return r;
  };
  const head_ = h('div', { class: 'vrow vhead' },
    h('div', { class: 'c' }),
    h('div', null, '文件名'),
    h('div', { class: 'num' }, '大小'),
    h('div', null, '风险'),
    h('div', null, '修改时间'));
  const vl = vlist(rows, { row, height: 322, rowH: 30 });
  const box = h('div', { class: 'vbox' }, head_, vl.node);
  box.sync = () => vl.redraw();
  return box;
}

/* ── 模态框 ───────────────────────────────────────────────────────── */

function openModal(build, opts) {
  opts = opts || {};
  const ov = $('#overlay');
  const box = h('div', { class: 'modal' + (opts.wide ? ' wide' : '') });
  clear(ov).appendChild(box);
  ov.classList.add('show');
  const close = () => { ov.classList.remove('show'); ov.onclick = null; document.onkeydown = null; clear(ov); };
  build(box, close);
  if (!opts.persistent) {
    ov.onclick = (e) => { if (e.target === ov) close(); };
    document.onkeydown = (e) => { if (e.key === 'Escape') close(); };
  }
  return { box, close };
}

function confirmBox(o) {
  return new Promise((resolve) => {
    let typedInput = null;
    let chk = null;
    openModal((box, close) => {
      box.appendChild(h('h3', null, o.title));
      if (o.desc) box.appendChild(h('p', { html: o.desc }));
      if (o.extra) box.appendChild(o.extra);
      if (o.needType) {
        typedInput = h('input', { class: 'typebox', placeholder: '在此输入 ' + o.needType });
        box.appendChild(h('div', { class: 'mt16' }, typedInput));
      }
      if (o.requireCheck) {
        chk = o._chk || h('input', { type: 'checkbox', class: 'check' });
        box.appendChild(h('label', { class: 'chkline' }, chk, h('span', null, o.checkText || '我已核对以上内容')));
      }
      const foot = h('div', { class: 'foot' });
      foot.appendChild(h('button', { class: 'btn', onclick: () => { close(); resolve(false); } }, o.cancelText || '取消'));
      foot.appendChild(h('button', {
        class: 'btn ' + (o.danger ? 'danger' : 'primary'),
        onclick: () => {
          if (chk && !chk.checked) { toast('请先勾选确认项', 'err'); return; }
          if (typedInput && typedInput.value.trim().toUpperCase() !== o.needType.toUpperCase()) {
            toast('请输入 ' + o.needType + ' 以确认', 'err');
            typedInput.focus();
            return;
          }
          close();
          resolve(true);
        },
      }, o.confirmText || '确认'));
      box.appendChild(foot);
      if (typedInput) setTimeout(() => typedInput.focus(), 60);
    });
  });
}

/* 进度弹窗：环形进度 + 平滑等待动画 + 三列对齐统计 */
function progressModal(title, sub, labels) {
  const LB = labels || { a: '已处理', b: '已释放', c: '耗时' };
  const R = 55, C = 2 * Math.PI * R;

  const ring = h('div', {
    class: 'ringbox',
    html:
      '<svg viewBox="0 0 132 132" aria-hidden="true">' +
      '<defs><linearGradient id="kgrad" x1="0" y1="0" x2="1" y2="1">' +
      '<stop offset="0" stop-color="#57a8ff"/><stop offset="1" stop-color="#0064cf"/></linearGradient></defs>' +
      '<circle cx="66" cy="66" r="' + R + '" fill="none" stroke="rgba(120,120,128,.17)" stroke-width="10"/>' +
      '<circle class="arc" cx="66" cy="66" r="' + R + '" fill="none" stroke="url(#kgrad)" stroke-width="10" ' +
      'stroke-linecap="round" stroke-dasharray="' + C.toFixed(2) + '" stroke-dashoffset="' + C.toFixed(2) + '" ' +
      'transform="rotate(-90 66 66)"/></svg>' +
      '<div class="sweep"></div>',
  });
  const pctNum = h('b', null, '0');
  const pctUnit = h('span', null, '项');
  ring.appendChild(h('div', { class: 'pct' }, pctNum, pctUnit));

  const cur = h('div', { class: 'cur' }, '准备中…');
  const sA = h('b', null, '0'), sB = h('b', null, '0 B'), sC = h('b', null, '0s');
  const stats = h('div', { class: 'pstats' },
    h('div', null, sA, h('i', null, LB.a)),
    h('div', null, sB, h('i', null, LB.b)),
    h('div', null, sC, h('i', null, LB.c)));

  const cancelBtn = h('button', { class: 'btn' }, '停止');
  const panel = h('div', { class: 'modal prog-modal' },
    h('h3', null, title),
    h('p', null, sub || ''),
    ring, cur, stats,
    h('div', { class: 'foot' }, h('div', { class: 'spacer' }), cancelBtn));

  const shell = $('#overlay');
  clear(shell).appendChild(panel);
  shell.classList.add('show');

  const arc = $('.arc', ring);
  let done = 0, total = 0, bytes = 0, elapsed = 0, pct = 0;

  const paint = () => {
    const indet = !(total > 0);
    ring.classList.toggle('indet', indet);
    if (indet) {
      pctNum.textContent = fmtNum(done);
      pctUnit.textContent = '项';
    } else {
      pctNum.textContent = String(Math.min(100, Math.floor(pct)));
      pctUnit.textContent = '%';
    }
    arc.setAttribute('stroke-dashoffset', String(C * (1 - (indet ? 0 : Math.min(1, pct / 100) * 0.999))));
    sA.textContent = fmtNum(done);
    sB.textContent = fmtSize(bytes);
    sC.textContent = (elapsed / 1000).toFixed(0) + 's';
  };
  paint();

  return {
    update(e) {
      if (e.current) cur.textContent = e.current;
      if (typeof e.done === 'number') done = e.done;
      if (typeof e.total === 'number') total = e.total;
      if (typeof e.bytes === 'number') bytes = e.bytes;
      if (typeof e.elapsed === 'number') elapsed = e.elapsed;
      if (total > 0) pct = Math.min(99.5, (done / total) * 100);
      paint();
    },
    setText(t) { cur.textContent = t; },
    onCancel(fn) { cancelBtn.onclick = fn; },
    close() { shell.classList.remove('show'); clear(shell); },
  };
}

/* ── 任务事件流 ───────────────────────────────────────────────────── */

async function runJob(jobId, pv, opt) {
  opt = opt || {};
  return new Promise((resolve) => {
    let settled = false;
    const finish = (st) => {
      if (settled) return;
      settled = true;
      es.close(); clearInterval(poll);
      resolve(st);
    };
    let lastAt = Date.now();
    const es = new EventSource('/api/events?id=' + encodeURIComponent(jobId) + '&token=' + encodeURIComponent(T));
    es.onmessage = (ev) => {
      let e; try { e = JSON.parse(ev.data); } catch (x) { return; }
      if (e.kind === 'progress' || e.kind === 'start') { lastAt = Date.now(); pv.update(e); }
      if (e.kind === 'log') toast(e.message);
      if (e.kind === 'done' || e.kind === 'error' || e.kind === 'canceled') {
        api('/api/job?id=' + encodeURIComponent(jobId)).then(finish).catch(() => finish(null));
      }
    };
    es.onerror = () => { /* 由轮询兜底 */ };
    const poll = setInterval(async () => {
      if (settled) return;
      if (Date.now() - lastAt < 2500) return; // 事件流正常时不额外请求
      try {
        const st = await api('/api/job?id=' + encodeURIComponent(jobId));
        pv.update(st.last || {});
        if (st.status !== 'running') finish(st);
      } catch (e) { /* 任务可能已过期 */ }
    }, 1500);
    if (opt.onStart) opt.onStart();
  });
}

/* ── 启动 ─────────────────────────────────────────────────────────── */

async function boot() {
  if (MODE === 'uninstall') { renderUninstall(); return; }
  try {
    S.sys = await api('/api/system');
  } catch (e) {
    document.body.innerHTML = '<div style="padding:60px;text-align:center;font-size:14px">无法连接本地服务：' + esc(e.message) + '</div>';
    return;
  }
  S.cfg = S.sys.config;
  S.mode = S.cfg.lastMode === 'deep' ? 'deep' : 'light';
  S.dupMin = S.cfg.minDupSize || (1 << 20);
  setTheme(S.cfg.theme, false);
  S.root = (S.sys.volumes[0] && S.sys.volumes[0].root) || 'C:\\';
  S.resDrive = S.root;

  $('#shell').classList.remove('hidden');
  renderNav();
  renderModeSeg();
  renderAdmin();
  renderView(); // 首屏直接渲染（go() 对同视图会短路）
  $('#themeSwitch').onclick = () => setTheme(document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark', true);
  setInterval(refreshSystem, 20000);
}

async function refreshSystem() {
  try {
    S.sys = await api('/api/system');
    if (S.view === 'overview') renderView();
  } catch (e) { /* ignore */ }
}

function setTheme(t, persist) {
  document.documentElement.dataset.theme = t;
  $('#themeSwitch').classList.toggle('on', t === 'dark');
  if (persist) api('/api/config', { theme: t }).catch(() => {});
}

function renderAdmin() {
  const chip = $('#adminChip');
  clear(chip);
  chip.appendChild(h('span', { class: 'bulb' }));
  chip.appendChild(h('span', null, S.sys.admin ? '管理员权限' : '普通权限'));
  chip.classList.toggle('ok', !!S.sys.admin);
  if (!S.sys.admin) {
    chip.appendChild(h('button', {
      class: 'btn sm glass',
      onclick: async () => {
        if (!await confirmBox({ title: '请求管理员权限', desc: '部分系统目录（如 Windows 临时目录、更新缓存）需要管理员权限才能清理。<br>将以管理员身份重新启动本程序，当前窗口会关闭。', confirmText: '提权重启' })) return;
        try { await api('/api/system/elevate', {}); toast('已发起提权，请在弹出的 UAC 窗口中确认'); } catch (e) { toast(e.message, 'err'); }
      },
    }, '提权'));
  }
}

const NAV = [
  { id: 'overview', name: '总览', icon: 'i-overview' },
  { id: 'clean', name: '磁盘清理', icon: 'i-broom' },
  { id: 'residue', name: '残留扫描', icon: 'i-radar' },
  { id: 'dupes', name: '重复文件', icon: 'i-copy' },
  { id: 'whitelist', name: '白名单', icon: 'i-shield' },
  { id: 'about', name: '关于', icon: 'i-info' },
];

function renderNav() {
  const nav = clear($('#nav'));
  NAV.forEach((n) => {
    nav.appendChild(h('button', {
      class: 'nav-item' + (S.view === n.id ? ' active' : ''),
      'data-view': n.id,
      onclick: () => go(n.id),
    }, svg(n.icon), h('span', null, n.name)));
  });
}

function renderModeSeg() {
  const seg = clear($('#modeSeg'));
  const thumb = h('div', { class: 'thumb' });
  seg.appendChild(thumb);
  [['light', '轻度清理'], ['deep', '深度清理']].forEach(([k, t]) => {
    seg.appendChild(h('button', {
      class: S.mode === k ? 'on' : '',
      onclick: () => {
        if (S.mode === k) return;
        S.mode = k; renderModeSeg();
        api('/api/config', { lastMode: k }).catch(() => {});
        if (S.view === 'clean') renderView();
      },
    }, t));
  });
  requestAnimationFrame(() => placeThumb(seg));
  $('#modeHint').textContent = S.mode === 'light'
    ? '仅清理明确安全的系统垃圾与缓存'
    : '额外纳入更新缓存、大文件与卸载残留';
}

/* 把分段控件的滑块移到选中的按钮下 */
function placeThumb(seg) {
  const thumb = seg.querySelector('.thumb');
  const btns = seg.querySelectorAll('button');
  let idx = 0;
  btns.forEach((b, i) => { if (b.classList.contains('on')) idx = i; });
  const b = btns[idx];
  if (!thumb || !b) return;
  thumb.style.width = b.offsetWidth + 'px';
  thumb.style.transform = `translateX(${b.offsetLeft - 2}px)`;
}

/* 生成一个分段控件；返回 { node, set } —— set 只改样式，不重建 DOM */
function segPick(opts, cur, onPick) {
  const seg = h('div', { class: 'seg' });
  seg.appendChild(h('div', { class: 'thumb' }));
  opts.forEach(([k, t]) => {
    seg.appendChild(h('button', { class: k === cur ? 'on' : '', 'data-k': k }, t));
  });
  const set = (k) => {
    seg.querySelectorAll('button').forEach((b) => b.classList.toggle('on', b.dataset.k === k));
    requestAnimationFrame(() => placeThumb(seg));
  };
  seg.addEventListener('click', (e) => {
    const b = e.target.closest('button');
    if (!b) return;
    set(b.dataset.k);
    onPick(b.dataset.k);
  });
  requestAnimationFrame(() => placeThumb(seg));
  return { node: seg, set };
}

function go(v) {
  if (S.view === v && $('#content').firstChild) return;
  S.view = v;
  $$('.nav-item').forEach((n) => n.classList.toggle('active', n.dataset.view === v));
  renderView();
  $('#content').scrollTop = 0;
}

function renderView() {
  const c = clear($('#content'));
  const map = { overview: renderOverview, clean: renderClean, residue: renderResidue, dupes: renderDupes, whitelist: renderWhitelist, about: renderAbout };
  (map[S.view] || renderOverview)(c);
}

function head(c, title, sub, ...right) {
  const hd = h('div', { class: 'page-head' },
    h('div', null, h('h2', null, title), h('p', null, sub)), h('div', { class: 'spacer' }));
  right.flat().forEach((r) => r && hd.appendChild(r));
  c.appendChild(hd);
  return hd;
}

/* ── 总览 ─────────────────────────────────────────────────────────── */

function gaugeSVG(pct) {
  const R = 78, C = 2 * Math.PI * R;
  const off = C * (1 - Math.min(1, Math.max(0, pct / 100)) * 0.999);
  const col = pct > 90 ? '#ff3b30' : pct > 75 ? '#ff9f0a' : '#0a84ff';
  const ns = 'http://www.w3.org/2000/svg';
  const s = document.createElementNS(ns, 'svg');
  s.setAttribute('viewBox', '0 0 168 168');
  s.innerHTML =
    `<defs><linearGradient id="gg" x1="0" y1="0" x2="1" y2="1">
       <stop offset="0" stop-color="${col}"/><stop offset="1" stop-color="${col}" stop-opacity=".78"/></linearGradient></defs>
     <circle cx="84" cy="84" r="78" fill="none" stroke="rgba(120,120,128,.18)" stroke-width="11"/>
     <circle cx="84" cy="84" r="78" fill="none" stroke="url(#gg)" stroke-width="11" stroke-linecap="round"
       stroke-dasharray="${C.toFixed(1)}" stroke-dashoffset="${off.toFixed(1)}" transform="rotate(-90 84 84)"/>`;
  return s;
}

function renderOverview(c) {
  head(c, '总览', '一键掌握系统盘的健康状况');
  const v = (S.sys.volumes || []).find((x) => /^c:/i.test(x.root)) || S.sys.volumes[0] ||
    { root: 'C:\\', totalBytes: 0, freeBytes: 0, usedBytes: 0, usedPercent: 0 };
  const rb = S.sys.recycle || { size: 0, count: 0 };

  const gauge = h('div', { class: 'gauge' }, gaugeSVG(v.usedPercent),
    h('div', { class: 'inner' },
      h('div', { class: 'pct' }, Math.round(v.usedPercent), h('span', null, '%')),
      h('div', { class: 'cap' }, '已使用')),
    h('div', { class: 'sheen' }));

  const stats = h('div', { class: 'stat-list' },
    h('div', { class: 'stat' }, h('span', { class: 'k' }, '卷标'), h('span', { class: 'v' }, v.label || '系统盘'), h('span', { class: 'sub' }, v.fileSystem || 'NTFS')),
    h('div', { class: 'stat' }, h('span', { class: 'k' }, '总容量'), h('span', { class: 'v' }, fmtSize(v.totalBytes))),
    h('div', { class: 'stat' }, h('span', { class: 'k' }, '已用'), h('span', { class: 'v' }, fmtSize(v.usedBytes))),
    h('div', { class: 'stat' }, h('span', { class: 'k' }, '可用'), h('span', { class: 'v', style: 'color:var(--ok)' }, fmtSize(v.freeBytes))),
    h('div', { class: 'bar mt8' }, h('i', { style: 'width:' + Math.min(100, v.usedPercent) + '%' })));

  c.appendChild(h('div', { class: 'card' }, h('div', { class: 'gauge-wrap' }, gauge, stats)));

  const qa = h('div', { class: 'card' }, h('h3', null, '快速操作', h('span', { class: 'hint' }, '选择一项开始')));
  [
    { icon: 'i-broom', t: '扫描并清理 C 盘', d: '全盘分类 · 风险评级', f: () => go('clean') },
    { icon: 'i-radar', t: '扫描卸载残留', d: '找出已卸载应用的文件', f: () => go('residue') },
    { icon: 'i-copy', t: '查找重复文件', d: '哈希比对 · 一键选中', f: () => go('dupes') },
  ].forEach((q) => {
    qa.appendChild(h('button', { class: 'btn block qbtn', onclick: q.f },
      svg(q.icon),
      h('div', { class: 'qtx' }, h('div', { class: 'qt' }, q.t), h('div', { class: 'qd' }, q.d)),
      svg('i-caret')));
  });
  c.appendChild(qa);

  const scanInfo = S.scan
    ? h('div', { class: 'stat-list' },
      h('div', { class: 'stat' }, h('span', { class: 'k' }, '可清理'), h('span', { class: 'v', style: 'color:var(--danger)' }, fmtSize(S.scan.junkBytes))),
      h('div', { class: 'stat' }, h('span', { class: 'k' }, '垃圾文件'), h('span', { class: 'v' }, fmtNum(S.scan.junkCount), h('small', null, '个'))),
      h('div', { class: 'stat' }, h('span', { class: 'k' }, '扫描文件'), h('span', { class: 'v' }, fmtNum(S.scan.totalFiles), h('small', null, '个'))),
      h('div', { class: 'stat' }, h('span', { class: 'k' }, '耗时'), h('span', { class: 'v' }, fmtDur(S.scan.elapsedMs))))
    : h('div', { class: 'empty' }, h('div', { class: 'big' }, '◌'), h('p', null, '尚未扫描，前往「磁盘清理」开始'));

  const rbCard = h('div', { class: 'card' },
    h('h3', null, '回收站', h('span', { class: 'hint' }, '删除的文件先进入这里，仍会占用磁盘')),
    h('div', { class: 'stat-list' },
      h('div', { class: 'stat' }, h('span', { class: 'k' }, '占用空间'), h('span', { class: 'v' }, fmtSize(rb.size))),
      h('div', { class: 'stat' }, h('span', { class: 'k' }, '项目数'), h('span', { class: 'v' }, fmtNum(rb.count), h('small', null, '个')))),
    h('div', { class: 'mt16' }, h('button', { class: 'btn danger block', onclick: () => emptyBinFlow() }, svg('i-trash'), '一键清空回收站')));

  c.appendChild(h('div', { class: 'grid c2' },
    h('div', { class: 'card' }, h('h3', null, S.scan ? '上次扫描结果' : '扫描概览'), scanInfo),
    rbCard));
}

/* ── 磁盘清理 ─────────────────────────────────────────────────────── */

const CLEAN = { cats: new Map(), sumCount: null, sumBytes: null };

function renderClean(c) {
  head(c, '磁盘清理', '扫描全盘文件，按类型分类并给出优先级与风险评估');

  const rootSel = h('div', { class: 'drivechips' });
  (S.sys.volumes || []).forEach((v) => {
    rootSel.appendChild(h('div', {
      class: 'chip' + (v.root === S.root ? ' on' : ''),
      onclick: () => { S.root = v.root; S.scan = null; renderView(); },
    }, v.root.replace(/\\$/, '') + ' 盘 · ' + fmtSize(v.freeBytes) + ' 可用'));
  });

  c.appendChild(h('div', { class: 'card tight' },
    h('h3', null, '扫描范围', h('span', { class: 'hint' }, S.mode === 'light' ? '轻度模式：只找出明确安全的垃圾' : '深度模式：同时纳入更新缓存、大文件与残留')),
    rootSel,
    h('div', { class: 'flexrow' },
      h('button', { class: 'btn primary', onclick: () => startScan() }, svg('i-scan'), '开始扫描'),
      h('span', { class: 'dim t12' }, '扫描仅统计，不会删除任何文件'))));

  if (!S.scan) {
    c.appendChild(h('div', { class: 'card' }, h('div', { class: 'empty' },
      h('div', { class: 'big' }, '◌'),
      h('p', null, '点击「开始扫描」后，这里会列出可清理的内容与风险评估'))));
    return;
  }

  const r = S.scan;
  const cntEl = h('div', { class: 'sumN' }, '0 项');
  const byteEl = h('div', { class: 'sumB' }, '0 B');

  const sum = h('div', { class: 'sumrow' },
    h('div', { class: 'sumleft' }, cntEl, byteEl),
    h('div', { class: 'spacer' }),
    h('button', { class: 'btn', onclick: () => { selectAllSafe(); syncCleanAll(); } }, '选中全部安全项'),
    h('button', { class: 'btn', onclick: () => { S.selCats.clear(); S.selFiles.clear(); syncCleanAll(); } }, '清空选择'),
    h('button', { class: 'btn danger', onclick: () => cleanFlow() }, svg('i-trash'), '执行清理'));

  const sortSeg = segPick([['bytes', '按大小'], ['count', '按数量'], ['priority', '按优先级']],
    S.sortKey, (k) => { S.sortKey = k; reorderCats(); });

  const sortBar = h('div', { class: 'flexrow sortbar' },
    h('span', { class: 'dim t12' }, '排序与筛选：'),
    sortSeg.node,
    h('span', { class: 'spacer' }),
    h('span', { class: 'dim t12' }, '共 ' + fmtNum(r.totalFiles) + ' 个文件 · ' + fmtSize(r.totalBytes) + ' · 用时 ' + fmtDur(r.elapsedMs)));

  const wrap = h('div', { class: 'card' },
    h('h3', null, '清理清单', h('span', { class: 'hint' }, '勾选类目 = 清理该类目下全部匹配文件')),
    sum, sortBar);

  CLEAN.cats.clear();
  CLEAN.sumCount = cntEl;
  CLEAN.sumBytes = byteEl;
  CLEAN.host = h('div', { class: 'catlist' });
  const cats = sortCats((r.categories || []).filter((x) => x.junkBytes > 0 || x.key === 'recycle'));
  cats.forEach((cat) => CLEAN.host.appendChild(catBlock(cat)));
  if (!cats.length) CLEAN.host.appendChild(h('div', { class: 'empty' }, h('p', null, '未发现可清理的内容')));
  wrap.appendChild(CLEAN.host);
  c.appendChild(wrap);

  if (S.mode === 'deep') {
    const bigs = h('div', { class: 'card' }, h('h3', null, '大文件（深度模式）', h('span', { class: 'hint' }, '超过 100MB 的文件，请确认后再清理')));
    const bf = (r.bigFiles || []).slice(0, 200);
    if (!bf.length) bigs.appendChild(h('div', { class: 'empty' }, h('p', null, '未发现 100MB 以上的大文件')));
    else bigs.appendChild(fileTable(bf, S.selFiles, () => syncCleanAll()));
    c.appendChild(bigs);
  }

  syncCleanAll();
}

/* 只调整顺序，复用已建好的节点 */
function reorderCats() {
  if (!CLEAN.host) return;
  const cats = sortCats((S.scan.categories || []).filter((x) => x.junkBytes > 0 || x.key === 'recycle'));
  const frag = document.createDocumentFragment();
  cats.forEach((cat) => {
    const rec = CLEAN.cats.get(cat.key);
    if (rec) frag.appendChild(rec.node);
  });
  clear(CLEAN.host).appendChild(frag);
}

function sortCats(list) {
  const k = S.sortKey || 'bytes';
  return list.slice().sort((a, b) => {
    if (k === 'count') return b.junkCount - a.junkCount;
    if (k === 'priority') return a.priority - b.priority || b.junkBytes - a.junkBytes;
    if (k === 'type') return a.name.localeCompare(b.name, 'zh-CN');
    return b.junkBytes - a.junkBytes;
  });
}

function catBlock(cat) {
  const open = S.openCats.has(cat.key);
  const cb = h('input', { type: 'checkbox', class: 'check' });
  const body = h('div', { class: 'cbody' + (open ? '' : ' hidden') });
  const headEl = h('header', null,
    cb,
    h('div', { class: 'ic' }, CAT_GLYPH[cat.key] || '他'),
    h('div', { class: 'nmrow' },
      h('div', { class: 'nm' }, cat.name),
      h('div', { class: 'ds' }, cat.desc || '')),
    h('div', { class: 'flexrow', style: 'gap:6px' },
      h('span', { class: 'badge ' + (cat.priority <= 1 ? 'p' + cat.priority : 'p2') }, PRI_CN[cat.priority] || 'P3'),
      h('span', { class: 'badge ' + cat.risk }, RISK_CN[cat.risk] || cat.risk)),
    h('div', { class: 'sz' },
      h('b', null, fmtSize(cat.junkBytes)),
      h('i', null, cat.synthetic ? '整体占用' : fmtNum(cat.junkCount) + ' 个文件 · 共 ' + fmtSize(cat.bytes))),
    svg('i-caret', 'caret'));

  const node = h('div', { class: 'cat' + (open ? ' open' : '') }, headEl, body);
  const rec = { cat, cb, body, node, built: false, table: null };
  CLEAN.cats.set(cat.key, rec);

  cb.onclick = (e) => { e.stopPropagation(); toggleCat(cat.key); };
  headEl.onclick = () => toggleCatOpen(cat.key);

  if (open) buildCatBody(rec);
  return node;
}

function buildCatBody(rec) {
  const cat = rec.cat;
  rec.built = true;
  const files = cat.junkFiles || [];
  if (cat.key === 'recycle') {
    rec.body.appendChild(h('div', { class: 'alert info' },
      h('b', null, '回收站'), '：这里统计的是回收站整体占用，执行后会调用系统「清空回收站」。'));
  }
  if (cat.truncated) {
    rec.body.appendChild(h('div', { class: 'alert', html:
      '<b>提示</b>：该类目文件数量极多，下方列表仅展示体积最大的前 ' + files.length +
      ' 项。勾选类目复选框将清理该类目下<strong>全部</strong>匹配文件。' }));
  }
  if (files.length) {
    rec.table = fileTable(files, S.selFiles, () => { syncCat(cat.key, false); syncCleanSummary(); });
    rec.body.appendChild(rec.table);
  } else {
    rec.body.appendChild(h('div', { class: 'dim t12', style: 'padding:10px 2px' }, '该类目暂无可逐项选择的文件'));
  }
}

function toggleCatOpen(key) {
  const rec = CLEAN.cats.get(key);
  if (!rec) return;
  const open = !S.openCats.has(key);
  if (open) S.openCats.add(key); else S.openCats.delete(key);
  rec.node.classList.toggle('open', open);
  if (open) {
    if (!rec.built) buildCatBody(rec);
    rec.body.classList.remove('hidden');
  } else {
    rec.body.classList.add('hidden');
  }
}

function toggleCat(key) {
  const rec = CLEAN.cats.get(key);
  if (!rec) return;
  if (S.selCats.has(key)) {
    S.selCats.delete(key);
  } else {
    S.selCats.add(key);
    (rec.cat.junkFiles || []).forEach((f) => S.selFiles.delete(f.path));
  }
  syncCat(key, true);
  syncCleanSummary();
}

function syncCat(key, refreshTable) {
  const rec = CLEAN.cats.get(key);
  if (!rec) return;
  const all = S.selCats.has(key);
  const files = rec.cat.junkFiles || [];
  let picked = 0;
  if (!all) {
    for (const f of files) if (S.selFiles.has(f.path)) picked++;
  }
  rec.cb.checked = all;
  rec.cb.indeterminate = !all && picked > 0;
  if (refreshTable && rec.table && rec.table.sync) rec.table.sync();
}

function syncCleanAll() {
  CLEAN.cats.forEach((_, key) => syncCat(key, true));
  syncCleanSummary();
}

function syncCleanSummary() {
  if (!S.scan) return;
  if (CLEAN.sumCount) CLEAN.sumCount.textContent = fmtNum(selectedCount()) + ' 项';
  if (CLEAN.sumBytes) CLEAN.sumBytes.textContent = fmtSize(selectedBytes());
}

function selectedFiles() {
  const out = [];
  const seen = new Set();
  if (!S.scan) return out;
  (S.scan.categories || []).forEach((c) => {
    (c.junkFiles || []).forEach((f) => {
      if (!S.selFiles.has(f.path) || seen.has(f.path)) return;
      seen.add(f.path);
      out.push({ path: f.path, size: f.size });
    });
  });
  (S.scan.bigFiles || []).forEach((f) => {
    if (!S.selFiles.has(f.path) || seen.has(f.path)) return;
    seen.add(f.path);
    out.push({ path: f.path, size: f.size });
  });
  return out;
}

function selectedBytes() {
  let b = 0;
  if (!S.scan) return 0;
  (S.scan.categories || []).forEach((c) => { if (S.selCats.has(c.key)) b += c.junkBytes; });
  selectedFiles().forEach((f) => { b += f.size; });
  return b;
}

function selectedCount() {
  let n = 0;
  if (!S.scan) return 0;
  (S.scan.categories || []).forEach((c) => {
    if (S.selCats.has(c.key)) n += c.synthetic ? 1 : (c.junkCount || (c.junkFiles || []).length);
  });
  return n + selectedFiles().length;
}

function selectAllSafe() {
  if (!S.scan) return;
  (S.scan.categories || []).forEach((c) => {
    if (c.key === 'recycle') return;
    if (c.risk === 'safe' && c.junkBytes > 0) S.selCats.add(c.key);
  });
}

async function startScan() {
  if (S.busy) return;
  S.busy = true;
  S.scan = null; S.selCats.clear(); S.selFiles.clear(); S.openCats.clear();
  const pv = progressModal('正在扫描 ' + S.root, '统计文件大小与类型，不修改任何数据',
    { a: '已扫描', b: '已读体积', c: '耗时' });
  try {
    const { job } = await api('/api/scan', { root: S.root, deep: S.mode === 'deep' });
    pv.onCancel(async () => { await api('/api/job/cancel', { path: job }); });
    const st = await runJob(job, pv);
    if (!st || st.status === 'error') toast((st && st.error) || '扫描失败', 'err');
    else if (st.status === 'canceled') toast('扫描已取消');
    else {
      S.scan = st.result;
      (S.scan.categories || []).filter((x) => x.junkBytes > 0).slice(0, 3)
        .forEach((x) => S.openCats.add(x.key)); // 默认只展开前 3 个类目，避免一次建出大量表格
      autoSelect();
      toast('扫描完成，可清理 ' + fmtSize(S.scan.junkBytes), 'ok');
    }
  } catch (e) { toast(e.message, 'err'); }
  pv.close();
  S.busy = false;
  renderView();
}

function autoSelect() {
  if (!S.scan) return;
  selectAllSafe();
  if (S.mode === 'deep') {
    (S.scan.categories || []).forEach((c) => { if (c.risk === 'caution' && c.priority <= 1) S.selCats.add(c.key); });
  }
}

async function cleanFlow() {
  const cats = Array.from(S.selCats);
  const files = selectedFiles();
  if (!cats.length && !files.length) { toast('请先选择要清理的项目', 'err'); return; }

  const totalBytes = selectedBytes();
  const totalCount = selectedCount();
  let method = 'recycle';

  const methodBox = h('div', null,
    h('div', { class: 'radio on', 'data-m': 'recycle', onclick: (e) => pick(e.currentTarget) },
      h('div', { class: 'rd' }), h('div', { class: 'tx' }, h('b', null, '移入回收站（推荐）'), h('span', null, '可随时从回收站还原，安全兜底'))),
    h('div', { class: 'radio', 'data-m': 'permanent', onclick: (e) => pick(e.currentTarget) },
      h('div', { class: 'rd' }), h('div', { class: 'tx' }, h('b', null, '永久删除'), h('span', null, '立即释放空间，删除后无法恢复'))));
  function pick(node) {
    $$('.radio', methodBox).forEach((r) => r.classList.remove('on'));
    node.classList.add('on');
    method = node.dataset.m;
  }

  const listBox = h('div', { class: 'riskbox' });
  cats.slice(0, 14).forEach((k) => {
    const c = (S.scan.categories || []).find((x) => x.key === k) || {};
    listBox.appendChild(h('div', { class: 'li' },
      h('span', { class: 'n' }, '● ' + (c.name || k)),
      h('span', { class: 'badge ' + (c.risk || 'caution') }, RISK_CN[c.risk] || '谨慎'),
      h('span', { class: 'dim nowrap' }, '整类清理')));
  });
  if (cats.length > 14) listBox.appendChild(h('div', { class: 'li dim' }, '… 以及另外 ' + (cats.length - 14) + ' 个类目'));
  files.slice(0, 12).forEach((f) => listBox.appendChild(h('div', { class: 'li' },
    h('span', { class: 'n', title: f.path }, f.path),
    h('span', { class: 'right num mono' }, fmtSize(f.size)))));
  if (files.length > 12) listBox.appendChild(h('div', { class: 'li dim' }, '… 以及另外 ' + (files.length - 12) + ' 个文件'));

  const ok = await confirmBox({
    danger: true,
    title: '确认执行清理？',
    desc: '即将清理 <b>' + fmtNum(totalCount) + '</b> 项内容，预计释放 <b>' + fmtSize(totalBytes) + '</b>。<br>白名单目录与系统保护区内的文件不会被触碰。',
    extra: h('div', null,
      h('div', { class: 'alert' }, h('b', null, '二次确认'), '：请核对下方清单，确认无误后再执行。'),
      methodBox, listBox),
    requireCheck: true,
    checkText: '我已核对以上内容，确认执行清理',
    confirmText: '确认清理', cancelText: '再想想',
  });
  if (!ok) return;

  const pv = progressModal('正在清理', method === 'permanent' ? '永久删除所选内容' : '正在把所选内容移入回收站');
  try {
    const { job } = await api('/api/clean', {
      categories: cats, files, root: S.root, deep: S.mode === 'deep',
      method, total: totalCount, minSize: 32 * 1024 * 1024,
    });
    pv.onCancel(async () => { await api('/api/job/cancel', { path: job }); });
    const st = await runJob(job, pv);
    pv.close();
    if (!st || st.status === 'error') { toast((st && st.error) || '清理失败', 'err'); return; }
    await showCleanResult(st.result || {}, method);
  } catch (e) { pv.close(); toast(e.message, 'err'); }
  refreshSystem();
}

async function showCleanResult(res, method) {
  const freed = res.freedBytes || 0;
  const body = h('div', null,
    h('div', { class: 'kv' },
      h('div', { class: 'k' }, '成功清理'), h('div', { class: 'v' }, fmtNum(res.deleted || 0) + ' 项'),
      h('div', { class: 'k' }, '释放空间'), h('div', { class: 'v', style: 'color:var(--danger)' }, fmtSize(freed)),
      h('div', { class: 'k' }, '失败'), h('div', { class: 'v' }, fmtNum(res.failed || 0) + ' 项'),
      h('div', { class: 'k' }, '跳过（保护/白名单）'), h('div', { class: 'v' }, fmtNum(res.skipped || 0) + ' 项'),
      h('div', { class: 'k' }, '耗时'), h('div', { class: 'v' }, fmtDur(res.elapsedMs || 0))));
  if (res.errors && res.errors.length) {
    const eb = h('div', { class: 'riskbox', style: 'max-height:140px' });
    res.errors.slice(0, 60).forEach((x) => eb.appendChild(h('div', { class: 'li mono t11' }, x)));
    body.appendChild(h('div', { class: 'mt8 dim t12' }, '部分项目未能删除（多为权限不足或被占用）：'));
    body.appendChild(eb);
  }
  openModal((box, close) => {
    box.appendChild(h('h3', null, '清理完成'));
    box.appendChild(body);
    if (method === 'recycle') {
      box.appendChild(h('div', { class: 'alert mt16' },
        h('b', null, '提示'), '：文件已进入回收站，仍占用磁盘空间。清空回收站才会真正释放。'));
    }
    const foot = h('div', { class: 'foot' });
    if (method === 'recycle') {
      foot.appendChild(h('button', { class: 'btn danger', onclick: async () => { close(); await emptyBinFlow(); } }, svg('i-trash'), '一键清空回收站'));
    }
    foot.appendChild(h('button', { class: 'btn primary', onclick: close }, '完成'));
    box.appendChild(foot);
  });
}

async function emptyBinFlow() {
  const rb = S.sys.recycle || { size: 0, count: 0 };
  const ok = await confirmBox({
    danger: true,
    title: '确认清空回收站？',
    desc: '回收站当前占用 <b>' + fmtSize(rb.size) + '</b>（' + fmtNum(rb.count) + ' 个项目）。<br><b>清空后这些文件将无法恢复</b>，且其中可能包含你在本次操作之外手动删除的文件。',
    needType: '清空',
    confirmText: '清空回收站',
  });
  if (!ok) return;
  try {
    const r = await api('/api/recyclebin/empty', {});
    toast('回收站已清空，释放 ' + fmtSize(r.freed), 'ok');
    S.sys.recycle = { size: r.remaining, count: r.items };
    renderView();
  } catch (e) { toast(e.message, 'err'); }
}

/* ── 残留扫描 ─────────────────────────────────────────────────────── */

const RES = { selCount: null, selBytes: null };

function renderResidue(c) {
  head(c, '残留扫描', '找出已卸载 / 已删除应用遗留的文件与无效快捷方式');
  if (!S.resDrive) S.resDrive = S.root;
  const chips = h('div', { class: 'drivechips' });
  (S.sys.volumes || []).forEach((v) => chips.appendChild(h('div', {
    class: 'chip' + (v.root === S.resDrive ? ' on' : ''),
    onclick: () => { S.resDrive = v.root; S.res = null; S.resSel.clear(); renderView(); },
  }, v.root.replace(/\\$/, '') + ' 盘')));

  c.appendChild(h('div', { class: 'card tight' },
    h('h3', null, '扫描位置', h('span', { class: 'hint' }, '应用数据、程序数据、开始菜单与注册表卸载记录')),
    chips,
    h('div', { class: 'flexrow' },
      h('button', { class: 'btn primary', onclick: () => startResidue() }, svg('i-radar'), '开始扫描残留'),
      h('span', { class: 'dim t12' }, '通过注册表已安装清单反向比对，识别无主的应用目录'))));

  if (!S.res) {
    c.appendChild(h('div', { class: 'card' }, h('div', { class: 'empty' }, h('div', { class: 'big' }, '◌'), h('p', null, '还没有扫描结果'))));
    return;
  }
  const r = S.res;
  RES.selCount = h('div', { class: 'sumN' }, '0 项');
  RES.selBytes = h('div', { class: 'sumB' }, '0 B');
  c.appendChild(h('div', { class: 'card tight' },
    h('div', { class: 'sumrow' },
      h('div', { class: 'sumleft' }, RES.selCount, RES.selBytes),
      h('div', { class: 'spacer' }),
      h('button', { class: 'btn', onclick: () => { (r.candidates || []).forEach((x) => { if (x.confidence === 'high') S.resSel.add(x.id); }); renderView(); } }, '选中高置信项'),
      h('button', { class: 'btn', onclick: () => { S.resSel.clear(); renderView(); } }, '清空选择'),
      h('button', { class: 'btn danger', onclick: () => residueCleanFlow() }, svg('i-trash'), '彻底清理'))));

  const list = h('div', { class: 'card' }, h('h3', null, '扫描结果',
    h('span', { class: 'hint' }, '已安装应用 ' + r.apps + ' 个 · 高置信 ' + r.highCount + ' · 中置信 ' + r.mediumCount + ' · 用时 ' + fmtDur(r.elapsedMs))));
  const cs = (r.candidates || []).slice().sort((a, b) => b.score - a.score || b.size - a.size);
  if (!cs.length) list.appendChild(h('div', { class: 'empty' }, h('p', null, '没有发现明显的卸载残留，系统很干净')));
  cs.forEach((x) => {
    const cb = h('input', { type: 'checkbox', class: 'check' });
    cb.checked = S.resSel.has(x.id);
    cb.onclick = () => { cb.checked ? S.resSel.add(x.id) : S.resSel.delete(x.id); syncResidue(); };
    list.appendChild(h('div', { class: 'row' }, cb,
      h('div', { class: 'meta' },
        h('div', { class: 't' }, x.name,
          ' ', h('span', { class: 'badge ' + (x.confidence === 'high' ? 'risky' : 'caution'), style: 'margin-left:6px' }, CONF_CN[x.confidence]),
          ' ', h('span', { class: 'badge p2' }, x.kindLabel)),
        h('div', { class: 's ellip', title: x.path }, x.path),
        h('div', { class: 'dim t11', style: 'margin-top:2px' }, (x.signals || []).join(' · '))),
      h('div', { class: 'sz right', style: 'min-width:96px;text-align:right' },
        h('div', { style: 'font-weight:620;font-variant-numeric:tabular-nums' }, x.size ? fmtSize(x.size) : '—'),
        h('div', { class: 'dim t11' }, x.files ? fmtNum(x.files) + ' 个文件' : '')),
      h('div', { class: 'acts' },
        h('button', { class: 'btn sm glass', title: '在资源管理器中打开', onclick: () => api('/api/reveal', { path: x.path }).catch((e) => toast(e.message, 'err')) }, '定位'))));
  });
  c.appendChild(list);
  syncResidue();
}

function syncResidue() {
  if (!S.res) return;
  if (RES.selCount) RES.selCount.textContent = S.resSel.size + ' 项';
  if (RES.selBytes) {
    const b = (S.res.candidates || []).filter((x) => S.resSel.has(x.id)).reduce((a, x) => a + (x.size || 0), 0);
    RES.selBytes.textContent = fmtSize(b);
  }
}

async function startResidue() {
  if (S.busy) return;
  S.busy = true; S.res = null; S.resSel.clear();
  const pv = progressModal('正在扫描卸载残留', '比对注册表已安装清单与磁盘目录',
    { a: '已检查', b: '已发现', c: '耗时' });
  try {
    const { job } = await api('/api/residue', { drive: (S.resDrive || S.root).replace(/\\$/, '') });
    pv.onCancel(async () => { await api('/api/job/cancel', { path: job }); });
    const st = await runJob(job, pv);
    if (!st || st.status === 'error') toast((st && st.error) || '扫描失败', 'err');
    else if (st.status === 'canceled') toast('已取消');
    else {
      S.res = st.result;
      (S.res.candidates || []).forEach((x) => { if (x.confidence === 'high') S.resSel.add(x.id); });
      toast('发现 ' + (S.res.candidates || []).length + ' 项疑似残留', 'ok');
    }
  } catch (e) { toast(e.message, 'err'); }
  pv.close(); S.busy = false; renderView();
}

async function residueCleanFlow() {
  const picked = (S.res.candidates || []).filter((x) => S.resSel.has(x.id));
  if (!picked.length) { toast('请先选择要清理的残留项', 'err'); return; }
  const bytes = picked.reduce((a, b) => a + (b.size || 0), 0);
  const hasRisky = picked.some((x) => x.risk === 'risky');

  const listBox = h('div', { class: 'riskbox' });
  picked.slice(0, 20).forEach((x) => listBox.appendChild(h('div', { class: 'li' },
    h('span', { class: 'n', title: x.path }, x.name + '  ·  ' + x.path),
    h('span', { class: 'dim nowrap' }, x.size ? fmtSize(x.size) : ''))));
  if (picked.length > 20) listBox.appendChild(h('div', { class: 'li dim' }, '… 以及另外 ' + (picked.length - 20) + ' 项'));

  const ok = await confirmBox({
    danger: true,
    title: '彻底清理残留？',
    desc: '将对 <b>' + picked.length + '</b> 项残留执行<b>永久删除</b>（不进入回收站），预计释放 <b>' + fmtSize(bytes) + '</b>。',
    extra: h('div', null,
      hasRisky ? h('div', { class: 'alert danger' }, h('b', null, '注意'), '：所选内容中包含位于程序目录的高风险项，删除后对应程序可能无法运行，请务必确认。') : null,
      listBox),
    requireCheck: true,
    checkText: '我已确认这些内容确实属于已卸载的应用',
    needType: 'DELETE',
    confirmText: '永久删除', cancelText: '取消',
  });
  if (!ok) return;

  const files = picked.filter((x) => x.kind !== 'registry' && x.path).map((x) => ({ path: x.path, size: x.size || 0 }));
  if (!files.length) { toast('所选项目仅剩注册表记录，卸载时会自动清理'); return; }
  const pv = progressModal('正在彻底清理残留', '永久删除所选文件与目录');
  try {
    const { job } = await api('/api/clean', { files, method: 'permanent' });
    pv.onCancel(async () => { await api('/api/job/cancel', { path: job }); });
    const st = await runJob(job, pv);
    pv.close();
    if (!st || st.status === 'error') { toast((st && st.error) || '清理失败', 'err'); return; }
    await showCleanResult(st.result || {}, 'permanent');
    S.res = null; S.resSel.clear();
  } catch (e) { pv.close(); toast(e.message, 'err'); }
  renderView();
}

/* ── 重复文件 ─────────────────────────────────────────────────────── */

const DUP = { sumCount: null, sumBytes: null, groups: new Map() };

function renderDupes(c) {
  head(c, '重复文件清理', '按大小 → 快速指纹 → 全量 SHA-256 三级比对，结果准确');

  const scopeSeg = segPick([['drive', '整个系统盘'], ['personal', '个人目录']], S.dupScope,
    (k) => { S.dupScope = k; });
  const minSeg = segPick(DUP_MIN_OPTS.map(([v, t]) => [String(v), t]), String(S.dupMin),
    (k) => {
      S.dupMin = Number(k);
      api('/api/config', { minDupSize: S.dupMin }).catch(() => {});
    });

  c.appendChild(h('div', { class: 'card tight' },
    h('h3', null, '扫描设置', h('span', { class: 'hint' }, '默认扫描整个系统盘，系统保护区与白名单自动跳过')),
    h('div', { class: 'flexrow', style: 'flex-wrap:wrap;gap:10px' },
      h('span', { class: 'dim t12' }, '扫描范围：'), scopeSeg.node,
      h('span', { class: 'dim t12', style: 'margin-left:8px' }, '最小体积：'), minSeg.node,
      h('span', { class: 'spacer' }),
      h('button', { class: 'btn primary', onclick: () => startDupes() }, svg('i-scan'), '开始查找重复文件')),
    h('div', { class: 'dim t11', style: 'margin-top:8px' },
      '跳过：Windows、Program Files、回收站、系统卷信息、UWP 应用数据、node_modules / .git 与白名单目录；每组默认保留修改时间最早的一个。'),
    S.dupScope === 'drive'
      ? h('div', { class: 'alert', style: 'margin-top:10px' },
          h('b', null, '提示'), '：整盘重复扫描需要逐个比对文件内容，文件多时可能需要几分钟，过程中可随时点「停止」。')
      : null));

  if (!S.dup) {
    c.appendChild(h('div', { class: 'card' }, h('div', { class: 'empty' }, h('div', { class: 'big' }, '◌'), h('p', null, '还没有扫描结果'))));
    return;
  }
  const r = S.dup;
  DUP.sumCount = h('div', { class: 'sumN' }, '0 个');
  DUP.sumBytes = h('div', { class: 'sumB' }, '0 B');

  c.appendChild(h('div', { class: 'card tight' },
    h('div', { class: 'sumrow' },
      h('div', { class: 'sumleft' }, DUP.sumCount, DUP.sumBytes),
      h('div', { class: 'spacer' }),
      h('button', { class: 'btn', onclick: () => { selectAllDupes(); syncDupAll(); } }, svg('i-check'), '一键选中所有重复文件'),
      h('button', { class: 'btn', onclick: () => { S.dupSel.clear(); syncDupAll(); } }, '取消全选'),
      h('button', { class: 'btn danger', onclick: () => dupeCleanFlow() }, svg('i-trash'), '删除选中'))));

  c.appendChild(h('div', { class: 'card tight' },
    h('div', { class: 'kv' },
      h('div', { class: 'k' }, '重复组'), h('div', { class: 'v' }, fmtNum(r.groupCount) + ' 组'),
      h('div', { class: 'k' }, '冗余文件'), h('div', { class: 'v' }, fmtNum(r.dupFiles) + ' 个'),
      h('div', { class: 'k' }, '可释放'), h('div', { class: 'v', style: 'color:var(--danger)' }, fmtSize(r.wastedBytes)),
      h('div', { class: 'k' }, '已扫描'), h('div', { class: 'v' }, fmtNum(r.scanned) + ' 个文件 · 用时 ' + fmtDur(r.elapsedMs)))));

  const list = h('div', { class: 'card' }, h('h3', null, '重复分组',
    h('span', { class: 'hint' }, '每组保留时间最早的一个（可用单选切换保留项）')));
  if (r.truncated) {
    list.appendChild(h('div', { class: 'alert', html:
      '<b>结果较多</b>：共 ' + fmtNum(r.groupCount) + ' 组，此处仅列出可释放空间最大的 ' +
      (r.groups || []).length + ' 组。「一键选中所有重复文件」只作用于已列出的分组。' }));
  }
  DUP.groups.clear();
  (r.groups || []).forEach((g, gi) => list.appendChild(dupeGroup(g, gi)));
  if (!(r.groups || []).length) list.appendChild(h('div', { class: 'empty' }, h('p', null, '没有发现重复文件')));
  c.appendChild(list);
  syncDupAll();
}

function dupeGroup(g, gi) {
  const open = S.openDups.has(gi);
  const body = h('div', { class: 'cbody' + (open ? '' : ' hidden') });
  const keeper = g.files.find((f) => f.keep) || g.files[0];
  const badge = h('span', { class: 'badge p2' }, '未选');
  const headEl = h('header', { onclick: () => toggleDupGroup(gi) },
    h('div', { class: 'ic' }, g.size >= (100 << 20) ? '大' : '双'),
    h('div', { class: 'nmrow' },
      h('div', { class: 'nm' }, keeper ? keeper.name : '重复文件'),
      h('div', { class: 'ds ellip', title: keeper ? keeper.path : '' }, keeper ? keeper.path : '')),
    h('div', { class: 'flexrow', style: 'gap:6px' }, badge),
    h('div', { class: 'sz' },
      h('b', null, '省 ' + fmtSize(g.wasted)),
      h('i', null, g.count + ' 个副本 · 单个 ' + fmtSize(g.size))),
    svg('i-caret', 'caret'));
  const node = h('div', { class: 'cat' + (open ? ' open' : '') }, headEl, body);

  const build = () => {
    const rows = h('div', { class: 'riskbox', style: 'max-height:240px' });
    g.files.forEach((f) => {
      const rd = h('input', { type: 'radio', name: 'keep' + gi, 'data-path': f.path });
      rd.style.accentColor = 'var(--accent)';
      rd.checked = f.keep;
      rd.onclick = () => setKeeper(gi, f.path);
      rows.appendChild(h('div', { class: 'li', style: 'align-items:center' }, rd,
        h('span', { class: 'n', title: f.path }, f.path),
        h('span', { class: 'dim nowrap t11' }, fmtTime(f.mod))));
    });
    body.appendChild(rows);
  };

  DUP.groups.set(gi, { g, node, body, badge, built: false, build });
  if (open) { build(); DUP.groups.get(gi).built = true; }
  return node;
}

function toggleDupGroup(gi) {
  const rec = DUP.groups.get(gi);
  if (!rec) return;
  const open = !S.openDups.has(gi);
  if (open) S.openDups.add(gi); else S.openDups.delete(gi);
  rec.node.classList.toggle('open', open);
  if (open) {
    if (!rec.built) { rec.build(); rec.built = true; }
    rec.body.classList.remove('hidden');
  } else rec.body.classList.add('hidden');
}

function setKeeper(gi, path) {
  const rec = DUP.groups.get(gi);
  if (!rec) return;
  rec.g.files.forEach((x) => {
    x.keep = (x.path === path);
    S.dupSel.delete(x.path);
  });
  syncDupGroup(gi);
  syncDupSummary();
}

function syncDupGroup(gi) {
  const rec = DUP.groups.get(gi);
  if (!rec) return;
  const selN = rec.g.files.filter((f) => S.dupSel.has(f.path)).length;
  rec.badge.textContent = selN ? ('已选 ' + selN) : '未选';
  rec.badge.className = 'badge ' + (selN ? 'danger' : 'p2');
}

function syncDupAll() {
  DUP.groups.forEach((_, gi) => syncDupGroup(gi));
  syncDupSummary();
}

function syncDupSummary() {
  if (!S.dup) return;
  const files = allDupFiles().filter((f) => S.dupSel.has(f.path));
  if (DUP.sumCount) DUP.sumCount.textContent = fmtNum(files.length) + ' 个';
  if (DUP.sumBytes) DUP.sumBytes.textContent = fmtSize(files.reduce((a, f) => a + f.size, 0));
}

function allDupFiles() {
  const out = [];
  (S.dup.groups || []).forEach((g) => g.files.forEach((f) => { if (!f.keep) out.push(f); }));
  return out;
}
function selectAllDupes() { allDupFiles().forEach((f) => S.dupSel.add(f.path)); }

async function startDupes() {
  if (S.busy) return;
  S.busy = true; S.dup = null; S.dupSel.clear(); S.openDups.clear();
  const pv = progressModal('正在查找重复文件', '先按体积分组，再做指纹与全量哈希比对',
    { a: '已比对', b: '候选文件', c: '耗时' });
  try {
    const { job } = await api('/api/dupes', {
      minSize: S.dupMin || 0,
      scope: S.dupScope,
    });
    pv.onCancel(async () => { await api('/api/job/cancel', { path: job }); });
    const st = await runJob(job, pv);
    if (!st || st.status === 'error') toast((st && st.error) || '扫描失败', 'err');
    else if (st.status === 'canceled') toast('已取消');
    else {
      S.dup = st.result;
      selectAllDupes();
      toast('找到 ' + S.dup.groupCount + ' 组重复文件，可释放 ' + fmtSize(S.dup.wastedBytes), 'ok');
    }
  } catch (e) { toast(e.message, 'err'); }
  pv.close(); S.busy = false; renderView();
}

async function dupeCleanFlow() {
  const files = allDupFiles().filter((f) => S.dupSel.has(f.path)).map((f) => ({ path: f.path, size: f.size }));
  if (!files.length) { toast('请先选择要删除的重复文件', 'err'); return; }
  const bytes = files.reduce((a, f) => a + f.size, 0);
  const listBox = h('div', { class: 'riskbox' });
  files.slice(0, 16).forEach((f) => listBox.appendChild(h('div', { class: 'li' }, h('span', { class: 'n', title: f.path }, f.path))));
  if (files.length > 16) listBox.appendChild(h('div', { class: 'li dim' }, '… 以及另外 ' + (files.length - 16) + ' 个文件'));
  const ok = await confirmBox({
    danger: true,
    title: '删除选中的重复文件？',
    desc: '共 <b>' + files.length + '</b> 个文件，预计释放 <b>' + fmtSize(bytes) + '</b>。<br>每组的「保留项」不在删除范围内。',
    extra: h('div', null,
      h('div', { class: 'alert' }, h('b', null, '二次确认'), '：请确认这些副本确实不再需要。'), listBox),
    requireCheck: true,
    checkText: '我确认保留项已正确选择，同意删除其余副本',
    confirmText: '删除重复文件',
  });
  if (!ok) return;

  const pv = progressModal('正在删除重复文件', '所选副本将移入回收站');
  try {
    const { job } = await api('/api/clean', { files, method: 'recycle' });
    pv.onCancel(async () => { await api('/api/job/cancel', { path: job }); });
    const st = await runJob(job, pv);
    pv.close();
    if (!st || st.status === 'error') { toast((st && st.error) || '删除失败', 'err'); return; }
    await showCleanResult(st.result || {}, 'recycle');
    S.dup = null; S.dupSel.clear();
  } catch (e) { pv.close(); toast(e.message, 'err'); }
  renderView();
  refreshSystem();
}

/* ── 白名单 ───────────────────────────────────────────────────────── */

function renderWhitelist(c) {
  head(c, '白名单', '被加入白名单的文件夹默认不参与任何扫描与清理',
    h('button', { class: 'btn primary', onclick: () => pickFolder() }, svg('i-plus'), '添加文件夹'));

  const wl = (S.cfg && S.cfg.whitelist) || [];
  const card = h('div', { class: 'card' }, h('h3', null, '已保护的文件夹', h('span', { class: 'hint' }, wl.length + ' 个')));
  if (!wl.length) {
    card.appendChild(h('div', { class: 'empty' }, h('div', { class: 'big' }, '◎'),
      h('p', null, '还没有添加白名单。建议把重要资料目录加入白名单，避免被误清理。')));
  }
  wl.forEach((w) => {
    card.appendChild(h('div', { class: 'row' },
      h('div', { class: 'ic' }, svg('i-folder')),
      h('div', { class: 'meta' }, h('div', { class: 't mono t12', style: 'word-break:break-all' }, w)),
      h('div', { class: 'acts' },
        h('button', { class: 'btn sm glass', onclick: () => api('/api/reveal', { path: w }).catch((e) => toast(e.message, 'err')) }, '定位'),
        h('button', {
          class: 'btn sm glass', onclick: async () => {
            if (!await confirmBox({ title: '移出白名单？', desc: '移出后该文件夹将重新参与扫描与清理。', confirmText: '移出' })) return;
            try { S.cfg = await api('/api/whitelist/remove', { dir: w }); toast('已移出白名单', 'ok'); renderView(); } catch (e) { toast(e.message, 'err'); }
          },
        }, '移出'))));
  });
  c.appendChild(card);
  c.appendChild(h('div', { class: 'card' }, h('h3', null, '安全机制说明'),
    h('div', { class: 'alert info' }, h('b', null, '保护范围'), '：系统保护区（Windows / System32 / WinSxS 等）、盘符根目录、回收站本体等路径在任何模式下都不会被清理。'),
    h('div', { class: 'alert info' }, h('b', null, '双重校验'), '：清理前会再次检查白名单与受保护路径，即使前端传入了异常清单也会被跳过。')));
}

function pickFolder(startPath) {
  let cur = startPath || '';
  return new Promise((resolve) => {
    openModal((box, close) => {
      box.classList.add('wide');
      box.appendChild(h('h3', null, '选择文件夹'));
      box.appendChild(h('p', null, '选中的文件夹及其全部子目录都会受到保护'));
      const crumbs = h('div', { class: 'crumbs' });
      const list = h('div', { class: 'dirlist' }, h('div', { class: 'empty' }, h('p', null, '加载中…')));
      const chosen = h('div', { class: 'dim mono t12', style: 'margin-top:10px;word-break:break-all;min-height:18px' }, '未选择');
      const load = async (p) => {
        try {
          const r = await api('/api/fs/list?path=' + encodeURIComponent(p || ''));
          cur = r.path;
          clear(crumbs);
          const parts = cur ? cur.split('\\').filter(Boolean) : [];
          crumbs.appendChild(h('span', { class: 'clk', onclick: () => load('') }, '此电脑'));
          let acc = '';
          parts.forEach((seg) => {
            acc = acc ? acc + '\\' + seg : seg + '\\';
            const target = acc;
            crumbs.appendChild(h('span', { class: 'dim' }, '›'));
            crumbs.appendChild(h('span', { class: 'clk', onclick: () => load(target) }, seg));
          });
          clear(list);
          if (r.parent && r.parent !== cur) {
            list.appendChild(h('div', { class: 'd', onclick: () => load(r.parent) }, svg('i-folder', 'up'), h('span', { class: 'up' }, '..  上一层')));
          }
          if (!r.dirs.length) list.appendChild(h('div', { class: 'empty' }, h('p', null, '该目录下没有子文件夹')));
          r.dirs.forEach((d) => list.appendChild(h('div', { class: 'd', onclick: () => load(d.path) }, svg('i-folder'), h('span', null, d.name))));
          chosen.textContent = cur || '选择一个磁盘以开始';
        } catch (e) { toast(e.message, 'err'); }
      };
      box.appendChild(crumbs);
      box.appendChild(list);
      box.appendChild(chosen);
      const foot = h('div', { class: 'foot' });
      foot.appendChild(h('button', { class: 'btn', onclick: () => { close(); resolve(null); } }, '取消'));
      foot.appendChild(h('button', {
        class: 'btn primary', onclick: async () => {
          if (!cur) { toast('请先进入并选择具体文件夹', 'err'); return; }
          try {
            S.cfg = await api('/api/whitelist/add', { dir: cur });
            toast('已加入白名单', 'ok');
            close(); resolve(cur);
            if (S.view === 'whitelist') renderView();
          } catch (e) { toast(e.message, 'err'); }
        },
      }, '将此文件夹加入白名单'));
      box.appendChild(foot);
      load(cur);
    });
  });
}

/* ── 关于 ─────────────────────────────────────────────────────────── */

function renderAbout(c) {
  head(c, '关于', 'Kill the C · 轻量级 Windows 系统盘清理工具');
  const info = S.sys;
  c.appendChild(h('div', { class: 'card' },
    h('div', { class: 'flexrow', style: 'gap:16px' },
      h('div', { style: 'width:64px;height:64px;flex:0 0 64px' }, svg('i-logo', 'logo-lg')),
      h('div', null,
        h('div', { class: 't18' }, 'Kill the C'),
        h('div', { class: 'dim t12' }, '版本 ' + info.version + ' · 单文件原生程序，安装包小于 20 MB'))),
    h('div', { class: 'kv mt16' },
      h('div', { class: 'k' }, '程序目录'), h('div', { class: 'v mono t11', style: 'word-break:break-all' }, info.installDir),
      h('div', { class: 'k' }, '数据目录'), h('div', { class: 'v mono t11', style: 'word-break:break-all' }, info.dataDir),
      h('div', { class: 'k' }, '运行权限'), h('div', { class: 'v' }, info.admin ? '管理员' : '普通用户'),
      h('div', { class: 'k' }, '界面引擎'), h('div', { class: 'v' }, 'Microsoft Edge WebView2'))));

  c.appendChild(h('div', { class: 'card' }, h('h3', null, '安全设计'),
    h('div', { class: 'alert info' }, h('b', null, '1. 扫描不改动'), '：扫描阶段只统计，绝不修改或删除任何文件。'),
    h('div', { class: 'alert info' }, h('b', null, '2. 分级风险'), '：每个文件都有风险等级与优先级（P0–P3），系统保护区永远不可勾选。'),
    h('div', { class: 'alert info' }, h('b', null, '3. 二次确认'), '：所有清理动作都会弹出确认框，列出数量、体积与清单；永久删除还需二次输入确认。'),
    h('div', { class: 'alert info' }, h('b', null, '4. 回收站兜底'), '：默认删除方式是移入回收站，可随时还原；清空回收站需再次确认。'),
    h('div', { class: 'alert info' }, h('b', null, '5. 白名单优先'), '：清理执行前会重新校验白名单与受保护路径，防止任何越权删除。')));

  c.appendChild(h('div', { class: 'card' }, h('h3', null, '操作建议'),
    h('p', { class: 'dim t12', style: 'margin:0' },
      '日常维护建议使用「轻度清理」，它会清理临时文件、缓存、日志等明确安全的垃圾；' +
      '当 C 盘空间紧张时再切换到「深度清理」，额外处理系统更新缓存、大文件与卸载残留，并逐项确认后再执行。')));
}

/* ── 卸载模式 ─────────────────────────────────────────────────────── */

function renderUninstall() {
  $('#shell').classList.add('hidden');
  const root = $('#uninstRoot');
  root.classList.remove('hidden');
  const chk = h('input', { type: 'checkbox', class: 'check' });
  const panel = h('div', { class: 'card panel' },
    h('div', { class: 'flexrow', style: 'gap:14px' },
      h('div', { style: 'width:56px;height:56px;flex:0 0 56px' }, svg('i-logo', 'logo-lg')),
      h('div', null,
        h('div', { class: 't18' }, '卸载 Kill the C'),
        h('div', { class: 'dim t12' }, '将从本机移除程序文件、快捷方式与卸载记录'))),
    h('div', { class: 'alert mt16' }, h('b', null, '说明'), '：卸载不会触碰你磁盘上的个人文件；仅移除本程序自身。'),
    h('label', { class: 'chkline', style: 'margin-top:14px' }, chk, h('span', null, '同时删除个人配置（白名单、界面偏好等）')),
    h('div', { class: 'flexrow mt16' },
      h('span', { class: 'spacer' }),
      h('button', {
        class: 'btn danger lg', onclick: async () => {
          if (!await confirmBox({ danger: true, title: '确认卸载？', desc: '卸载完成后程序将被移除，可随时从安装包重新安装。', confirmText: '确认卸载' })) return;
          runUninstall(chk.checked);
        },
      }, svg('i-trash'), '开始卸载')));
  clear(root).appendChild(panel);
}

async function runUninstall(removeConfig) {
  const root = $('#uninstRoot');
  clear(root).appendChild(h('div', { class: 'card panel' },
    h('div', { style: 'text-align:center' },
      h('div', { class: 't17', style: 'margin-bottom:10px' }, '正在卸载…'),
      h('div', { class: 'bar', style: 'max-width:320px;margin:0 auto' }, h('i', { style: 'width:0%' })),
      h('div', { class: 'dim t12', style: 'margin-top:12px' }, '正在移除程序文件与注册表记录'))));
  const bar = root.querySelector('.bar > i');
  let p = 6;
  const t = setInterval(() => { p = Math.min(92, p + Math.random() * 12); bar.style.width = p + '%'; }, 220);
  try {
    await api('/api/uninstall/run', { removeConfig });
  } catch (e) {
    clearInterval(t); toast(e.message, 'err'); return;
  }
  clearInterval(t);
  bar.style.width = '100%';
  setTimeout(() => {
    clear(root).appendChild(h('div', { class: 'card panel' },
      h('div', { style: 'text-align:center' },
        h('div', { class: 't17', style: 'margin-bottom:6px;color:var(--ok)' }, '卸载完成'),
        h('div', { class: 'dim t12' }, '感谢使用 Kill the C，窗口即将自动关闭。'))));
  }, 900);
}

/* ── 启动 ─────────────────────────────────────────────────────────── */

document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') {
    const ov = $('#overlay');
    if (ov.classList.contains('show') && !ov.querySelector('.prog-modal')) { ov.classList.remove('show'); clear(ov); }
  }
});

boot();
