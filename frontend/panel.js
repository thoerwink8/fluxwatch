// 托盘面板：顶部结论 → 目标列表 → 点一行展开示波器、连接、「复制诊断」「打开日志」。
// 只观测：这里没有任何切节点、改代理的按钮。
import { call, on, initPlatform, dur, bytes, esc, tone, stateText, layerChip, worstTarget } from "/common.js";

const RANGES = [
  { sec: 60, label: "60 秒", axis: ["−60s", "−30s", "现在"] },
  { sec: 900, label: "15 分钟", axis: ["−15:00", "−7:30", "现在"] },
  { sec: 3600, label: "1 小时", axis: ["−60:00", "−30:00", "现在"] },
];

const st = {
  snap: null,
  expanded: null,     // 展开的目标 name
  userPicked: false,  // 用户自己点过：不再自动跟着最严重的走
  range: 900,
  trace: [],          // 展开目标的样本
  msg: null,          // { name, text, err }
  info: { fake: false, version: "" },
  lastH: 0,
  shown: false,       // 面板此刻可见：收起后不重画、不拉轨迹（Go 那边也不再推快照）
};

const ALARM = ["stall", "suspect"];

/** 引擎连不上 mihomo（Clash 没开、手填的地址不对）那一条；没有为 undefined。 */
const mihomoErr = (s) => (s.sourceErr || []).find((e) => e.startsWith("mihomo："));

/** 去掉前缀，只留原因本身给人看。 */
function mihomoCause(e) {
  return e.replace(/^mihomo：/, "").replace(/^没找到 mihomo 控制接口：/, "");
}

const $ = (id) => document.getElementById(id);

// ── 顶部结论
function renderHead(s) {
  const head = $("head");
  const targets = s.targets || [];
  const how = $("howto");
  how.hidden = true;
  if (!targets.length) {
    // 引擎起不来（比如 targets.yaml 写坏了）：只有原因。「配置第 N 行有错：…」把前半句当标题
    const err = (s.sourceErr || [])[0];
    const m = err && /^(配置[^：]*有错)：(.*)$/.exec(err);
    head.innerHTML = `<span class="dot busy"></span>
      <div class="txt"><div class="big">${m ? esc(m[1]) : err ? "读不到数据" : "还没有监控目标"}</div>
      <div class="sub"><span>${esc(m ? "改好后会自动重新加载，不用重启" : err ? "不算报警，原因见下" : "从「发现更多目标」里挑一个加进来")}</span></div></div>`;
    if (err) {
      how.hidden = false;
      how.innerHTML = esc(m ? m[2] : err);
    }
    return;
  }
  const w = worstTarget(targets);
  const alerting = targets.filter((t) => ALARM.includes(t.state));
  if (alerting.length) {
    const word = { stall: "卡住", suspect: "慢" }[w.state];
    const others = alerting.length > 1 ? `<span class="chip plain">另 ${alerting.length - 1} 个在报警</span>` : "";
    head.innerHTML = `<span class="dot ${tone(w.state)}"></span>
      <div class="txt"><div class="big">${word} <span class="t">${dur(w.stuckFor)}</span></div>
      <div class="sub">${layerChip(w)}<span>${esc(w.label)}${w.node ? " · " + esc(w.node) : ""}</span>${others}</div></div>`;
    return;
  }
  const unknown = targets.filter((t) => t.state === "unknown");
  if (unknown.length) {
    // 读不到不是报警：灰点，写清是哪样读不到、怎么办
    const me = mihomoErr(s);
    head.innerHTML = `<span class="dot busy"></span>
      <div class="txt"><div class="big">${me ? "没连上 Clash" : "读不到数据"}</div>
      <div class="sub"><span>${unknown.length === targets.length ? "目标" : unknown.length + " 个目标"}先标「读不到」，不算报警</span></div></div>`;
    how.hidden = false;
    how.innerHTML = me
      ? `<div>${esc(mihomoCause(me))}</div>
         <div class="do"><b>怎么办</b>：打开 Clash Party 或 Clash Verge，连上后自动恢复；控制器不在 9090 / 9097、或要 secret，到设置里手填。</div>
         <button class="btn" data-act="settings">打开设置</button>`
      : esc(unknown[0].reason || "数据源读不到");
    return;
  }
  const busy = targets.filter((t) => t.state === "busy");
  const active = targets.filter((t) => t.state === "ok" || t.state === "thinking");
  if (busy.length && !active.length) {
    head.innerHTML = `<span class="dot busy"></span>
      <div class="txt"><div class="big">本地忙</div>
      <div class="sub"><span class="chip busy">D 不判定</span><span>${esc(busy[0].label)} 在跑本地工具</span></div></div>`;
    return;
  }
  head.innerHTML = `<span class="dot good"></span>
    <div class="txt"><div class="big">一切正常</div>
    <div class="sub"><span>${targets.length} 个目标 · 没有在报警</span></div></div>`;
}

