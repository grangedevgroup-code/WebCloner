package cloner

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var png1x1 = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\xff\xff?\x00\x05\xfe\x02\xfe\xa7\x35\x81\x84\x00\x00\x00\x00IEND\xaeB`\x82")

func testSite(t *testing.T) (*httptest.Server, *httptest.Server) {
	t.Helper()
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/font.woff2":
			w.Header().Set("Content-Type", "font/woff2")
			w.Write([]byte("wOF2fake"))
		case "/photo":
			w.Header().Set("Content-Type", "image/png")
			w.Write(png1x1)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(cdn.Close)
	mux := http.NewServeMux()
	page := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, body)
		}
	}
	mux.HandleFunc("/{$}", page(`<!doctype html><html><head>
<link rel="stylesheet" href="/css/site.css?v=3" integrity="sha384-x" crossorigin="anonymous">
<link rel="icon" href="favicon.ico">
<link rel="canonical" href="/">
</head><body>
<a href="/about">About</a>
<a href="/shop/item?id=7#top">Item</a>
<a href="/private/secret">Secret</a>
<a href="mailto:a@b.c">Mail</a>
<a href="#here">Jump</a>
<img src="/img/a b.png" srcset="/img/a%20b.png 1x, CDN/photo 2x">
<div style="background:url('/img/bg.png')"></div>
<a href="/missing">Missing</a>
</body></html>`))
	mux.HandleFunc("/about", page(`<html><body><a href="/">Home</a><a href="/about/team/">Team</a><img src="img/a%20b.png"></body></html>`))
	mux.HandleFunc("/about/team/", page(`<html><body><a href="../../">Home</a><a href="/about#x">About</a><a href="/index.html">Index</a></body></html>`))
	mux.HandleFunc("/shop/item", page(`<html><body>Item <a href="/con">Con</a></body></html>`))
	mux.HandleFunc("/con", page(`<html><body>reserved name</body></html>`))
	mux.HandleFunc("/private/secret", page(`<html><body>secret</body></html>`))
	mux.HandleFunc("/css/site.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		fmt.Fprint(w, `@import "more.css"; body{background:url(../img/bg.png)} @font-face{src:url("CDN/font.woff2")}`)
	})
	mux.HandleFunc("/css/more.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		fmt.Fprint(w, `p{color:red}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/img/a b.png", "/img/bg.png", "/favicon.ico":
			w.Header().Set("Content-Type", "image/png")
			w.Write(png1x1)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "User-agent: *\nDisallow: /private/\n")
	})
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(&cdnWriter{ResponseWriter: w, cdn: cdn.URL}, r)
	}))
	t.Cleanup(site.Close)
	return site, cdn
}

type cdnWriter struct {
	http.ResponseWriter
	cdn string
}

func (c *cdnWriter) Write(b []byte) (int, error) {
	out := strings.ReplaceAll(string(b), "CDN/", c.cdn+"/")
	if _, err := c.ResponseWriter.Write([]byte(out)); err != nil {
		return 0, err
	}
	return len(b), nil
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected %s: %v", path, err)
	}
	return string(b)
}

func TestCloneSite(t *testing.T) {
	site, cdn := testSite(t)
	out := t.TempDir()
	res, err := Clone(context.Background(), Options{URL: site.URL, OutDir: out, FollowRobots: true})
	if err != nil {
		t.Fatal(err)
	}
	su, _ := url.Parse(site.URL)
	cu, _ := url.Parse(cdn.URL)
	root := filepath.Join(out, hostDir(su.Host))
	if res.Root != root || res.Index != filepath.Join(root, "index.html") {
		t.Fatalf("root %q index %q", res.Root, res.Index)
	}
	index := read(t, filepath.Join(root, "index.html"))
	for _, want := range []string{
		`href="about/index.html"`,
		`href="css/site-q`,
		`href="favicon.ico"`,
		`src="img/a%20b.png"`,
		`_external/` + hostDir(cu.Host) + `/photo.png 2x`,
		`url(&#34;img/bg.png&#34;)`,
		`href="mailto:a@b.c"`,
		`href="#here"`,
		`href="` + site.URL + `/private/secret"`,
		`href="` + site.URL + `/missing"`,
		`#top"`,
	} {
		if !strings.Contains(index, want) {
			t.Errorf("index.html lacks %s\n%s", want, index)
		}
	}
	if strings.Contains(index, "integrity") || strings.Contains(index, "crossorigin") {
		t.Error("integrity or crossorigin kept on a local stylesheet")
	}
	about := read(t, filepath.Join(root, "about", "index.html"))
	for _, want := range []string{`href="../index.html"`, `href="team/index.html"`, `src="../img/a%20b.png"`} {
		if !strings.Contains(about, want) {
			t.Errorf("about lacks %s\n%s", want, about)
		}
	}
	team := read(t, filepath.Join(root, "about", "team", "index.html"))
	if strings.Count(team, `href="../../index.html"`) != 2 {
		t.Errorf("/index.html not treated as the home page\n%s", team)
	}
	if extra, _ := filepath.Glob(filepath.Join(root, "index-*.html")); len(extra) > 0 {
		t.Errorf("home page saved twice: %v", extra)
	}
	if !strings.Contains(team, `href="../../index.html"`) || !strings.Contains(team, `href="../index.html#x"`) {
		t.Errorf("team links wrong\n%s", team)
	}
	matches, _ := filepath.Glob(filepath.Join(root, "css", "site-q*.css"))
	if len(matches) != 1 {
		t.Fatalf("css files %v", matches)
	}
	css := read(t, matches[0])
	for _, want := range []string{`@import "more.css"`, `url("../img/bg.png")`, `url("../_external/` + hostDir(cu.Host) + `/font.woff2")`} {
		if !strings.Contains(css, want) {
			t.Errorf("css lacks %s\n%s", want, css)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "private")); !os.IsNotExist(err) {
		t.Error("robots.txt disallowed page was saved")
	}
	if _, err := os.Stat(filepath.Join(root, "_con", "index.html")); err != nil {
		t.Error("reserved Windows name was not renamed")
	}
	if res.Stats.Failed != 1 || res.Stats.Skipped != 1 {
		t.Errorf("stats %+v", res.Stats)
	}
	if res.Stats.Pages != 5 {
		t.Errorf("pages %d", res.Stats.Pages)
	}
}

