// Package config 读写 ~/.fluxwatch/targets.yaml：第一次写默认两个目标，按 mtime 热加载，界面「加入监控」写回。
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration 在 yaml 里写成 "10s" 这种。
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("第 %d 行：时长 %q 写法不对（要像 10s、1m）", n.Line, s)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// Std 转成 time.Duration。
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Source 类型。
const (
	TypeMihomo  = "mihomo"
	TypeLogtail = "logtail"
)

// Source 是目标的一个数据源。
type Source struct {
	Type     string   `yaml:"type"`
	Hosts    []string `yaml:"hosts,omitempty"`    // mihomo：host 通配
	Path     string   `yaml:"path,omitempty"`     // logtail：日志文件（~ 和 <UserCacheDir> 会展开）
	Progress string   `yaml:"progress,omitempty"` // logtail：进展行正则
	Error    string   `yaml:"error,omitempty"`    // logtail：报错行正则（第一个捕获组当状态码）
	Account  string   `yaml:"account,omitempty"`  // logtail：换号 / 配额线索正则；不填用内置的
	Start    string   `yaml:"start,omitempty"`    // logtail：回合开始行正则（可选）
	End      string   `yaml:"end,omitempty"`      // logtail：回合结束行正则（可选）
}

// Target 是一个被盯的目标。
type Target struct {
	Name    string   `yaml:"name"`
	Label   string   `yaml:"label,omitempty"`
	Suspect Duration `yaml:"suspect,omitempty"`
	Stall   Duration `yaml:"stall,omitempty"`
	Sources []Source `yaml:"sources"`
}

// Defaults 是阈值的配置值（自适应阈值的下限）。
type Defaults struct {
	Suspect Duration `yaml:"suspect"`
	Stall   Duration `yaml:"stall"`
}

// Mihomo 是手填的控制器；空着就自动探测。
type Mihomo struct {
	Controller string `yaml:"controller,omitempty"` // external-controller，如 127.0.0.1:9090
	Secret     string `yaml:"secret,omitempty"`
}

// Config 是 targets.yaml 的全部。
type Config struct {
	Defaults Defaults `yaml:"defaults"`
	Mihomo   Mihomo   `yaml:"mihomo,omitempty"`
	Targets  []Target `yaml:"targets"`
}

// DefaultSuspect / DefaultStall 是 design 第四节的默认阈值。
const (
	DefaultSuspect = 10 * time.Second
	DefaultStall   = 30 * time.Second
)

// DefaultYAML 是第一次启动写的配置（design 第五节）。
const DefaultYAML = `# Fluxwatch 监控目标。手改和界面「加入监控」写的是同一份，改完自动生效。
# mihomo: { controller: 127.0.0.1:9090, secret: xxx }  # 自动找不到控制接口时手填
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
`

// Dir 是 ~/.fluxwatch（Windows 上 %USERPROFILE%\.fluxwatch）。
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("找不到用户目录：%w", err)
	}
	return filepath.Join(home, ".fluxwatch"), nil
}

// DefaultPath 是 targets.yaml 的位置。
func DefaultPath() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "targets.yaml"), nil
}

// ExpandPath 展开开头的 ~ 和 <UserCacheDir>。
func ExpandPath(p string) (string, error) {
	switch {
	case p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, p[1:]), nil
	case strings.HasPrefix(p, "<UserCacheDir>"):
		d, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(d, strings.TrimPrefix(p, "<UserCacheDir>")), nil
	}
	return filepath.Clean(p), nil
}

// Parse 解析并校验。
func Parse(b []byte) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Validate 检查名字唯一、类型已知、正则能编译。
func (c *Config) Validate() error {
	if c.Defaults.Suspect <= 0 {
		c.Defaults.Suspect = Duration(DefaultSuspect)
	}
	if c.Defaults.Stall <= 0 {
		c.Defaults.Stall = Duration(DefaultStall)
	}
	names := map[string]bool{}
	for i, t := range c.Targets {
		if t.Name == "" {
			return fmt.Errorf("第 %d 个目标没有 name", i+1)
		}
		if names[t.Name] {
			return fmt.Errorf("目标名 %q 重复", t.Name)
		}
		names[t.Name] = true
		if len(t.Sources) == 0 {
			return fmt.Errorf("目标 %s 没有数据源", t.Name)
		}
		for _, s := range t.Sources {
			switch s.Type {
			case TypeMihomo:
				if len(s.Hosts) == 0 {
					return fmt.Errorf("目标 %s 的 mihomo 数据源没写 hosts", t.Name)
				}
			case TypeLogtail:
				if s.Path == "" {
					return fmt.Errorf("目标 %s 的 logtail 数据源没写 path", t.Name)
				}
				for _, re := range []string{s.Progress, s.Error, s.Account, s.Start, s.End} {
					if re == "" {
						continue
					}
					if _, err := regexp.Compile(re); err != nil {
						return fmt.Errorf("目标 %s 的正则 %q 写错了：%w", t.Name, re, err)
					}
				}
			default:
				return fmt.Errorf("目标 %s 有不认识的数据源类型 %q（只有 mihomo、logtail）", t.Name, s.Type)
			}
		}
	}
	return nil
}

