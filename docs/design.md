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
| `mihomo` | `/connections`，按 `hosts` 通配匹配 host | 匹配连接的下行字节有增量 | — |
| `logtail` | 跟读一个日志文件的新增行（处理截断、轮转） | 行匹配 `progress` 正则 | 行匹配 `error` 正则，记状态码 |
| `hook` | Claude Code 的 PreToolUse / PostToolUse（hook 往 `~/.fluxwatch/hook.jsonl` 追加一行） | — | — ；只给「本地工具在跑」 |

`hook` 的作用是判 `busy`：工具在跑时网络静默是正常的。没装 hook 时不判 busy，在设置里提示怎么装。

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
STALL ──(进展)──> OK                       发一次「已恢复」
任何态 ──(hook 说工具在跑)──> BUSY          工具结束回到原来的计时
任何态 ──(数据源读不到)──> UNKNOWN
任何态 ──(没有在飞的请求超过 60s)──> IDLE
```

「在飞」：目标的连接最近 60 秒有过上行（发了请求），且之后下行还没停稳；或日志里回合开始了还没结束。

阈值按目标、按节点自适应：`suspect = max(配置值, 该节点近期进展间隔 p95 × 5)`，`stall = max(配置值, p95 × 10)`；默认配置值 10s / 30s。

进 STALL 那一刻判层，按顺序：
1. 目标日志最近 stall 窗口里有 429 / 5xx → **B 上游卡**（reason 写状态码和次数）
2. 有换号、配额线索 → **C 账号配额**
3. 线路字节全停 → **A 线路停**（reason 写节点）
4. 线路在动、日志干净 → 不进 STALL，记 THINKING

## 五、配置和账本

- 配置 `~/.fluxwatch/targets.yaml`（Windows `%USERPROFILE%\.fluxwatch\targets.yaml`），第一次启动写默认的两个目标。手改和界面「加入监控」写同一份；文件变了热加载。
- 账本 `~/.fluxwatch/ledger.jsonl`：每次 STALL 一行（时间、目标、层、节点、时长、恢复时间），不含会话内容。「今天第 N 次」从它算。

默认配置：

```yaml
defaults: { suspect: 10s, stall: 30s }
targets:
  - name: reclaude
    label: ReClaude
    sources:
      - { type: mihomo, hosts: ["*.reclaude.ai"] }
      - { type: logtail, path: ~/.reclaude/logs/daemon.log, error: ' (429|50[0-9]) ' }
  - name: mirasim
    label: Mirasim Cloud
    sources:
      - { type: mihomo, hosts: ["*.mirasim.ai"] }
      - { type: logtail, path: <UserCacheDir>/reclaude-mirasim/launch.log, progress: 'event=turn-(first-output|result)' }
```

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
- 菜单栏图标取 Severity 最大的目标，角标是报警目标数；mac 正常态用模板图标。卡住呼吸，尊重减少动效。
- 点图标弹贴着托盘的面板：目标列表（每行：状态点、名字、节点 · 软件、时长），点一行展开示波器、连接列表、「复制诊断」「打开日志」。
- 「发现」面板：此刻正在跑、按域名聚合的连接，带软件名、节点、下行量；选中建议通配符，「加入监控」。
- 通知只在进 STALL 和恢复时各一次，不带会话内容。
- 设置：开机启动、通知开关、hook 安装状态、mihomo 接口（自动 / 手填）、阈值（高级）。
