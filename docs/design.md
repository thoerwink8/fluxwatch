# Fluxwatch 设计

界面长相看 `design/ui-sketch.html`（明暗两版、多目标、发现面板、Windows 差异都画在里面）。这份写数据源、判定和结构。

## 一、实测出来的前提

在一台装 Clash Party（mihomo v1.19.14）的 mac 上量的：

1. **空闲连接的字节增量常态是 0。** 30 秒每秒采样，11 条 reclaude / mirasim 连接里 8 条全程 0。只盯字节必然误报，「此刻该不该有数据」是判定的主干。
2. **mihomo 的控制接口不在 TCP 上。** Clash Party 把 `external-controller` 置空，接口在 Unix socket `/tmp/mihomo-party-<uid>-<pid>.sock`（核心日志里有 `RESTful API unix listening at`），免密钥。路径带 pid，核心重启会变，要按 glob 找。Windows 上的形态没验过（可能是命名管道），找不到时允许在设置里填 TCP 控制器地址和 secret。
3. **mihomo 给不出进程。** `find-process-mode: strict` 开着，`/connections` 的 `process` 也全空。用 `metadata.sourcePort` 反查本机：mac 上 `lsof -nP -iTCP -sTCP:ESTABLISHED -Fpcn` 一次 40ms 左右，补上 19/21 条（剩下的是已关的短连接）；Windows 用 `GetExtendedTcpTable` 拿端口→PID。
4. **软件名要上溯。** 很多连接是 `X Helper` 子进程发的。沿 ppid 往上最多 6 层，遇到 `.app` 目录取它的名字；Windows 取可执行文件的 FileDescription，没有就取文件名。实测 `api.github.com` 那条上溯 3 层到 `Mirasim`。
5. **转录文件不能当心跳。** Claude Code 的 jsonl 一次写一块，写完能 15 秒不动，分不出在想还是卡了。
6. **代理面板的延迟不可信。** 面板报 50ms 的节点，真实请求首字节 300–400ms。

## 二、数据源

每种数据源只回答两件事：**有没有进展**、**有没有报错**。判定逻辑不认识 reclaude 是什么，所以加目标只写配置。

| 类型 | 读什么 | 进展 | 报错 |
|---|---|---|---|
| `mihomo` | `/connections`，按 `hosts` 通配匹配 host，可选 `apps` 只算这些软件发的连接（浏览器打开同域名不算） | 匹配连接的下行字节有增量 | — |
| `logtail` | 跟读一个日志文件的新增行（处理截断、轮转；盯着时才建出来的文件从头读）；可选 `start` / `end` 正则标出回合 | 行匹配 `progress` 正则 | 行匹配 `error` 正则，记状态码 |
| `hook` | Claude Code 的 PreToolUse / PostToolUse / PostToolUseFailure / PermissionDenied 和回合事件 UserPromptSubmit / Stop / StopFailure / SubagentStop / SessionEnd 调 `fluxwatch-cli hook`，往 `~/.fluxwatch/hook.jsonl` 追加一行（事件、工具名、会话、`agent_id`、`tool_use_id`、`app`，不记入参和提示词；超过 1MB 先改名成 `hook.jsonl.1` 再写）；`app` 记发起这次调用的软件 | — | — ；给「本地工具在跑」和「在等模型回包」，按目标的 `claude` 字段归属（`claude: all` 收所有会话，不看发起软件） |

mihomo 接口按顺序自动找，找到能回 `/version` 的就用：Clash Party 的 socket（`/tmp/mihomo-party-*.sock`）→ Clash Verge Rev 的 socket（2.5 起 `$TMPDIR/verge-mihomo.sock`，服务模式 `/var/run/clash-verge-service/users/<uid>/verge-mihomo.sock`，2.4 及以前 `/var/tmp/verge/verge-mihomo.sock`）→ Windows 上名字带 mihomo 的命名管道（含 `verge-mihomo*`）→ TCP `127.0.0.1:9090`、`127.0.0.1:9097`。要 secret 或在别处的，设置里选「手填」。不去读 Clash Verge 自己的 config.yaml 拿 secret：那份文件里有节点配置。

`hook` 回答两件事，都按会话、子代理按 `agent_id` 单算：
- **本地忙（busy）**：一次工具调用从 pre 到它自己的 post（按 `tool_use_id` 配对，旧记录先进先出），每次单独最多算 10 分钟；Stop 一类回合结束清掉这个会话没结束的调用。工具在跑时网络静默是正常的。派子代理的 `Agent`（老版本叫 `Task`）不算本地忙：主代理在等子代理，子代理在联网，靠子代理自己的 pre / post 判。
- **在飞**：一个代理从 UserPromptSubmit 起、或从它最后一个 post 起，到下一个 pre 或 Stop 止，在等模型回包。只算读到过回合事件的会话——Claude Code 只在会话开始时读 hook 配置，装回合事件之前起的会话回合结束没有 Stop，post 之后的在飞收不了尾。30 分钟没有任何事件当成 Stop 丢了。

