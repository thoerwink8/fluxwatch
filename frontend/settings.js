// 设置窗：改了就存到 ~/.fluxwatch/settings.json；存不进去照实显示原因、界面回到文件里的值。
import { call, initPlatform, esc } from "/common.js";

const $ = (id) => document.getElementById(id);
let cur = null;
let saveTimer = 0;

function fill(v) {
  cur = { ...v.settings };
  $("where").textContent = `存在 ${v.path} · 数据：${v.engine}`;
  $("loaderr").hidden = !v.loadErr;
  $("loaderr").textContent = v.loadErr ? "设置文件读不了，先用默认值（原文件没动）：" + v.loadErr : "";
  $("launchAtLogin").checked = !!cur.launchAtLogin;
  $("autostartD").textContent = v.autostartErr
    ? "读不到系统里的登记状态：" + v.autostartErr
    : `登录后自动出现在菜单栏 · 系统里现在${v.autostartOn ? "已登记" : "未登记"}`;
  $("notify").checked = !!cur.notify;
  const hk = v.hook || {};
  $("hookChip").className = "chip " + (hk.installed ? "good" : "warn");
  $("hookChip").textContent = hk.installed ? "已装" : "未装";
  $("hookD").innerHTML = esc(hk.detail || "—") +
    (hk.lastSeen ? ` · 最近一次写入 <code>${esc(hk.lastSeen)}</code>` : "") +
    (hk.installed ? "" : "<br>装法：在 Claude Code 的用户设置里给 PreToolUse 和 PostToolUse 各挂一条往 <code>~/.fluxwatch/hook.jsonl</code> 追加一行的命令。");
  $("mihomoAddr").value = cur.mihomoAddr || "";
  $("mihomoSecret").value = cur.mihomoSecret || "";
  $("suspectSec").value = cur.suspectSec;
  $("stallSec").value = cur.stallSec;
  syncMode();
}

function syncMode() {
  for (const b of $("mihomoMode").querySelectorAll("button")) {
    const on = b.dataset.mode === cur.mihomoMode;
    b.classList.toggle("on", on);
    b.setAttribute("aria-checked", on);
  }
  $("manual").hidden = cur.mihomoMode !== "manual";
}

function status(text, cls) {
  $("status").className = "status" + (cls ? " " + cls : "");
  $("status").textContent = text;
}

async function save(next) {
  try {
    await call("SaveSettings", next);
    cur = next;
    status("已保存", "ok");
  } catch (e) {
    status("没存上：" + String(e && e.message ? e.message : e), "err");
    // 开机启动这类开关存失败时回到实际值
    fill(await call("GetSettings"));
  }
}

function collect() {
  return {
    ...cur,
    launchAtLogin: $("launchAtLogin").checked,
    notify: $("notify").checked,
    mihomoAddr: $("mihomoAddr").value.trim(),
    mihomoSecret: $("mihomoSecret").value,
    suspectSec: parseInt($("suspectSec").value, 10) || 0,
    stallSec: parseInt($("stallSec").value, 10) || 0,
  };
}

// 开关立即存；输入框停顿 600ms 后存
for (const id of ["launchAtLogin", "notify"]) $(id).addEventListener("change", () => save(collect()));
for (const id of ["mihomoAddr", "mihomoSecret", "suspectSec", "stallSec"]) {
  $(id).addEventListener("input", () => {
    clearTimeout(saveTimer);
    status("…");
    saveTimer = setTimeout(() => save(collect()), 600);
  });
}
$("mihomoMode").addEventListener("click", (e) => {
  const b = e.target.closest("[data-mode]");
  if (!b) return;
  cur.mihomoMode = b.dataset.mode;
  syncMode();
  if (cur.mihomoMode === "auto" || $("mihomoAddr").value.trim()) save(collect());
  else status("填上地址后保存");
});

initPlatform();
call("GetSettings").then(fill).catch((e) => status("读不到设置：" + e, "err"));
window.addEventListener("focus", () => call("GetSettings").then((v) => { $("hookD") && fill(v); }).catch(() => {}));
