// 三个窗口共用：调 Go、主题、格式化、状态文案。纯 ES 模块，不上框架。
import { Call, Events } from "/wails/runtime.js";

const PKG = "github.com/thoerwink8/fluxwatch/internal/app.Bridge.";

/** 调 Bridge 上的方法。 */
export function call(method, ...args) {
  return Call.ByName(PKG + method, ...args);
}

export function on(name, fn) {
  return Events.On(name, (ev) => fn(ev.data));
}

// ── 主题：跟随系统。CSS 已按 prefers-color-scheme 走；这里再把 data-theme 设成系统当前值，
//    系统外观变化时（媒体查询事件、Go 推的 fluxwatch:theme）都刷新一次。
const mq = window.matchMedia("(prefers-color-scheme: dark)");
function applyTheme(dark) {
  document.documentElement.dataset.theme = dark ? "dark" : "light";
}
applyTheme(mq.matches);
mq.addEventListener("change", (e) => applyTheme(e.matches));
on("fluxwatch:theme", (dark) => applyTheme(!!dark));

export async function initPlatform() {
  try {
    const info = await call("Info");
    document.documentElement.classList.add("platform-" + info.platform);
    return info;
  } catch (e) {
    return { fake: false, version: "", platform: "" };
  }
}

// ── 格式化
export function dur(sec) {
  sec = Math.max(0, Math.round(sec || 0));
  if (sec < 60) return sec + "s";
  const m = Math.floor(sec / 60), s = sec % 60;
  if (m < 60) return m + "m" + (s ? String(s).padStart(2, "0") + "s" : "");
  const h = Math.floor(m / 60);
  return h + "h" + String(m % 60).padStart(2, "0") + "m";
}

export function bytes(n) {
  n = n || 0;
  if (n < 1000) return n + " B";
  if (n < 1e6) return (n / 1e3).toFixed(n < 1e4 ? 1 : 0) + " KB";
  if (n < 1e9) return (n / 1e6).toFixed(1) + " MB";
  return (n / 1e9).toFixed(2) + " GB";
}

export function bytesShort(n) {
  n = n || 0;
  if (n < 1000) return String(n);
  if (n < 1e6) return (n / 1e3).toFixed(n < 1e4 ? 1 : 0) + "K";
  return (n / 1e6).toFixed(1) + "M";
}

export function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
}

// ── 状态
// 界面里「读不到」排在「慢」后面、用灰色：Clash 没开时不像在报警，也不盖住别的目标的「慢」。
export const SEVERITY = { stall: 4, suspect: 3, unknown: 2, ok: 1, thinking: 1, busy: 0, idle: 0 };

/** 状态 → 语义色名（dot / chip / led 的 class）。 */
export function tone(state) {
  switch (state) {
    case "stall": return "crit";
    case "suspect": return "warn";
    case "ok": case "thinking": return "good";
    default: return "busy";
  }
}

/** 行尾的状态文字和样式。 */
export function stateText(t) {
  switch (t.state) {
    case "stall": return { text: "卡住 " + dur(t.stuckFor), cls: "dead" };
    case "suspect": return { text: "慢 " + dur(t.stuckFor), cls: "slow" };
    case "unknown": return { text: "读不到", cls: "idle" };
    case "busy": return { text: "本地忙", cls: "idle" };
    case "thinking": return { text: "思考中 " + dur(t.stuckFor), cls: "" };
    case "ok": return { text: "正常", cls: "ok" };
    default: return { text: "空闲", cls: "idle" };
  }
}

export const LAYER = { A: "A 线路停", B: "B 上游卡", C: "C 账号配额" };

/** 层级徽章：B 层能从原因里抠出状态码时写成「B 上游 503」。 */
export function layerChip(t) {
  if (!t.layer) return "";
  let label = LAYER[t.layer] || t.layer;
  if (t.layer === "B") {
    const m = /\b(429|5\d\d)\b/.exec(t.reason || "");
    if (m) label = "B 上游 " + m[1];
  }
  const cls = t.layer === "C" ? "warn" : "crit";
  return `<span class="chip ${cls}">${esc(label)}</span>`;
}

/** 按严重度、再按卡住时长排出最该看的目标。 */
export function worstTarget(targets) {
  let best = null;
  for (const t of targets || []) {
    if (!best) { best = t; continue; }
    const a = SEVERITY[t.state] ?? 0, b = SEVERITY[best.state] ?? 0;
    if (a > b || (a === b && (t.stuckFor || 0) > (best.stuckFor || 0))) best = t;
  }
  return best;
}
