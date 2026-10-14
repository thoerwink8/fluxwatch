// pnpm e2e：端到端跑真引擎（真的 fluxwatch-cli / 托盘应用），打印每一步通过还是没通过。
//
// 每一步都在临时 HOME 里跑（~/.fluxwatch 是临时的），不碰本机的 targets.yaml、账本、hook.jsonl，
// 也不改 ~/.claude/settings.json：要 hook 的步骤用 `claude --settings <临时文件>` 只给那一个会话挂。
//
//   fake-busy    假 mihomo + 假 hook 事件（由名叫 e2e 的父进程调 hook，对上目标 name）：请求发出没回包时工具在跑 → 一直 busy，不升 suspect / stall；工具结束接着计时
//   fake-stall   假 mihomo、没有 hook：请求发出没回包 → suspect ≤ 30s、stall ≤ 90s；回包后恢复 OK
//   fake-scope   假 mihomo、两个目标都发了请求没回包：由名叫 reclaude 的父进程调 hook（真上溯父进程链）→ 只有 reclaude busy，
//                mirasim 照报 suspect / stall；换成名叫 Mirasim 的父进程 → 只有 mirasim busy
//   hook-cost    fluxwatch-cli hook 一次多花多少时间（跑 20 次取平均和最大）
//   real-busy    真 Claude 会话（经 reclaude；claude 由一个名叫 reclaude 的父进程起，hook 上溯认成 reclaude）跑 70 秒的命令（perl sleep：本机的会话钩子拦前台裸 sleep），会话级挂 hook：sleep 期间 reclaude 不升 suspect / stall
//   real-nohook  同上但不挂 hook（对照组）：记下 sleep 期间的状态，看没有 hook 会不会误报
//   tray-notify  真托盘应用 + 假 mihomo 卡住：进 STALL 时系统通知弹出来，截右上角到 _tmp/e2e/
//
// 开关：--skip-claude 跳过 real-*；--skip-tray 跳过 tray-notify；--only=a,b 只跑这几步。
// CI（环境变量 CI 非空）只跑 fake-* 和 hook-cost。
import { spawn, spawnSync } from 'node:child_process';
import { createServer } from 'node:http';
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { join } from 'node:path';
import { build, root } from './lib.mjs';

const args = process.argv.slice(2);
const flag = (f) => args.includes(f);
const only = (args.find((a) => a.startsWith('--only=')) || '').slice(7).split(',').filter(Boolean);
const ci = !!process.env.CI;
const mac = process.platform === 'darwin';
const outDir = join(root, '_tmp', 'e2e');
mkdirSync(outDir, { recursive: true });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const results = [];

// ---------- 构建 ----------
const binDir = join(outDir, 'bin');
const cli = build('fluxwatch-cli', binDir);
if (!cli) {
  console.error('fluxwatch-cli 构建失败');
  process.exit(1);
}

// ---------- 假 mihomo ----------
function fakeMihomo() {
  const conns = new Map();
  const srv = createServer((req, res) => {
    if (req.url === '/version') return res.end('{"meta":true,"version":"v1.19.14-e2e"}');
    if (req.url.startsWith('/connections')) {
      return res.end(JSON.stringify({ connections: [...conns.values()] }));
    }
    res.statusCode = 404;
    res.end();
  });
  return new Promise((resolve) => {
    srv.listen(0, '127.0.0.1', () => {
      resolve({
        addr: `127.0.0.1:${srv.address().port}`,
        add(id, host, up = 0, down = 0) {
          conns.set(id, {
            id, upload: up, download: down, start: new Date().toISOString(), chains: ['E2E-NODE', 'Proxy'],
            metadata: { host, sourcePort: String(40000 + conns.size), destinationPort: '443' },
          });
        },
        bump(id, up, down) {
          const c = conns.get(id);
          c.upload += up;
          c.download += down;
        },
        close: () => srv.close(),
      });
    });
  });
}

// ---------- 临时 HOME ----------
function tempHome(name, targetsYAML) {
  const h = mkdtempSync(join(tmpdir(), `fluxwatch-e2e-${name}-`));
  mkdirSync(join(h, '.fluxwatch'), { recursive: true });
  writeFileSync(join(h, '.fluxwatch', 'targets.yaml'), targetsYAML);
  return h;
}
const homeEnv = (h) => ({ ...process.env, HOME: h, USERPROFILE: h });
const fakeTargets = (addr, label = 'E2E') => `defaults: { suspect: 10s, stall: 30s }
mihomo: { controller: ${addr} }
targets:
  - name: e2e
    label: ${label}
    sources:
      - { type: mihomo, hosts: ["*.example.com"] }
`;

