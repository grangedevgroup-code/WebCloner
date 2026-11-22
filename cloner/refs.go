package cloner

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

type refKind int

const (
	refResource refKind = iota
	refPage
)

type refFunc func(raw string, kind refKind) (string, bool)

var cssURL = regexp.MustCompile(`(?i)url\(\s*(?:"([^"]*)"|'([^']*)'|([^)'"\s]*))\s*\)`)
var cssImport = regexp.MustCompile(`(?i)@import\s+(?:"([^"]*)"|'([^']*)')`)

func walkCSS(css string, fn refFunc) string {
	css = cssImport.ReplaceAllStringFunc(css, func(m string) string {
		sub := cssImport.FindStringSubmatch(m)
		raw := sub[1] + sub[2]
		if repl, ok := fn(raw, refResource); ok {
			return `@import "` + repl + `"`
		}
		return m
	})
	return cssURL.ReplaceAllStringFunc(css, func(m string) string {
		sub := cssURL.FindStringSubmatch(m)
		raw := sub[1] + sub[2] + sub[3]
		if repl, ok := fn(raw, refResource); ok {
			return `url("` + repl + `")`
		}
		return m
	})
}

func walkSrcset(v string, fn refFunc) (string, bool) {
	changed := false
	var out []string
	for _, c := range parseSrcset(v) {
		u := c[0]
		if repl, ok := fn(u, refResource); ok {
			u = repl
			changed = true
		}
		if c[1] != "" {
			u += " " + c[1]
		}
		out = append(out, u)
	}
	return strings.Join(out, ", "), changed
}

func parseSrcset(v string) [][2]string {
	var out [][2]string
	i := 0
	isSpace := func(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' }
	for i < len(v) {
		for i < len(v) && (isSpace(v[i]) || v[i] == ',') {
			i++
		}
		if i >= len(v) {
			break
		}
		start := i
		for i < len(v) && !isSpace(v[i]) {
			i++
		}
		u := v[start:i]
		if strings.HasSuffix(u, ",") {
			out = append(out, [2]string{strings.TrimRight(u, ","), ""})
			continue
		}
		dstart := i
		depth := 0
		for i < len(v) {
			if v[i] == '(' {
				depth++
			} else if v[i] == ')' && depth > 0 {
				depth--
			} else if v[i] == ',' && depth == 0 {
				break
			}
			i++
		}
		out = append(out, [2]string{u, strings.TrimSpace(v[dstart:i])})
	}
	return out
}

var resourceRels = map[string]bool{
	"stylesheet": true, "icon": true, "shortcut": true, "apple-touch-icon": true,
	"apple-touch-icon-precomposed": true, "mask-icon": true, "preload": true,
	"manifest": true, "image_src": true, "modulepreload": true,
}

func linkIsResource(n *html.Node) bool {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, "rel") {
			for _, r := range strings.Fields(strings.ToLower(a.Val)) {
				if resourceRels[r] {
					return true
				}
			}
		}
	}
	return false
}

func attrKind(tag, key string, n *html.Node) (refKind, bool) {
	switch key {
	case "style", "srcset", "data-srcset":
		return refResource, false
	}
	switch tag {
	case "a", "area":
		if key == "href" {
			return refPage, true
		}
	case "iframe", "frame":
		if key == "src" {
			return refPage, true
		}
	case "link":
		if key == "href" && linkIsResource(n) {
			return refResource, true
		}
	case "img", "source", "audio", "video", "track", "embed", "input", "script":
		if key == "src" || key == "data-src" || key == "poster" {
			return refResource, true
		}
	case "object":
		if key == "data" {
			return refResource, true
		}
	case "body", "table", "td", "th":
		if key == "background" {
			return refResource, true
		}
	case "image", "use":
		if key == "href" || key == "xlink:href" {
			return refResource, true
		}
	}
	return refResource, false
}

func walkHTML(doc *html.Node, fn refFunc) {
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)
			for i, a := range n.Attr {
				key := strings.ToLower(a.Key)
				switch key {
				case "style":
					n.Attr[i].Val = walkCSS(a.Val, fn)
					continue
				case "srcset", "data-srcset", "imagesrcset":
					if v, ok := walkSrcset(a.Val, fn); ok {
						n.Attr[i].Val = v
					}
					continue
				}
				kind, ok := attrKind(tag, key, n)
				if !ok {
					continue
				}
				if repl, ok := fn(a.Val, kind); ok {
					n.Attr[i].Val = repl
				}
			}
			if tag == "style" {
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.TextNode {
						c.Data = walkCSS(c.Data, fn)
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(doc)
}

func findBase(doc *html.Node) (string, *html.Node) {
	var href string
	var node *html.Node
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if node != nil {
			return
		}
		if n.Type == html.ElementNode && strings.EqualFold(n.Data, "base") {
			for _, a := range n.Attr {
				if strings.EqualFold(a.Key, "href") {
					href, node = a.Val, n
					return
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(doc)
	return href, node
}

func stripScripts(doc *html.Node) {
	var remove []*html.Node
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "script":
				typ := ""
				for _, a := range n.Attr {
					if strings.EqualFold(a.Key, "type") {
						typ = strings.ToLower(a.Val)
					}
				}
				if typ != "application/ld+json" {
					remove = append(remove, n)
				}
			case "link":
				for _, a := range n.Attr {
					if strings.EqualFold(a.Key, "rel") && strings.Contains(strings.ToLower(a.Val), "modulepreload") {
						remove = append(remove, n)
					}
				}
			case "noscript":
				remove = append(remove, n)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(doc)
	for _, n := range remove {
		if n.Parent != nil {
			n.Parent.RemoveChild(n)
		}
	}
}

func setUTF8Charset(doc *html.Node) {
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode && strings.EqualFold(n.Data, "meta") {
			for i, a := range n.Attr {
				switch strings.ToLower(a.Key) {
				case "charset":
					n.Attr[i].Val = "utf-8"
				case "content":
					for _, b := range n.Attr {
						if strings.EqualFold(b.Key, "http-equiv") && strings.EqualFold(b.Val, "content-type") {
							n.Attr[i].Val = "text/html; charset=utf-8"
						}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(doc)
}

func isLocalRef(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "#") || strings.HasPrefix(v, "//") {
		return false
	}
	return !strings.Contains(strings.SplitN(v, "/", 2)[0], ":")
}

func dropIntegrity(doc *html.Node) {
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode {
			local := false
			for _, a := range n.Attr {
				switch strings.ToLower(a.Key) {
				case "src", "href", "poster", "data":
					if isLocalRef(a.Val) {
						local = true
					}
				}
			}
			if local {
				kept := n.Attr[:0]
				for _, a := range n.Attr {
					k := strings.ToLower(a.Key)
					if k != "integrity" && k != "crossorigin" {
						kept = append(kept, a)
					}
				}
				n.Attr = kept
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(doc)
}