func TestPageLimit(t *testing.T) {
	site, _ := testSite(t)
	res, err := Clone(context.Background(), Options{URL: site.URL, OutDir: t.TempDir(), MaxPages: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.Pages != 2 {
		t.Fatalf("pages %d", res.Stats.Pages)
	}
}

func TestStop(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		time.Sleep(20 * time.Millisecond)
		fmt.Fprintf(w, `<a href="/p%d">a</a><a href="/q%d">b</a>`, n, n)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	done := make(chan *Result)
	go func() {
		res, err := Clone(ctx, Options{URL: srv.URL, OutDir: t.TempDir()})
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	select {
	case res := <-done:
		if res == nil || !res.Stopped || res.Stats.Pages == 0 {
			t.Fatalf("result %+v", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stop did not end the clone")
	}
}

func TestLocalPath(t *testing.T) {
	site, _ := url.Parse("https://www.example.com/")
	cases := []struct{ in, ctype, want string }{
		{"https://example.com/", "text/html", "index.html"},
		{"https://www.example.com/a/b", "text/html", "a/b/index.html"},
		{"https://www.example.com/a/b.html", "text/html", "a/b.html"},
		{"https://www.example.com/x/../../etc/passwd", "text/plain", "x/etc/passwd.txt"},
		{"https://www.example.com/pic", "image/jpeg", "pic.jpg"},
		{"https://www.example.com/a:b*c.png", "image/png", "a_b_c.png"},
		{"https://cdn.other.net/f.css?x=1", "text/css", "_external/cdn.other.net/f-q" + shortHash("x=1") + ".css"},
		{"https://www.example.com/aux.png", "image/png", "_aux.png"},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.in)
		got := filepath.ToSlash(localPath(u, c.ctype, site))
		if got != c.want {
			t.Errorf("%s: got %s want %s", c.in, got, c.want)
		}
	}
	long, _ := url.Parse("https://www.example.com/" + strings.Repeat("é", 100) + ".png")
	if seg := filepath.Base(localPath(long, "image/png", site)); len(seg) > maxSegment || !strings.HasSuffix(seg, ".png") {
		t.Errorf("long segment %q", seg)
	}
}

func TestSrcset(t *testing.T) {
	got := parseSrcset("a.png 1x,b, c.png 2x, d(1).png 300w")
	want := [][2]string{{"a.png", "1x"}, {"b", ""}, {"c.png", "2x"}, {"d(1).png", "300w"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v", got)
	}
	got = parseSrcset("https://x/img,w_200.jpg 200w, https://x/img,w_400.jpg 400w")
	if len(got) != 2 || got[1][0] != "https://x/img,w_400.jpg" {
		t.Fatalf("comma urls %v", got)
	}
}

func TestRobots(t *testing.T) {
	r := parseRobots(strings.NewReader("User-agent: Googlebot\nDisallow: /\n\nUser-agent: *\nDisallow: /admin\nAllow: /admin/public\nDisallow: /*.pdf$\n"), "WebCloner")
	for p, want := range map[string]bool{"/": true, "/admin": false, "/admin/x": false, "/admin/public/a": true, "/doc.pdf": false, "/doc.pdf?x": true} {
		if r.allowed(p) != want {
			t.Errorf("%s: want %v", p, want)
		}
	}
}

func TestParseSiteURL(t *testing.T) {
	u, err := ParseSiteURL(" example.com/shop ")
	if err != nil || u.String() != "https://example.com/shop" {
		t.Fatalf("%v %v", u, err)
	}
	for _, bad := range []string{"", "ftp://x.com", "http://"} {
		if _, err := ParseSiteURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestRenderJS(t *testing.T) {
	browser := FindBrowser()
	if browser == "" || os.Getenv("WEBCLONER_BROWSER_TEST") == "" {
		t.Skip("set WEBCLONER_BROWSER_TEST=1 with Chrome, Edge, Brave or Chromium installed")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body><div id="app"></div><script>document.getElementById('app').innerHTML='<a href="/made-by-js">js</a><img src="/js.png">'</script></body></html>`)
		case "/made-by-js":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body>hello</body></html>`)
		case "/js.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(png1x1)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	res, err := Clone(context.Background(), Options{URL: srv.URL, OutDir: t.TempDir(), RenderJS: true, BrowserPath: browser})
	if err != nil {
		t.Fatal(err)
	}
	index := read(t, res.Index)
	if strings.Contains(index, "<script") || !strings.Contains(index, `href="made-by-js/index.html"`) || !strings.Contains(index, `src="js.png"`) {
		t.Fatalf("rendered page wrong\n%s", index)
	}
	if res.Stats.Pages != 2 || res.Stats.Files != 1 {
		t.Fatalf("stats %+v", res.Stats)
	}
}
