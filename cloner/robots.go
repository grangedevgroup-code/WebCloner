package cloner

import (
	"bufio"
	"io"
	"strings"
)

type robotsRules struct {
	allow    []string
	disallow []string
}

func parseRobots(r io.Reader, agent string) *robotsRules {
	agent = strings.ToLower(agent)
	type group struct {
		agents []string
		rules  robotsRules
	}
	var groups []*group
	var cur *group
	lastWasAgent := false
	sc := bufio.NewScanner(io.LimitReader(r, 512<<10))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		switch key {
		case "user-agent":
			if cur == nil || !lastWasAgent {
				cur = &group{}
				groups = append(groups, cur)
			}
			cur.agents = append(cur.agents, strings.ToLower(val))
			lastWasAgent = true
		case "allow", "disallow":
			lastWasAgent = false
			if cur == nil || val == "" {
				continue
			}
			if key == "allow" {
				cur.rules.allow = append(cur.rules.allow, val)
			} else {
				cur.rules.disallow = append(cur.rules.disallow, val)
			}
		default:
			lastWasAgent = false
		}
	}
	var star *robotsRules
	for _, g := range groups {
		for _, a := range g.agents {
			if a != "*" && agent != "" && strings.Contains(agent, a) {
				return &g.rules
			}
			if a == "*" && star == nil {
				star = &g.rules
			}
		}
	}
	if star == nil {
		return &robotsRules{}
	}
	return star
}

func robotsMatch(pattern, p string) (bool, int) {
	anchored := strings.HasSuffix(pattern, "$")
	pattern = strings.TrimSuffix(pattern, "$")
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(p, parts[0]) {
		return false, 0
	}
	pos := len(parts[0])
	for _, part := range parts[1:] {
		i := strings.Index(p[pos:], part)
		if i < 0 {
			return false, 0
		}
		pos += i + len(part)
	}
	if anchored && len(parts) == 1 && pos != len(p) {
		return false, 0
	}
	if anchored && len(parts) > 1 && !strings.HasSuffix(p, parts[len(parts)-1]) {
		return false, 0
	}
	return true, len(pattern)
}

func (r *robotsRules) allowed(p string) bool {
	if r == nil {
		return true
	}
	best, allow := -1, true
	for _, d := range r.disallow {
		if ok, n := robotsMatch(d, p); ok && n > best {
			best, allow = n, false
		}
	}
	for _, a := range r.allow {
		if ok, n := robotsMatch(a, p); ok && n >= best {
			best, allow = n, true
		}
	}
	return allow
}