// ── 目标列表
function rowMeta(t) {
  const apps = (t.apps || []).join(", ");
  return [t.node || "—", apps || "—"].join(" · ");
}

function renderTargets(s) {
  const box = $("targets");
  const targets = s.targets || [];
  if (!targets.length) {
    box.innerHTML = `<div class="h"><span>监控目标</span><span>0 个</span></div>
      <div class="empty">${(s.sourceErr || []).length ? "原因见上，好了之后这里列出目标。" : "还没有目标。"}</div>`;
    return;
  }
  let html = `<div class="h"><span>监控目标</span><span>${targets.length} 个</span></div>`;
  for (const t of targets) {
    const open = t.name === st.expanded;
    const sx = stateText(t);
    html += `<button class="trow${open ? " on" : ""}" data-name="${esc(t.name)}" aria-expanded="${open}" role="listitem">
      <span class="led" style="background:var(--${tone(t.state)})"></span>
      <div class="tn"><div class="n"><span>${esc(t.label)}</span></div><div class="m">${esc(rowMeta(t))}</div></div>
      <span class="tv ${sx.cls}">${esc(sx.text)}</span></button>`;
    if (open) html += detailHTML(t);
  }
  box.innerHTML = html;
  drawScope();
}

function detailHTML(t) {
  const r = RANGES.find((x) => x.sec === st.range) || RANGES[1];
  const seg = RANGES.map((x) => `<button data-range="${x.sec}" class="${x.sec === r.sec ? "on" : ""}">${x.label}</button>`).join("");
  const conns = (t.conns || []).slice().sort((a, b) => (b.idleFor || 0) - (a.idleFor || 0));
  const stalled = ALARM.includes(t.state);
  // 只列 4 条（按停得最久排，卡住的那几条在最前），其余收起：连接一多，按钮会被挤出面板
  const MAX_CONNS = 4;
  const shown = st.allConns === t.name ? conns : conns.slice(0, MAX_CONNS);
  const more = conns.length - shown.length;
  const connHTML = conns.length
    ? shown.map((c) => {
        const idle = c.idleFor || 0;
        const dead = stalled && idle >= (t.stuckFor || 0) - 1 && idle >= 10;
        const stText = idle < 2 ? "活动" : "停 " + dur(idle);
        const cls = idle < 2 ? "ok" : dead ? "dead" : "";
        return `<div class="conn">
          <span class="led" style="background:var(--${idle < 2 ? "good" : dead ? tone(t.state) : "busy"})"></span>
          <div style="min-width:0"><div class="nm">${esc(c.host)}</div><div class="sm">${esc(c.app || "—")} · ${esc(c.node || "—")}</div></div>
          <span class="st ${cls}">${esc(stText)}</span></div>`;
      }).join("") + (more > 0 ? `<button class="more" data-act="more">还有 ${more} 条，点开看全部</button>` : "")
    : `<div class="empty">此刻没有连接</div>`;
  const m = st.msg && st.msg.name === t.name ? st.msg : null;
  return `<div class="detail" data-detail="${esc(t.name)}">
    ${t.reason ? `<div class="reason">${layerChip(t)} ${esc(t.reason)}</div>` : ""}
    <div class="seg" role="tablist">${seg}</div>
    <div class="scope">
      <div class="lbl"><span>下行流量</span><span class="val" id="scopeval">—</span></div>
      <div class="frame"><svg id="scope" viewBox="0 0 300 50" preserveAspectRatio="none"></svg></div>
      <div class="axis"><span>${r.axis[0]}</span><span>${r.axis[1]}</span><span>${r.axis[2]}</span></div>
    </div>
    <div class="conns"><div class="h"><span>连接 ${conns.length}</span><span>软件 · 节点 / 停多久</span></div>${connHTML}</div>
    <div class="acts">
      <button class="btn" data-act="diag">复制诊断</button>
      <button class="btn" data-act="log">打开日志</button>
      <span class="msg${m && m.err ? " err" : ""}">${m ? esc(m.text) : ""}</span>
    </div></div>`;
}