// fluxwatch-cli watch：收集每个目标的状态变化（收到那一行的时刻）。
function watch(home) {
  const p = spawn(cli, ['watch'], { env: homeEnv(home), stdio: ['ignore', 'pipe', 'pipe'] });
  const events = [];
  let buf = '';
  const log = [];
  const onData = (d) => {
    buf += d;
    let i;
    while ((i = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, i);
      buf = buf.slice(i + 1);
      log.push(line);
      const m = line.match(/^\S+\s+(\S+)\s+(\S+)→(\S+)\s/);
      if (m) events.push({ at: Date.now(), target: m[1], from: m[2], to: m[3], line });
    }
  };
  p.stdout.setEncoding('utf8').on('data', onData);
  p.stderr.setEncoding('utf8').on('data', (d) => log.push('stderr: ' + d.trim()));
  return {
    events, log,
    states: (target, from = 0, to = Infinity) => events.filter((e) => e.target === target && e.at >= from && e.at <= to),
    // 某个时刻目标处在什么状态（最后一次变化）
    stateAt: (target, t) => [...events].reverse().find((e) => e.target === target && e.at <= t)?.to,
    stop: () => new Promise((r) => { p.once('exit', r); p.kill('SIGINT'); setTimeout(() => p.kill('SIGKILL'), 3000); }),
  };
}

function hookEvent(home, event, session = 'e2e') {
  const r = spawnSync(cli, ['hook'], {
    env: homeEnv(home),
    input: JSON.stringify({ session_id: session, hook_event_name: event, tool_name: 'Bash', tool_input: { command: 'sleep 70' } }),
  });
  if (r.status !== 0) throw new Error(`hook 失败：${r.stderr}`);
}

// appExe 把 node 链接（Windows 上复制）成 <home>/bin-<app>/<app>：用它跑的脚本，进程名就是 app。
// hook 顺父进程链往上找发起调用的软件，经它起的子进程会把 app 写进 hook.jsonl。
function appExe(home, app) {
  const dir = join(home, 'bin-' + app);
  const exe = join(dir, process.platform === 'win32' ? `${app}.exe` : app);
  if (!existsSync(exe)) {
    mkdirSync(dir, { recursive: true });
    if (process.platform === 'win32') copyFileSync(process.execPath, exe);
    else symlinkSync(process.execPath, exe);
  }
  return exe;
}

// hookVia 让名叫 app 的父进程去调 fluxwatch-cli hook。
function hookVia(home, app, event, session) {
  const input = JSON.stringify({ session_id: session, hook_event_name: event, tool_name: 'Bash' });
  const script = `const r=require('child_process').spawnSync(${JSON.stringify(cli)},['hook'],{input:${JSON.stringify(input)},stdio:['pipe','inherit','inherit']});process.exit(r.status??1)`;
  const r = spawnSync(appExe(home, app), ['-e', script], { env: homeEnv(home), encoding: 'utf8' });
  if (r.status !== 0) throw new Error(`经 ${app} 调 hook 失败：${r.stderr || r.error}`);
}

function record(name, ok, detail) {
  results.push({ name, ok, detail });
  console.log(`${ok === null ? '跳过' : ok ? '通过' : '没通过'}  ${name}  ${detail}`);
}
const want = (name) => (only.length ? only.includes(name) : true);
const fmt = (evs, t0) => evs.map((e) => `${((e.at - t0) / 1000).toFixed(0)}s ${e.from}→${e.to}`).join(', ') || '（没有变化）';

// ---------- fake-busy ----------
async function fakeBusy() {
  const mh = await fakeMihomo();
  const home = tempHome('busy', fakeTargets(mh.addr));
  const w = watch(home);
  try {
    await sleep(3000); // 引擎建好连接基线
    mh.add('c1', 'api.example.com', 4000, 0); // 发请求，不回包
    await sleep(2000);
    hookVia(home, 'e2e', 'PreToolUse', 'e2e'); // 父进程名对上目标 name，本地忙才压这个目标
    const t0 = Date.now();
    await sleep(40000);
    const during = w.states('e2e', t0);
    const busy = w.stateAt('e2e', Date.now()) === 'busy';
    const bad = during.filter((e) => e.to === 'suspect' || e.to === 'stall');
    hookVia(home, 'e2e', 'PostToolUse', 'e2e');
    const t1 = Date.now();
    await sleep(15000);
    const after = w.states('e2e', t1);
    const resumed = after.some((e) => e.to === 'suspect');
    record('fake-busy', busy && bad.length === 0 && resumed,
      `工具在跑 40s：${fmt(during, t0)}；此刻 ${w.stateAt('e2e', t0 + 40000)}；工具结束后 15s 内：${fmt(after, t1)}（要接着计时进 suspect）`);
  } finally {
    await w.stop();
    mh.close();
    writeFileSync(join(outDir, 'fake-busy.log'), w.log.join('\n'));
    rmSync(home, { recursive: true, force: true });
  }
}