// DisplayLabel 返回显示名，没写就用 name。
func (t Target) DisplayLabel() string {
	if t.Label != "" {
		return t.Label
	}
	return t.Name
}

// Store 管一份 targets.yaml：读、热加载、写回。
type Store struct {
	mu   sync.Mutex
	path string
	cur  *Config
	mod  time.Time
	size int64
	err  error // 最近一次加载失败的原因；成功后清空
}

// Open 读配置；文件不存在就写默认的。读坏了返回错误（不悄悄覆盖用户的文件）。
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := writeAtomic(path, []byte(DefaultYAML)); err != nil {
			return nil, fmt.Errorf("写默认配置失败：%w", err)
		}
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	fi, err := os.Stat(s.path)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	c, err := Parse(b)
	if err != nil {
		return fmt.Errorf("%s：%w", filepath.Base(s.path), err)
	}
	s.cur, s.mod, s.size = c, fi.ModTime(), fi.Size()
	return nil
}

// Path 是配置文件路径。
func (s *Store) Path() string { return s.path }

// Get 返回当前生效的配置（只读用，别改）。
func (s *Store) Get() *Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Reload 文件 mtime 或大小变了就重读。返回是否换了配置；读坏了保留旧配置并返回错误。
func (s *Store) Reload() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fi, err := os.Stat(s.path)
	if err != nil {
		s.err = fmt.Errorf("配置文件读不到：%w", err)
		return false, s.err
	}
	if fi.ModTime().Equal(s.mod) && fi.Size() == s.size {
		return false, s.err
	}
	if err := s.load(); err != nil {
		s.mod, s.size = fi.ModTime(), fi.Size() // 坏文件不反复重读，等下次改动
		s.err = err
		return false, err
	}
	s.err = nil
	return true, nil
}

// AddTarget 加一个只看 mihomo 的目标并写回文件。name 由 label 生成，重复时加数字。
func (s *Store) AddTarget(label, hostPattern string) (string, error) {
	label = strings.TrimSpace(label)
	hostPattern = strings.ToLower(strings.TrimSpace(hostPattern))
	if hostPattern == "" {
		return "", errors.New("要给一个域名或通配符")
	}
	if label == "" {
		label = strings.TrimPrefix(hostPattern, "*.")
	}
	return s.mutate(func(c *Config) (string, error) {
		for _, t := range c.Targets {
			for _, src := range t.Sources {
				for _, h := range src.Hosts {
					if h == hostPattern {
						return "", fmt.Errorf("%s 已经在目标 %s 里了", hostPattern, t.Name)
					}
				}
			}
		}
		base := slug(label)
		if base == "" {
			base = slug(strings.TrimPrefix(hostPattern, "*."))
		}
		if base == "" {
			base = "target"
		}
		name := base
		for i := 2; c.has(name); i++ {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		c.Targets = append(c.Targets, Target{Name: name, Label: label, Sources: []Source{{Type: TypeMihomo, Hosts: []string{hostPattern}}}})
		return name, nil
	})
}

// RemoveTarget 删一个目标并写回。
func (s *Store) RemoveTarget(name string) error {
	_, err := s.mutate(func(c *Config) (string, error) {
		for i, t := range c.Targets {
			if t.Name == name {
				c.Targets = append(c.Targets[:i], c.Targets[i+1:]...)
				return name, nil
			}
		}
		return "", fmt.Errorf("没有叫 %s 的目标", name)
	})
	return err
}

func (c *Config) has(name string) bool {
	for _, t := range c.Targets {
		if t.Name == name {
			return true
		}
	}
	return false
}

// mutate 先重读磁盘上的最新版（别丢了用户手改），改完校验、原子写回。
// 注意：写回会丢掉文件里的注释。
func (s *Store) mutate(f func(*Config) (string, error)) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if err != nil {
		return "", err
	}
	c, err := Parse(b)
	if err != nil {
		return "", fmt.Errorf("配置文件现在是坏的，先修好再加：%w", err)
	}
	out, err := f(c)
	if err != nil {
		return "", err
	}
	if err := c.Validate(); err != nil {
		return "", err
	}
	nb, err := yaml.Marshal(c)
	if err != nil {
		return "", err
	}
	if err := writeAtomic(s.path, nb); err != nil {
		return "", err
	}
	if err := s.load(); err != nil {
		return "", err
	}
	s.err = nil
	return out, nil
}

func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			dash = false
		} else if b.Len() > 0 && !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func writeAtomic(path string, b []byte) error {
	tmp := fmt.Sprintf("%s.%d.tmp", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