// ── 示波器：静默段（卡住 / 慢的那一截）用状态色，虚线标起点
function drawScope() {
  const svg = $("scope");
  if (!svg || !st.snap) return;
  const t = (st.snap.targets || []).find((x) => x.name === st.expanded);
  if (!t) return;
  const W = 300, H = 50, PT = 6, PB = 5, plotH = H - PT - PB;
  const span = st.range;
  const buckets = Math.min(span, 300);
  const per = span / buckets;
  // 按时间对齐到右端，没有数据的地方留空
  const vals = new Array(buckets).fill(null);
  const samples = st.trace || [];
  let total = 0;
  const end = samples.length ? new Date(samples[samples.length - 1].at).getTime() : Date.now();
  for (const p of samples) {
    const age = (end - new Date(p.at).getTime()) / 1000;
    if (age < 0 || age >= span) continue;
    const i = buckets - 1 - Math.floor(age / per);
    vals[i] = Math.max(vals[i] ?? 0, p.down || 0);
    total += p.down || 0;
  }
  $("scopeval").textContent = bytes(total);
  const max = Math.max(1, ...vals.map((v) => v ?? 0));
  const x = (i) => (buckets === 1 ? W : (i / (buckets - 1)) * W);
  const y = (v) => PT + plotH - (v / max) * plotH;

  let grid = "";
  for (let g = 0; g <= 4; g++) {
    const gy = (PT + (plotH / 4) * g).toFixed(1);
    grid += `<line x1="0" y1="${gy}" x2="${W}" y2="${gy}" stroke="currentColor" stroke-width="0.5" opacity="0.13" vector-effect="non-scaling-stroke"/>`;
  }
  const first = vals.findIndex((v) => v !== null);
  if (first < 0) { svg.innerHTML = grid; return; }

  const stalled = t.state === "stall" || t.state === "suspect";
  const tailB = stalled ? Math.min(buckets - first, Math.ceil((t.stuckFor || 0) / per)) : 0;
  const okEnd = buckets - tailB; // [first, okEnd) 正常段
  const baseColor = t.state === "busy" || t.state === "idle" ? "var(--busy)" : "var(--accent)";
  const tailColor = `var(--${tone(t.state)})`;
  const tailFill = `var(--${tone(t.state)}-soft)`;
  const pts = (a, b) => { const out = []; for (let i = a; i < b; i++) out.push(`${x(i).toFixed(1)},${y(vals[i] ?? 0).toFixed(1)}`); return out; };

  let area = "", line = "", mark = "";
  if (okEnd - first > 1) {
    const p = pts(first, okEnd);
    area += `<path d="M${x(first).toFixed(1)},${H - PB} L${p.join(" L")} L${x(okEnd - 1).toFixed(1)},${H - PB} Z" fill="${baseColor}" opacity="0.16"/>`;
    line += `<polyline points="${p.join(" ")}" fill="none" stroke="${baseColor}" stroke-width="1.3" vector-effect="non-scaling-stroke" stroke-linejoin="round" stroke-linecap="round" opacity="0.85"/>`;
  }
  if (tailB > 0) {
    const a = Math.max(first, okEnd - 1);
    const p = pts(a, buckets);
    area += `<path d="M${x(a).toFixed(1)},${H - PB} L${p.join(" L")} L${W},${H - PB} Z" fill="${tailFill}"/>`;
    line += `<polyline points="${p.join(" ")}" fill="none" stroke="${tailColor}" stroke-width="1.6" vector-effect="non-scaling-stroke" stroke-linejoin="round" stroke-linecap="round"/>`;
    mark = `<line x1="${x(a).toFixed(1)}" y1="${PT}" x2="${x(a).toFixed(1)}" y2="${H - PB}" stroke="${tailColor}" stroke-width="0.8" stroke-dasharray="2 2" opacity="0.7" vector-effect="non-scaling-stroke"/>`;
  }
  const endColor = tailB > 0 ? tailColor : baseColor;
  const endDot = `<circle cx="${W}" cy="${y(vals[buckets - 1] ?? 0).toFixed(1)}" r="2.1" fill="${endColor}"/>`;
  svg.setAttribute("style", "color:" + endColor);
  svg.innerHTML = grid + area + line + mark + endDot;
}

async function refreshTrace() {
  if (!st.expanded || !st.shown) return;
  const name = st.expanded;
  try {
    const tr = await call("Trace", name, st.range);
    if (name === st.expanded) { st.trace = tr || []; drawScope(); }
  } catch (e) {
    st.trace = [];
  }
}

