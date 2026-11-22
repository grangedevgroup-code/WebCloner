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
	for _, cand := range splitSrcset(v) {
		fields := strings.Fields(cand)
		if len(fields) == 0 {
			continue
		}
		if repl, ok := fn(fields[0], refResource); ok {
			fields[0] = repl
			changed = true
		}
		out = append(out, strings.Join(fields, " "))
	}
	return strings.Join(out, ", "), changed
}

func splitSrcset(v string) []string {
	var parts []string
	var cur strings.Builder
	inURL := true
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c == ',' && !inURL:
			parts = append(parts, cur.String())
			cur.Reset()
			inURL = true
			continue
		case c == ',' && inURL && (i+1 == len(v) || v[i+1] == ' '):
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		case c == ' ' || c == '\t' || c == '\n':
			if strings.TrimSpace(cur.String()) != "" {
				inURL = false
			}
		}
		cur.WriteByte(c)
	}
	if strings.TrimSpace(cur.String()) != "" {
		parts = append(parts, cur.String())
	}
	return parts
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
