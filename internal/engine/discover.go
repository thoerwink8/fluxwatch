package engine

import (
	"net"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/publicsuffix"

	"github.com/thoerwink8/fluxwatch/internal/model"
	"github.com/thoerwink8/fluxwatch/internal/procattr"
)

// Suggest 给域名一个建议的通配：取注册域（misaka.route.reclaude.ai → *.reclaude.ai）；IP 和取不到的原样返回。
func Suggest(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || net.ParseIP(host) != nil {
		return host
	}
	d, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return host
	}
	return "*." + d
}

// Discover 现读一次 /connections，按域名聚合，标出已被盯的。按下行量从大到小排。
func (e *Engine) Discover() ([]model.Discovered, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ui, _ := e.settings.Get()
	conns, err := e.fetch(e.cfg.Get(), ui)
	if err != nil {
		return nil, err
	}
	var ports []int
	for _, c := range conns {
		if p, err := strconv.Atoi(c.Metadata.SourcePort); err == nil {
			ports = append(ports, p)
		}
	}
	procs := map[int]procattr.Proc{}
	procsOK := false
	if len(ports) > 0 {
		if m, err := e.opts.Procs.Lookup(ports); err == nil {
			procs, procsOK = m, true
		}
	}
	type agg struct {
		d     model.Discovered
		nodes map[string]int
		apps  map[string]bool
	}
	by := map[string]*agg{}
	for _, c := range conns {
		host := c.HostName()
		if host == "" {
			continue
		}
		a := by[host]
		if a == nil {
			a = &agg{d: model.Discovered{Host: host, Suggest: Suggest(host)}, nodes: map[string]int{}, apps: map[string]bool{}}
			by[host] = a
		}
		a.d.ConnN++
		a.d.Down += c.Download
		a.nodes[c.Node()]++
		var pr procattr.Proc
		if p, err := strconv.Atoi(c.Metadata.SourcePort); err == nil {
			pr = procs[p]
		}
		if pr.App != "" {
			a.apps[pr.App] = true
		}
		// 一行里有任一条连接被某个目标算进去就标「已盯」（同一个域名，浏览器发的可能没被盯）
		if e.watchedConn(host, pr.App, procsOK) {
			a.d.Watched = true
		}
	}
	out := make([]model.Discovered, 0, len(by))
	for _, a := range by {
		best := ""
		for n, k := range a.nodes {
			if best == "" || k > a.nodes[best] || (k == a.nodes[best] && n < best) {
				best = n
			}
		}
		a.d.Node = best
		a.d.Apps = sortedKeys(a.apps)
		out = append(out, a.d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Down != out[j].Down {
			return out[i].Down > out[j].Down
		}
		return out[i].Host < out[j].Host
	})
	return out, nil
}
