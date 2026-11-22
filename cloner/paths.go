package cloner

import (
	"crypto/sha1"
	"encoding/hex"
	"mime"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxSegment = 80

var mimeExt = map[string]string{
	"text/html":                 ".html",
	"application/xhtml+xml":     ".html",
	"text/css":                  ".css",
	"text/javascript":           ".js",
	"application/javascript":    ".js",
	"application/x-javascript":  ".js",
	"application/json":          ".json",
	"application/manifest+json": ".webmanifest",
	"application/pdf":           ".pdf",
	"application/xml":           ".xml",
	"text/xml":                  ".xml",
	"text/plain":                ".txt",
	"image/jpeg":                ".jpg",
	"image/png":                 ".png",
	"image/gif":                 ".gif",
	"image/webp":                ".webp",
	"image/avif":                ".avif",
	"image/svg+xml":             ".svg",
	"image/x-icon":              ".ico",
	"image/vnd.microsoft.icon":  ".ico",
	"font/woff2":                ".woff2",
	"font/woff":                 ".woff",
	"font/ttf":                  ".ttf",
	"font/otf":                  ".otf",
	"application/font-woff":     ".woff",
	"application/font-woff2":    ".woff2",
	"video/mp4":                 ".mp4",
	"video/webm":                ".webm",
	"audio/mpeg":                ".mp3",
	"audio/ogg":                 ".ogg",
	"audio/wav":                 ".wav",
}

var windowsReserved = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

func mediaType(contentType string) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	}
	return mt
}

func isHTMLType(contentType string) bool {
	mt := mediaType(contentType)
	return mt == "text/html" || mt == "application/xhtml+xml"
}

func isCSSType(contentType string) bool {
	return mediaType(contentType) == "text/css"
}

func extForType(contentType string) string {
	mt := mediaType(contentType)
	if ext, ok := mimeExt[mt]; ok {
		return ext
	}
	if exts, err := mime.ExtensionsByType(mt); err == nil && len(exts) > 0 {
		return exts[0]
	}
	return ""
}

func shortHash(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}

func sanitizeSegment(seg string) string {
	if decoded, err := url.PathUnescape(seg); err == nil {
		seg = decoded
	}
	var b strings.Builder
	for _, r := range seg {
		switch {
		case r < 0x20, r == 0x7f:
			b.WriteRune('_')
		case strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimRight(b.String(), ". ")
	if out == "" || out == "." || out == ".." {
		out = "_"
	}
	base := strings.ToLower(strings.SplitN(out, ".", 2)[0])
	if windowsReserved[base] {
		out = "_" + out
	}
	if len(out) > maxSegment {
		ext := path.Ext(out)
		if len(ext) > 12 || !utf8.ValidString(ext) {
			ext = ""
		}
		cut := maxSegment - len(ext) - 9
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		out = out[:cut] + "-" + shortHash(out) + ext
	}
	return out
}

func hostDir(h string) string {
	return sanitizeSegment(strings.ReplaceAll(h, ":", "_"))
}

func localPath(u *url.URL, contentType string, site *url.URL) string {
	var parts []string
	if !sameSite(u, site) {
		parts = append(parts, "_external", hostDir(u.Host))
	}
	raw := u.EscapedPath()
	trailing := raw == "" || strings.HasSuffix(raw, "/")
	var segs []string
	for s := range strings.SplitSeq(raw, "/") {
		if s == "" || s == "." || s == ".." {
			continue
		}
		segs = append(segs, sanitizeSegment(s))
	}
	html := isHTMLType(contentType)
	var name string
	if trailing || len(segs) == 0 {
		if html {
			name = "index.html"
		} else {
			name = "index" + extForType(contentType)
		}
	} else {
		name = segs[len(segs)-1]
		segs = segs[:len(segs)-1]
		ext := strings.ToLower(path.Ext(name))
		if html {
			if ext != ".html" && ext != ".htm" {
				segs = append(segs, name)
				name = "index.html"
			}
		} else if ext == "" || len(ext) > 6 {
			if want := extForType(contentType); want != "" {
				name += want
			}
		}
	}
	if u.RawQuery != "" {
		ext := path.Ext(name)
		name = strings.TrimSuffix(name, ext) + "-q" + shortHash(u.RawQuery) + ext
	}
	parts = append(parts, segs...)
	parts = append(parts, name)
	return filepath.Join(parts...)
}

func relativeRef(fromFile, toFile string) string {
	rel, err := filepath.Rel(filepath.Dir(fromFile), toFile)
	if err != nil {
		return ""
	}
	segs := strings.Split(filepath.ToSlash(rel), "/")
	for i, s := range segs {
		if s != ".." {
			segs[i] = url.PathEscape(s)
		}
	}
	return strings.Join(segs, "/")
}

func sameSite(u, site *url.URL) bool {
	return strings.TrimPrefix(strings.ToLower(u.Host), "www.") == strings.TrimPrefix(strings.ToLower(site.Host), "www.")
}

func normalize(raw string, base *url.URL) (*url.URL, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "#") {
		return nil, false
	}
	lower := strings.ToLower(raw)
	for _, p := range []string{"data:", "javascript:", "mailto:", "tel:", "about:", "blob:", "sms:", "file:"} {
		if strings.HasPrefix(lower, p) {
			return nil, false
		}
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return nil, false
	}
	u := base.ResolveReference(ref)
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, false
	}
	u.Fragment = ""
	u.RawFragment = ""
	u.Host = strings.ToLower(u.Host)
	if (u.Scheme == "http" && u.Port() == "80") || (u.Scheme == "https" && u.Port() == "443") {
		u.Host = u.Hostname()
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u, true
}