没装 hook（hook.jsonl 没出现过或读不到）时不判 busy、不判 hook 在飞，在设置里提示怎么装；回合层只报慢（第四节）。

hook 归到哪个目标，看目标的 `claude` 字段（#27）：它写明「哪些 Claude Code 会话的模型流量走这个目标的线路」。`claude: all` 收所有会话（发起软件为空的也算），`claude: [Mirasim]` 按 hook 记录的发起软件（不分大小写、不算 `.exe`；列表时发起软件为空的记录也算本地忙，宁可压住），不写就不接 hook。本地忙、在飞、断流都只按这个字段归属，不再拿发起软件去对 mihomo 的 `apps`——发起软件和流量走哪条线路是两回事：本机 Mirasim 里起的会话，发起软件是 `Mirasim`，模型请求却经 mirasim-reclaude 启动器交给 reclaude 网关，连接是 reclaude 进程发的、走 `*.reclaude.ai`。所以默认配置把 `claude: all` 写在 reclaude 上，mirasim 目标只管 Mirasim 自己的云端中转。老配置没有任何目标写 `claude` 时照常能跑，hook 不归任何目标（不判本地忙、回合层只报慢），启动和配置变了时日志里写一行提示怎么加。

发起软件这样找：`fluxwatch-cli hook` 从自己的父进程（claude）顺链往上最多 8 层（`procattr.CallerApp`），跳过 claude、shell、node 这类过道，取第一个别的进程——它在某个 `.app` 里就取最外层 `.app` 的名字；是命令行程序时，紧挨着的上一层（同名副本不算）是非终端的 `.app` 就取那个 `.app`（Mirasim 起的 mirasim-reclaude → `Mirasim`），否则取它的文件名（`reclaude`）；碰到终端就停，留空。上溯在 mac 上用 sysctl、Windows 用进程快照、Linux 读 `/proc`，不起子进程。

装法（README「Claude Code hook」有 mac、Windows 可粘贴的片段）：`~/.claude/settings.json` 的 `hooks.PreToolUse` 和 `hooks.PostToolUse` 各有一条 `matcher` 为 `*`（或不写）、`type` 为 `command`、命令调用 `fluxwatch-cli hook` 的 hook，就算已装；上面那几个回合、失败事件也各挂一条（缺的设置窗照实列出来）。设置窗的「hook 安装状态」照这条判（`source.CheckHookInstall`），顺带查命令指向的二进制在不在；只挂一边、或 matcher 只限部分工具，照实说哪里不够。

数据源读不到时：日志文件不存在（被盯的程序没装、还没跑过）只记一条 sourceErr，这一路不算，目标照其余数据源判；给回合的日志不见了就当回合结束，不拿它判卡住。其余读错（权限、mihomo 连不上），或者目标的数据源全都读不到，目标 UNKNOWN。

## 三、进程归属

每秒一轮：读 `/connections` → 收集 sourcePort → 一次反查端口到 PID（mac `lsof`，Windows `GetExtendedTcpTable`）→ PID 上溯到软件名（结果按 PID 缓存，进程没了再清）。反查失败不阻塞判定，`App` 留空，界面显示「—」。

## 四、判定

每个目标一个状态机：

```
IDLE ──(有连接在动 / 日志出现回合开始)──> OK
OK ──(进展)──> OK                         重置计时
OK ──(超 suspect 无进展)──> SUSPECT
SUSPECT ──(进展)──> OK
SUSPECT ──(超 stall 无进展)──> STALL       此刻判层、发一次通知
STALL ──(真进展)──> OK                     发一次「已恢复」（Target.Recovered）
STALL ──(线路又动了 / 重发了请求，没真进展)──> STALL   同一次卡住，不出来
任何态 ──(hook 说本目标的软件有工具在跑)──> BUSY   工具结束回到原来的计时
任何态 ──(数据源读不到，见第二节)──> UNKNOWN
任何态 ──(没有在飞的请求超过 60s)──> IDLE    开着的卡住按「未恢复」收尾，不发「已恢复」
任何态 ──(睡眠唤醒)──> 清等待、30s 宽限        开着的卡住按「未恢复」收尾，时长截到睡前
```