// ---------- fake-stall ----------
async function fakeStall() {
  const mh = await fakeMihomo();
  const home = tempHome('stall', fakeTargets(mh.addr));
  const w = watch(home);
  try {
    await sleep(3000);
    mh.add('c1', 'api.example.com', 4000, 0);
    const t0 = Date.now();
    let sus, st;
    while (Date.now() - t0 < 95000 && !st) {
      await sleep(500);
      sus ??= w.states('e2e', t0).find((e) => e.to === 'suspect');
      st ??= w.states('e2e', t0).find((e) => e.to === 'stall');
    }
    mh.bump('c1', 0, 5000); // 回包
    await sleep(3000);
    const recovered = w.stateAt('e2e', Date.now()) === 'ok';
    const s = sus ? (sus.at - t0) / 1000 : Infinity;
    const t = st ? (st.at - t0) / 1000 : Infinity;
    record('fake-stall', s <= 30 && t <= 90 && recovered,
      `suspect 在 ${s.toFixed(1)}s（要 ≤30s）、stall 在 ${t.toFixed(1)}s（要 ≤90s）、回包后 ${w.stateAt('e2e', Date.now())}；${fmt(w.states('e2e', t0), t0)}`);
  } finally {
    await w.stop();
    mh.close();
    writeFileSync(join(outDir, 'fake-stall.log'), w.log.join('\n'));
    rmSync(home, { recursive: true, force: true });
  }
}

// ---------- fake-scope ----------
async function fakeScope() {
  const mh = await fakeMihomo();
  const home = tempHome('scope', `defaults: { suspect: 10s, stall: 30s }
mihomo: { controller: ${mh.addr} }
targets:
  - name: reclaude
    label: E2E reclaude
    sources:
      - { type: mihomo, hosts: ["*.reclaude.example.com"] }
  - name: mirasim
    label: E2E Mirasim
    sources:
      - { type: mihomo, hosts: ["*.mirasim.example.com"] }
`);
  const w = watch(home);
  try {
    await sleep(3000);
    mh.add('r1', 'api.reclaude.example.com', 4000, 0); // 两边都发请求，不回包
    mh.add('m1', 'api.mirasim.example.com', 4000, 0);
    await sleep(2000);
    hookVia(home, 'reclaude', 'PreToolUse', 'rc'); // reclaude 会话在跑工具
    const t0 = Date.now();
    await sleep(40000);
    const rcBusy = w.states('reclaude', t0).every((e) => e.to === 'busy') && w.stateAt('reclaude', Date.now()) === 'busy';
    const msEv = w.states('mirasim', t0);
    const msOK = msEv.some((e) => e.to === 'suspect') && msEv.some((e) => e.to === 'stall') && !msEv.some((e) => e.to === 'busy');
    hookVia(home, 'reclaude', 'PostToolUse', 'rc');
    hookVia(home, 'Mirasim', 'PreToolUse', 'ms'); // 换成 Mirasim 会话在跑工具
    const t1 = Date.now();
    await sleep(8000);
    const msBusy = w.stateAt('mirasim', Date.now()) === 'busy';
    const rcNow = w.stateAt('reclaude', Date.now());
    hookVia(home, 'Mirasim', 'PostToolUse', 'ms');
    const apps = readFileSync(join(home, '.fluxwatch', 'hook.jsonl'), 'utf8').trim().split('\n').map((l) => JSON.parse(l).app);
    const appsOK = apps.join(',') === 'reclaude,reclaude,Mirasim,Mirasim';
    record('fake-scope', rcBusy && msOK && msBusy && rcNow !== 'busy' && appsOK,
      `hook.jsonl 的 app：${apps.join(',')}（要 reclaude,reclaude,Mirasim,Mirasim）；reclaude 在跑工具 40s：reclaude ${fmt(w.states('reclaude', t0, t1), t0)}，`
      + `mirasim ${fmt(w.states('mirasim', t0, t1), t0)}（要 suspect、stall，不 busy）；Mirasim 在跑工具 8s 后：mirasim=${w.stateAt('mirasim', t1 + 8000)}（要 busy）、reclaude=${rcNow}（不要 busy）`);
  } finally {
    await w.stop();
    mh.close();
    writeFileSync(join(outDir, 'fake-scope.log'), w.log.join('\n'));
    rmSync(home, { recursive: true, force: true });
  }
}