function renderFoot(s) {
  const w = worstTarget(s.targets);
  $("footr").textContent = w && w.todayCount ? `${w.label} 今天第 ${w.todayCount} 次` : "";
  $("footl").textContent = st.info.fake ? "演示数据（-fake）· 点一行展开" : "点一行展开该目标详情";
}

function render() {
  const s = st.snap;
  if (!s) return;
  const names = (s.targets || []).map((t) => t.name);
  // 默认展开最严重的那个；用户点过就听用户的，目标没了再回到默认
  if (st.expanded && !names.includes(st.expanded)) { st.expanded = null; st.userPicked = false; }
  if (!st.userPicked) {
    const w = worstTarget(s.targets);
    const next = w && ALARM.includes(w.state) ? w.name : null;
    if (next !== st.expanded) { st.expanded = next; st.trace = []; }
  }
  // 顶部已经讲了「没连上 Clash」时，横幅只列别的问题
  const me = (s.targets || []).some((t) => t.state === "unknown") && !(s.targets || []).some((t) => ALARM.includes(t.state)) ? mihomoErr(s) : null;
  const errs = (s.sourceErr || []).filter((e) => e !== me);
  const banner = $("srcerr");
  banner.hidden = !errs.length || !(s.targets || []).length;
  banner.textContent = errs.length ? "读不到：" + errs.join("；") : "";
  $("hookhint").hidden = !s.hookMissing || !(s.targets || []).length;
  renderHead(s);
  renderTargets(s);
  renderFoot(s);
  fit();
}

function fit() {
  requestAnimationFrame(() => {
    // 量内容自然高度（列表区展开后的全部），窗口按它定高；超过上限由 Go 截住、列表区内部滚动
    const pop = $("pop"), list = $("targets");
    const h = Math.ceil(pop.getBoundingClientRect().height + (list.scrollHeight - list.clientHeight));
    if (h && h !== st.lastH) {
      st.lastH = h;
      call("FitPanel", h).catch(() => {});
    }
  });
}

// ── 交互
$("targets").addEventListener("click", async (e) => {
  const row = e.target.closest(".trow");
  if (row) {
    const name = row.dataset.name;
    st.expanded = st.expanded === name ? null : name;
    st.allConns = null;
    st.userPicked = true;
    st.trace = [];
    render();
    refreshTrace();
    return;
  }
  const rb = e.target.closest("[data-range]");
  if (rb) {
    st.range = Number(rb.dataset.range);
    render();
    refreshTrace();
    return;
  }
  const act = e.target.closest("[data-act]");
  if (!act || !st.expanded) return;
  const name = st.expanded;
  if (act.dataset.act === "more") {
    st.allConns = name;
    render();
    return;
  }
  try {
    if (act.dataset.act === "diag") {
      await call("CopyDiagnose", name);
      st.msg = { name, text: "已复制到剪贴板" };
    } else {
      const p = await call("OpenLog", name);
      st.msg = { name, text: "已打开 " + String(p).split(/[\\/]/).pop() };
    }
  } catch (err) {
    st.msg = { name, text: String(err && err.message ? err.message : err), err: true };
  }
  render();
  setTimeout(() => { if (st.msg && st.msg.name === name) { st.msg = null; render(); } }, 3000);
});

$("howto").addEventListener("click", (e) => { if (e.target.closest("[data-act=settings]")) call("OpenSettings"); });
$("discover").addEventListener("click", () => call("OpenDiscover"));
$("settings").addEventListener("click", () => call("OpenSettings"));
$("hookhow").addEventListener("click", () => call("OpenSettings"));
document.addEventListener("keydown", (e) => { if (e.key === "Escape") call("HidePanel"); });

// Go 只在面板可见时每秒推；打开时先补推一份再发 panel-shown。收起后这里什么都不做。
on("fluxwatch:snapshot", (s) => { st.snap = s; if (st.shown) { render(); refreshTrace(); } });
on("fluxwatch:panel-shown", () => { st.shown = true; st.userPicked = false; render(); refreshTrace(); });
on("fluxwatch:panel-hidden", () => { st.shown = false; });

(async () => {
  st.info = await initPlatform();
  // 开场取一份把窗口高度量好（面板还收着，不拉轨迹）
  try { st.snap = await call("Snapshot"); render(); } catch (e) { /* 等推送 */ }
})();