「在飞」（在等回包，三层取最重的结论）：
1. **hook 层**：hook 说归到本目标的会话（目标的 `claude` 字段，第二节）有代理在等模型回包，而且这段在飞里目标连接上真发出过模型请求（一秒上行 ≥ 8KB：请求带整段上下文，本机实测几十到几百 KB；relay 心跳、鉴权小请求都在 2KB 以下）——这叫「上膛」。上膛后从请求时刻或最近一次真下行（去掉 ping-pong）起没有字节，就升 SUSPECT / STALL，阈值是下面的自适应阈值但不低于 40s / 75s（本机影子引擎实测 hook 说在等模型的 1098 秒里，走模型流量的线路最长 36s 没有一个下行字节——长思考就是这样，按 30s 判会误报）：回包到一半断流、几个会话共用 launch.log 时一个结束另一个卡住，都靠这一层报出来。不上膛不判（`claude` 写错了目标、模型流量不走它的线路时不误报）。收到 Stop 就不在飞，静默不报。hook 层在飞期间，字节层不判这个目标：本机重放 53 分钟真实流量，reclaude 网关一条连接常常只往上传（上 MB）、回复走别的连接，按连接等回包会挂几分钟的「慢 / 卡住」。
2. **字节层**：某条连接发了请求（一秒上行 ≥ 1KB）、这条连接还没回包。hook 层在飞时不判（见上）；hook 不知道有会话在飞时照常判——装回合事件之前起的会话、不是 Claude 的流量都靠它，本机 09:22 那次真断网就是这一层报的。同目标别的连接的回包不算（本机 09:23 断网的真样本：一条连接发 1.2KB 后再没回，另一条连接两秒后收到的是它自己的回包，之后线路全停——按目标合起来算会漏掉这次）。
3. **回合层**：日志里回合开始了还没结束。回合日志只有首帧和结束两种进展，一个回合里跑几分钟工具很常见，所以目标没接 hook（没装、或目标没写 `claude`）时，回合层的线路结论最多报到 SUSPECT（慢），不进 STALL、不发通知；日志里有 429 / 5xx、配额线索时照常 STALL。hook 带回合事件时回合层不判（回合就是 hook 看到的那些会话，归 hook 层；回合日志几分钟没新帧在长回合里是常态）。只装了 Pre / Post 的旧 hook 照旧由回合层判 STALL。没装 hook 时面板常驻提示「没装 hook：回合内只报慢」。

字节层按连接等回包：一条连接一秒内上行 ≥ 1KB 算在这条连接上发了请求，只有这条连接自己的下行（或日志进展）算回包。同目标别的连接的保活心跳不算（mirasim relay 每 30 秒一次，正好等于卡住阈值，按目标合起来算会把在等的请求清掉）；同一秒上下行都是小包（< 1KB 的 ping-pong）在哪条连接上都不算。在等的那条连接从 `/connections` 里没了：之后来了新请求就不再等旧的；60 秒内没有新请求就当这次请求放弃了，不再算在飞。

「真进展」是：在等的连接回了包、日志新帧、回合结束，或者没配 progress 的回合里来了真下行。进了 STALL 只有真进展才出来——线路又动了（THINKING）、客户端重发了请求、BUSY / SUSPECT 中间夹一下都还是同一次卡住，账本一行、通知一次。「线路在动」要真下行：最近一个 suspect 窗口里去掉 ping-pong 的下行累计 ≥ 1KB，心跳撑不起来。引擎只在真进展让 STALL 结束的那一轮标 `Target.Recovered`，「已恢复」通知只认它；卡住转 IDLE / UNKNOWN、等的连接没了、睡眠唤醒收尾都不发。

睡眠唤醒：两轮之间墙钟或单调钟跳了超过 10 秒（mac 的单调钟睡眠时不走，只能看墙钟；Windows 两种都跳）当成断档。断档时清掉在等的请求和间隔起点，回合从现在重新计时；开着的卡住按「未恢复」收尾，账本时长截到睡前一轮；之后 30 秒宽限期内在飞也不升级，计时暂停（网络要一会儿才回来）。宽限之后照常判。

阈值按目标、按节点自适应，但有上限：`suspect = clamp(p95 × 5, 配置值, 30s)`，`stall = clamp(p95 × 10, 配置值, 90s)`，默认配置值 10s / 30s。p95 只用「在飞」期间（发了请求、在等回包）的进展间隔算，空闲和保活心跳不算——不封顶时实测被心跳撑到 75s / 150s，卡两分钟都不报。

进 STALL 那一刻判层，按顺序：
1. 目标日志最近 stall 窗口里有 429 / 5xx → **B 上游卡**（reason 写状态码和次数）
2. 有换号、配额线索 → **C 账号配额**
3. 线路字节全停 → **A 线路停**（reason 写节点）
4. 线路在动、日志干净 → 不进 STALL，记 THINKING

## 五、配置和账本

