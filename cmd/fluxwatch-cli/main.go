// fluxwatch-cli 是命令行观测器：watch 逐行打状态变化，discover 打当前流量表，status --json 打一份快照，
// hook 给 Claude Code 的 hook 调用（工具前后、回合开始结束、会话结束）。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/thoerwink8/fluxwatch/internal/engine"
	"github.com/thoerwink8/fluxwatch/internal/model"
	"github.com/thoerwink8/fluxwatch/internal/procattr"
	"github.com/thoerwink8/fluxwatch/internal/source"
)

// version 发版时由 -ldflags "-X main.version=..." 注入。
var version = "dev"

const usage = `fluxwatch-cli %s

用法：
  fluxwatch-cli watch            前台跑，每个目标状态一变就打一行：时间 目标 旧→新 层 原因 节点
  fluxwatch-cli discover         此刻正在跑的连接，按域名聚合（域名、软件、节点、连接数、下行）
  fluxwatch-cli status --json    采样 2 秒后打一份快照（JSON）
  fluxwatch-cli hook [事件]      给 Claude Code hook 用：从 stdin 读 hook JSON，往 ~/.fluxwatch/hook.jsonl 追加一行（app 记发起调用的软件）；
                                 一律退出码 0，出错写 ~/.fluxwatch/hook.err
  fluxwatch-cli version

配置在 ~/.fluxwatch/targets.yaml（第一次运行写默认的两个目标）。
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, version)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "watch":
		err = watch(os.Stdout)
	case "discover":
		err = discover(os.Stdout)
	case "status":
		err = status(os.Stdout, os.Args[2:])
	case "hook":
		// 不打扰 Claude Code：任何错误（含 panic）只写 hook.err，退出码一律 0
		hook(os.Args[2:], os.Stdin, time.Now(), procattr.HookCaller)
		os.Exit(0)
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "-h", "--help":
		fmt.Printf(usage, version)
	default:
		fmt.Fprintf(os.Stderr, "不认识的子命令 %q\n\n"+usage, os.Args[1], version)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fluxwatch-cli:", err)
		os.Exit(1)
	}
}

func watch(w io.Writer) error {
	e, err := engine.New(engine.Options{})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ch, unsub := e.Subscribe()
	defer unsub()
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()

	prev := map[string]model.State{}
	prevErr := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			<-done // Run 收尾：把没恢复的卡住写进账本
			return nil
		case s := <-ch:
			for _, line := range changes(s, prev, prevErr) {
				fmt.Fprintln(w, line)
			}
		}
	}
}

// changes 比较上一份，返回要打印的行（状态变化、新出现 / 消失的数据源问题）。
func changes(s model.Snapshot, prev map[string]model.State, prevErr map[string]bool) []string {
	var out []string
	ts := s.At.Format("15:04:05")
	seen := map[string]bool{}
	for _, t := range s.Targets {
		seen[t.Name] = true
		old, ok := prev[t.Name]
		if ok && old == t.State {
			continue
		}
		from := "—"
		if ok {
			from = string(old)
		}
		prev[t.Name] = t.State
		out = append(out, strings.TrimRight(fmt.Sprintf("%s  %-12s %s→%s  %s  %s  %s",
			ts, t.Name, from, t.State, dash(string(t.Layer)), dash(t.Reason), dash(t.Node)), " "))
	}
	for name := range prev {
		if !seen[name] {
			delete(prev, name)
			out = append(out, fmt.Sprintf("%s  %-12s 不再监控", ts, name))
		}
	}
	cur := map[string]bool{}
	for _, e := range s.SourceErr {
		cur[e] = true
		if !prevErr[e] {
			out = append(out, fmt.Sprintf("%s  数据源问题：%s", ts, e))
		}
	}
	for e := range prevErr {
		if !cur[e] {
			out = append(out, fmt.Sprintf("%s  已恢复：%s", ts, e))
		}
	}
	for k := range prevErr {
		delete(prevErr, k)
	}
	for k := range cur {
		prevErr[k] = true
	}
	return out
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func discover(w io.Writer) error {
	e, err := engine.New(engine.Options{})
	if err != nil {
		return err
	}
	defer e.Close()
	ds, err := e.Discover()
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "域名\t软件\t节点\t连接\t下行\t已盯\t建议")
	for _, d := range ds {
		watched := ""
		if d.Watched {
			watched = "是"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n", d.Host, dash(strings.Join(d.Apps, ",")), dash(d.Node), d.ConnN, bytes(d.Down), watched, d.Suggest)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(ds) == 0 {
		fmt.Fprintln(w, "（此刻没有连接）")
	}
	return nil
}

func bytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}

func status(w io.Writer, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "打 JSON")
	sample := fs.Duration("sample", 2*time.Second, "采样多久再出快照（字节增量要两次读数才算得出）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := engine.New(engine.Options{})
	if err != nil {
		return err
	}
	defer e.Close()
	s := e.Tick()
	for end := time.Now().Add(*sample); time.Now().Before(end); {
		time.Sleep(time.Second)
		s = e.Tick()
	}
	if *asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(s)
	}
	for _, t := range s.Targets {
		fmt.Fprintf(w, "%-12s %-9s %s  %s  节点 %s  连接 %d\n", t.Name, t.State, dash(string(t.Layer)), dash(t.Reason), dash(t.Node), len(t.Conns))
	}
	for _, e := range s.SourceErr {
		fmt.Fprintln(w, "数据源问题：", e)
	}
	return nil
}

// hook 把一次 Claude Code hook 事件记进 hook.jsonl。它从不失败：Claude Code 把非 0 退出码显示成 hook error，
// 每次工具调用都弹会很烦，所以错误（包括 panic）只追加到 ~/.fluxwatch/hook.err（64KB 轮转）；连 hook.err 都写不了就算了。
func hook(args []string, in io.Reader, now time.Time, caller func() string) {
	defer func() {
		if r := recover(); r != nil {
			hookErr(now, fmt.Sprintf("panic：%v", r))
		}
	}()
	ev := ""
	if len(args) > 0 {
		ev = args[0]
	}
	p, err := source.HookPath()
	if err == nil {
		err = source.AppendHookEvent(p, in, now, ev, caller())
	}
	if err != nil {
		hookErr(now, err.Error())
	}
}

func hookErr(now time.Time, msg string) {
	if p, err := source.HookErrPath(); err == nil {
		_ = source.AppendHookErr(p, now, msg)
	}
}
