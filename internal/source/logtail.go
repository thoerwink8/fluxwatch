package source

import (
	"regexp"

	"github.com/thoerwink8/fluxwatch/internal/config"
)

// DefaultAccount 是没配 account 正则时用的换号 / 配额线索。
const DefaultAccount = `(?i)(insufficient_quota|quota|usage.?limit|切换账号|换号|配额|额度)`

var statusCode = regexp.MustCompile(`\b(429|5[0-9][0-9])\b`)

// LogResult 是一轮里日志新增行的统计；只有计数和状态码，不留行内容。
type LogResult struct {
	Progress int      // 匹配 progress 的行数
	Starts   int      // 匹配 start 的行数（回合开始）
	Ends     int      // 匹配 end 的行数（回合结束）
	Errors   []string // 匹配 error 的行，各取一个状态码（取不到记 "error"）
	Account  int      // 匹配换号 / 配额线索的行数
}

// Logtail 跟读一个日志文件。
type Logtail struct {
	Path                                 string // 展开后的路径
	tail                                 *Tailer
	progress, errRe, account, start, end *regexp.Regexp
}

// HasProgress 表示这个日志能给「进展」（配了 progress 正则）。
func (l *Logtail) HasProgress() bool { return l.progress != nil }

// HasTurns 表示这个日志能给回合开始 / 结束。
func (l *Logtail) HasTurns() bool { return l.start != nil }

// NewLogtail 按配置建；路径展开 ~ 和 <UserCacheDir>。只看打开以后新增的行。
func NewLogtail(src config.Source) (*Logtail, error) {
	p, err := config.ExpandPath(src.Path)
	if err != nil {
		return nil, err
	}
	l := &Logtail{Path: p, tail: NewTailer(p, 0)}
	comp := func(s string) (*regexp.Regexp, error) {
		if s == "" {
			return nil, nil
		}
		return regexp.Compile(s)
	}
	acc := src.Account
	if acc == "" {
		acc = DefaultAccount
	}
	for _, x := range []struct {
		dst **regexp.Regexp
		s   string
	}{{&l.progress, src.Progress}, {&l.errRe, src.Error}, {&l.account, acc}, {&l.start, src.Start}, {&l.end, src.End}} {
		re, err := comp(x.s)
		if err != nil {
			return nil, err
		}
		*x.dst = re
	}
	return l, nil
}

// Close 关文件。
func (l *Logtail) Close() { l.tail.Close() }

// Poll 读新增行并归类。读不到返回错误。
func (l *Logtail) Poll() (LogResult, error) {
	var r LogResult
	lines, err := l.tail.Lines()
	for _, ln := range lines {
		if l.progress != nil && l.progress.MatchString(ln) {
			r.Progress++
		}
		if l.start != nil && l.start.MatchString(ln) {
			r.Starts++
		}
		if l.end != nil && l.end.MatchString(ln) {
			r.Ends++
		}
		if l.errRe != nil {
			if m := l.errRe.FindStringSubmatch(ln); m != nil {
				code := ""
				if len(m) > 1 {
					code = m[1]
				}
				if code == "" {
					code = statusCode.FindString(ln)
				}
				if code == "" {
					code = "error"
				}
				r.Errors = append(r.Errors, code)
				continue // 报错行不再算配额线索
			}
		}
		if l.account != nil && l.account.MatchString(ln) {
			r.Account++
		}
	}
	return r, err
}