- 配置 `~/.fluxwatch/targets.yaml`（Windows `%USERPROFILE%\.fluxwatch\targets.yaml`），第一次启动写默认的两个目标。手改和界面「加入监控」写同一份；文件变了热加载。界面加 / 删目标在 yaml 节点树上改 `targets` 那一段，注释保留（流式写法的空格会被重排）。
- 托盘每秒把当前快照原子写到 `~/.fluxwatch/state.json`。`fluxwatch-cli status` 默认读它并标出快照时间；快照超过 5 秒没更新或文件不在，就自己采样并写明「托盘没在跑」。
- 账本 `~/.fluxwatch/ledger.jsonl`：每次 STALL 一行（时间、目标、层、节点、时长、恢复时间），不含会话内容。「今天第 N 次」从它算。

默认配置：

```yaml
defaults: { suspect: 10s, stall: 30s }
targets:
  - name: reclaude
    label: ReClaude
    claude: all
    sources:
      - { type: mihomo, hosts: ["*.reclaude.ai"], apps: [reclaude] }
      - { type: logtail, path: ~/.reclaude/logs/daemon.log, error: ' (429|50[0-9]) ' }
      - { type: logtail, path: <UserCacheDir>/reclaude-mirasim/launch.log, progress: 'event=turn-(first-output|result)', start: 'event=(launch|turn-first-output)( |$)', end: 'event=(turn-result|reject|once-exit)( |$)' }
  - name: mirasim
    label: Mirasim Cloud
    sources:
      - { type: mihomo, hosts: ["*.mirasim.ai"], apps: [Mirasim] }
```

目标按「流量走哪条线路」划分（#27）：Claude 会话不管从哪个软件起，模型请求都经 reclaude 网关出去，所以 `claude: all` 和 launch.log 的回合都挂在 reclaude 上；mirasim 只管 Mirasim 自己的云端中转 `relay.mirasim.ai`，不接 hook，按字节层判。

launch.log 的回合正则是按本机 `launch.log` 的事件顺序定的（只看 `event=` / `ms=`）：一次对话是 `launch → target-spawned → first-frame → turn-first-output → turn-result`，同一会话的后续回合没有 `launch`，是 `route-resolved → turn-first-output → turn-result`；`launch → target-spawned → once-exit` 是一次性调用、不出回合，`launch → reject` 是被拒。所以回合从 `launch` 或 `turn-first-output` 开始，到 `turn-result`、`reject` 或 `once-exit` 结束。几个会话共用一份日志时只有一个回合开关，交错时可能提前收（宁可少报，不误报）。已经写出来的 `targets.yaml` 不会自动改成这个结构，要手改（老结构照常能跑，只是 hook 不归任何目标，日志里有提示）。

## 六、结构

```
cmd/fluxwatch/        托盘应用（Wails v3）
cmd/fluxwatch-cli/    命令行：watch（逐行打状态变化）/ discover / status --json
internal/model/       引擎和界面的约定（Snapshot、Provider）
internal/mihomo/      找接口（socket / 命名管道 / TCP）、读 /connections
internal/procattr/    端口 → PID → 软件名（_darwin / _windows）
internal/source/      mihomo、logtail、hook 三种数据源
internal/engine/      状态机、阈值、判层、实现 model.Provider
internal/config/      targets.yaml 读写、热加载
internal/ledger/      账本
internal/app/         托盘、面板窗口、通知（Wails）
frontend/             面板 HTML/CSS/JS
```

## 七、界面

照草图。要点：
- 菜单栏图标取 Severity 最大的目标，角标是报警目标数。卡住呼吸，尊重减少动效。
- mac 不用模板图标：正常态按菜单栏当前明暗自己画单色图，外观变了重画，换图不重建状态项（Wails v3 beta 里用过模板图标后彩色图也被当模板，而重建会把图标甩到菜单栏最左、落进刘海）。
- 菜单栏挤满时图标会落进刘海看不见：启动时用 NSScreen 的刘海两侧安全区判断，落进去就发一次通知（不受通知开关管，它是告诉人怎么找回入口）。全局快捷键 mac ⌥⌘F、Windows Ctrl+Alt+F 开关面板，贴不到托盘时面板居中在屏幕顶部。状态项设 autosaveName，⌘ 拖过的位置下次还在（要 .app 的 bundle id）。
- 点图标弹贴着托盘的面板：目标列表（每行：状态点、名字、节点 · 软件、时长），点一行展开示波器、连接列表、「复制诊断」「打开日志」。
- 「发现」面板：此刻正在跑、按域名聚合的连接，带软件名、节点、下行量；选中建议通配符，「加入监控」。
- 通知只在进 STALL 和恢复时各一次，不带会话内容。
- 设置：开机启动、通知开关、hook 安装状态、mihomo 接口（自动 / 手填）、阈值（高级）。