// ---------- hook-cost ----------
function hookCost() {
  const home = mkdtempSync(join(tmpdir(), 'fluxwatch-e2e-cost-'));
  const ms = [];
  try {
    for (let i = 0; i < 20; i++) {
      const t = process.hrtime.bigint();
      hookEvent(home, i % 2 ? 'PostToolUse' : 'PreToolUse');
      ms.push(Number(process.hrtime.bigint() - t) / 1e6);
    }
    const lines = readFileSync(join(home, '.fluxwatch', 'hook.jsonl'), 'utf8').trim().split('\n').length;
    const avg = ms.reduce((a, b) => a + b, 0) / ms.length;
    const max = Math.max(...ms);
    record('hook-cost', lines === 20 && avg < 200, `20 次平均 ${avg.toFixed(1)}ms、最大 ${max.toFixed(1)}ms（含起进程），写了 ${lines} 行`);
  } finally {
    rmSync(home, { recursive: true, force: true });
  }
}

// ---------- real-* ----------
// 本机真实的目标配置：~ 和 <UserCacheDir> 展开成真路径（临时 HOME 下展开会找错地方）。
function realTargets() {
  const real = join(homedir(), '.fluxwatch', 'targets.yaml');
  if (!existsSync(real)) throw new Error('本机没有 ~/.fluxwatch/targets.yaml（先起一次 fluxwatch）');
  const cache = mac ? join(homedir(), 'Library', 'Caches') : process.platform === 'win32' ? process.env.LOCALAPPDATA : join(homedir(), '.cache');
  return readFileSync(real, 'utf8').replaceAll('<UserCacheDir>', cache).replace(/path: ~\//g, `path: ${homedir()}/`);
}

async function realClaude(name, withHook) {
  const home = tempHome(name, realTargets());
  const s = join(homedir(), '.fluxwatch', 'settings.json'); // 手填的 mihomo 地址在这里
  if (existsSync(s)) copyFileSync(s, join(home, '.fluxwatch', 'settings.json'));
  const settings = join(home, 'claude-settings.json');
  const cmd = `HOME='${home}' '${cli}' hook`;
  writeFileSync(settings, JSON.stringify(withHook
    ? { hooks: { PreToolUse: [{ matcher: '*', hooks: [{ type: 'command', command: cmd }] }], PostToolUse: [{ matcher: '*', hooks: [{ type: 'command', command: cmd }] }] } }
    : {}));
  const w = watch(home);
  try {
    await sleep(3000);
    const t0 = Date.now();
    const r = await new Promise((resolve) => {
      // claude 经一个名叫 reclaude 的父进程起，hook 上溯时把这个会话认成 reclaude 的
      const wrap = `const r=require('child_process').spawnSync('claude',process.argv.slice(1),{stdio:'inherit'});process.exit(r.status??1)`;
      const p = spawn(appExe(home, 'reclaude'), ['-e', wrap, '--', '-p', "This is an end-to-end test of a tool-duration monitor. Call the Bash tool exactly once, in the foreground (no run_in_background, no timeout parameter), with this exact command: perl -e 'sleep 70'   When it returns, reply with the single word done. Do nothing else.",
        '--allowedTools', 'Bash', '--settings', settings, '--output-format', 'json'], { stdio: ['ignore', 'pipe', 'pipe'] });
      let out = '';
      p.stdout.on('data', (d) => (out += d));
      p.stderr.on('data', (d) => (out += d));
      p.on('exit', (code) => resolve({ code, out }));
    });
    const t1 = Date.now();
    await sleep(2000);
    writeFileSync(join(outDir, `${name}-claude.json`), r.out);
    // sleep 的时间窗：挂了 hook 用 hook.jsonl 里的 pre / post；对照组按会话总时长减掉首尾各 10s 估
    let from = t0 + 10000, to = t1 - 10000;
    if (withHook) {
      const hl = existsSync(join(home, '.fluxwatch', 'hook.jsonl'))
        ? readFileSync(join(home, '.fluxwatch', 'hook.jsonl'), 'utf8').trim().split('\n').map((l) => JSON.parse(l)) : [];
      const pre = hl.find((l) => l.event === 'pre'), post = hl.find((l) => l.event === 'post');
      if (!pre || !post) {
        record(name, false, `hook 没收到 pre/post（${hl.length} 行），claude 退出码 ${r.code}`);
        return;
      }
      from = Date.parse(pre.ts); to = Date.parse(post.ts);
    }
    const inWin = w.states('reclaude', from, to);
    const startState = w.stateAt('reclaude', from + 1500);
    const bad = inWin.filter((e) => e.to === 'suspect' || e.to === 'stall');
    const detail = `claude 退出码 ${r.code}，会话 ${((t1 - t0) / 1000).toFixed(0)}s；sleep 窗口 ${((to - from) / 1000).toFixed(0)}s，窗口开始时 reclaude=${startState}；`
      + `窗口里：${fmt(inWin, from)}；全程：${fmt(w.states('reclaude', t0), t0)}`;
    if (withHook) record(name, r.code === 0 && startState === 'busy' && bad.length === 0, detail);
    else record(name, r.code === 0 && bad.length === 0, detail + (bad.length ? '（没有 hook 时误报了）' : '（没有 hook 也没误报）'));
  } finally {
    await w.stop();
    writeFileSync(join(outDir, `${name}.log`), w.log.join('\n'));
    rmSync(home, { recursive: true, force: true });
  }
}

// ---------- tray-notify ----------
async function trayNotify() {
  const app = build('fluxwatch', binDir);
  if (!app) return record('tray-notify', false, '托盘应用构建失败');
  const mh = await fakeMihomo();
  const home = tempHome('tray', fakeTargets(mh.addr, 'E2E 假上游'));
  const w = watch(home); // 同一份配置再起一个命令行观测器，拿到精确的跳变时刻
  const env = homeEnv(home);
  delete env.TZ;
  const p = spawn(app, [], { env, stdio: ['ignore', 'pipe', 'pipe'] });
  let tlog = '';
  p.stdout.on('data', (d) => (tlog += d));
  p.stderr.on('data', (d) => (tlog += d));
  try {
    await sleep(4000);
    mh.add('c1', 'api.example.com', 4000, 0);
    const t0 = Date.now();
    let st;
    while (Date.now() - t0 < 95000 && !(st = w.states('e2e', t0).find((e) => e.to === 'stall'))) await sleep(250);
    if (!st) return record('tray-notify', false, `90s 没进 stall：${fmt(w.states('e2e', t0), t0)}`);
    await sleep(1500); // 等通知横幅出来
    // 只截右上角通知横幅那一块，不截整屏（屏上可能有别的会话内容）
    const shot = join(outDir, 'tray-notify-stall.png');
    const sc = spawnSync('screencapture', ['-x', '-R', '1012,30,500,140', shot]);
    mh.bump('c1', 0, 5000);
    await sleep(2500);
    const shot2 = join(outDir, 'tray-notify-recover.png');
    spawnSync('screencapture', ['-x', '-R', '1012,30,500,140', shot2]);
    const notifyErr = /通知：.*失败/.test(tlog);
    record('tray-notify', sc.status === 0 && !notifyErr,
      `stall 在 ${((st.at - t0) / 1000).toFixed(1)}s；截图 ${shot}、${shot2}（要人看一眼横幅在不在）；托盘日志里${notifyErr ? '有' : '没有'}通知失败`);
  } finally {
    p.kill();
    await w.stop();
    mh.close();
    writeFileSync(join(outDir, 'tray-notify.log'), tlog);
    rmSync(home, { recursive: true, force: true });
  }
}

// ---------- 跑 ----------
const jobs = [];
if (want('fake-busy')) jobs.push(fakeBusy());
if (want('fake-stall')) jobs.push(fakeStall());
if (want('fake-scope')) jobs.push(fakeScope());
if (want('hook-cost')) hookCost();
await Promise.all(jobs);

const skipClaude = ci || flag('--skip-claude');
for (const [name, withHook] of [['real-busy', true], ['real-nohook', false]]) {
  if (!want(name)) continue;
  if (skipClaude) record(name, null, ci ? 'CI 上没有真 Claude 会话' : '--skip-claude');
  else {
    try { await realClaude(name, withHook); } catch (e) { record(name, false, e.message); }
  }
}
if (want('tray-notify')) {
  if (ci || !mac || flag('--skip-tray')) record('tray-notify', null, !mac ? '只在 mac 上截通知' : ci ? 'CI 上没有桌面' : '--skip-tray');
  else {
    try { await trayNotify(); } catch (e) { record('tray-notify', false, e.message); }
  }
}

const failed = results.filter((r) => r.ok === false);
console.log(`\n${results.filter((r) => r.ok).length} 通过，${failed.length} 没通过，${results.filter((r) => r.ok === null).length} 跳过；日志在 ${outDir}`);
process.exit(failed.length ? 1 : 0);
