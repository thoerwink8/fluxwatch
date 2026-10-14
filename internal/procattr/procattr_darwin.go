//go:build darwin

package procattr

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

type darwinPlatform struct{}

func platform() Platform { return darwinPlatform{} }

func run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

// PortPIDs 调一次 lsof；没有任何连接时 lsof 退出码是 1 且输出为空，不算错。
func (darwinPlatform) PortPIDs() (map[int]int, error) {
	out, err := run("lsof", "-nP", "-iTCP", "-sTCP:ESTABLISHED", "-Fpcn")
	if err != nil {
		var ee *exec.ExitError
		if !(errors.As(err, &ee) && ee.ExitCode() == 1 && out == "") {
			return nil, fmt.Errorf("lsof 失败：%w", err)
		}
	}
	return parseLsof(out), nil
}

func (darwinPlatform) AppNames(pids []int) map[int]string {
	out, err := run("ps", "-Ao", "pid=,ppid=,comm=")
	if err != nil {
		return nil
	}
	tree := parsePs(out)
	m := map[int]string{}
	for _, pid := range pids {
		if app := appFromTree(pid, tree); app != "" {
			m[pid] = app
		}
	}
	return m
}
