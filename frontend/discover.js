// 发现窗：列此刻正在跑、按域名聚合的连接；选中给出建议通配符，「加入监控」写进配置。
import { call, on, initPlatform, bytesShort, esc } from "/common.js";

const $ = (id) => document.getElementById(id);
const st = { rows: [], sel: null, err: "", busy: false };

function labelFor(host) {
  // 取主域名那一段，首字母大写：relay.mirasim.ai → Mirasim
  const parts = host.split(".");
  const core = parts.length >= 2 ? parts[parts.length - 2] : host;
  return core.charAt(0).toUpperCase() + core.slice(1);
}

function render() {
  const q = $("q").value.trim().toLowerCase();
  const rows = st.rows.filter((d) => !q || [d.host, d.node, ...(d.apps || [])].join(" ").toLowerCase().includes(q));
  const conns = st.rows.reduce((n, d) => n + (d.connN || 0), 0);
  $("sum").textContent = st.err ? "读不到：" + st.err : `${st.rows.length} 个域名 · ${conns} 条活动连接 · 按下行排序`;
  let html = `<div class="h"><span>正在跑</span><span>下行</span></div>`;
  if (!rows.length) html += `<div class="empty">${st.err ? esc(st.err) : q ? "没有匹配的域名" : "此刻没有活动连接"}</div>`;
  for (const d of rows) {
    const on = st.sel && st.sel.host === d.host;
    const direct = (d.node || "").toUpperCase() === "DIRECT";
    const chip = d.watched ? `<span class="chip good">已盯</span>` : direct ? `<span class="chip plain">直连</span>` : "";
    const led = d.watched ? "var(--accent)" : direct ? "var(--busy)" : "var(--fg-3)";
    const apps = (d.apps || []).join(", ") || "—";
    html += `<button class="trow${on ? " on" : ""}" data-host="${esc(d.host)}" role="option" aria-selected="${on}">
      <span class="led" style="background:${led}"></span>
      <div class="tn"><div class="n"><span>${esc(d.host)}</span>${chip}</div>
      <div class="m">${d.connN} 条连接 · ${esc(apps)} · ${esc(d.node || "—")}</div></div>
      <span class="tv${d.watched ? " ok" : ""}">${bytesShort(d.down)}</span></button>`;
  }
  $("list").innerHTML = html;
}

function select(host) {
  const d = st.rows.find((x) => x.host === host);
  st.sel = d || null;
  $("pick").hidden = !d;
  if (d) {
    $("lead").textContent = d.watched ? `${d.host} 已经在盯了` : `选中 ${d.host}（建议含全部子域）`;
    $("label").value = labelFor(d.host);
    $("pattern").value = d.suggest || d.host;
    // 把这一行的软件带上：同一个域名，浏览器打开的网页不算进桌面客户端的目标
    $("apps").value = (d.apps || []).join(", ");
    $("add").disabled = d.watched;
    $("msg").textContent = "";
    $("msg").className = "msg";
  }
  render();
}

async function load() {
  try {
    st.rows = (await call("Discover")) || [];
    st.err = "";
    if (st.sel) st.sel = st.rows.find((x) => x.host === st.sel.host) || st.sel;
  } catch (e) {
    st.rows = [];
    st.err = String(e && e.message ? e.message : e);
  }
  render();
}

$("list").addEventListener("click", (e) => {
  const row = e.target.closest(".trow");
  if (row) select(row.dataset.host);
});
$("q").addEventListener("input", render);
$("cancel").addEventListener("click", () => select(null));
$("add").addEventListener("click", async () => {
  if (st.busy) return;
  st.busy = true;
  $("add").disabled = true;
  try {
    const apps = $("apps").value.split(/[,，]/).map((s) => s.trim()).filter(Boolean);
    await call("AddTarget", $("label").value, $("pattern").value, apps);
    $("msg").className = "msg";
    const only = apps.length ? ` · 只算 ${apps.join(", ")}` : "";
    $("msg").textContent = `已加入：${$("label").value}（${$("pattern").value}${only}）`;
    await load();
  } catch (e) {
    $("msg").className = "msg err";
    $("msg").textContent = String(e && e.message ? e.message : e);
    $("add").disabled = false;
  } finally {
    st.busy = false;
  }
});

// 截图模式：选中第一个还没盯的域名，好拍到「加入监控」那一栏
on("fluxwatch:shots-select", () => {
  const d = st.rows.find((x) => !x.watched);
  if (d) select(d.host);
});

initPlatform();
load();
setInterval(() => { if (!document.hidden) load(); }, 2000);
