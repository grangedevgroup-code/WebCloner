package main

import (
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/grangedevgroup-code/WebCloner/cloner"
)

type uiPump chan func()

func (p uiPump) attach(g *gui) { g.do = func(f func()) { p <- f } }

func (p uiPump) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for !cond() {
		select {
		case f := <-p:
			f()
		case <-tick.C:
		case <-deadline:
			t.Fatal("timed out waiting for " + what)
		}
	}
}

func capture(t *testing.T, g *gui, name string) {
	dir := os.Getenv("WEBCLONER_UI_SHOTS")
	if dir == "" {
		return
	}
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, g.win.Canvas().Capture()); err != nil {
		t.Fatal(err)
	}
}

func TestWindowStartStop(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/slow" {
			<-release
		}
		fmt.Fprint(w, `<html><body><a href="/a">A</a><a href="/slow">S</a></body></html>`)
	}))
	defer srv.Close()
	defer close(release)

	a := test.NewTempApp(t)
	g := newGUI(a, "/usr/bin/chrome-for-test")
	ui := make(uiPump, 16)
	ui.attach(g)
	g.win.Show()
	capture(t, g, "empty")

	out := t.TempDir()
	test.Type(g.url, srv.URL)
	g.folder.SetText(out)
	g.js.SetChecked(false)
	test.Tap(g.start)
	if g.start.Text != "Stop" || !g.url.Disabled() || !g.folder.Disabled() {
		t.Fatalf("running state: button %q url disabled %v", g.start.Text, g.url.Disabled())
	}
	ui.waitFor(t, "first pages", func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.latest.Pages >= 2
	})
	g.mu.Lock()
	g.dirty = false
	g.counts.SetText(describe(g.latest))
	g.mu.Unlock()
	g.logs.Refresh()
	capture(t, g, "running")

	test.Tap(g.start)
	ui.waitFor(t, "buttons", func() bool { return g.start.Text == "Start" && !g.open.Disabled() })
	if g.url.Disabled() || g.result == nil || !g.result.Stopped {
		t.Fatalf("after stop: url disabled %v result %+v", g.url.Disabled(), g.result)
	}
	if _, err := os.Stat(filepath.Join(g.result.Root, "index.html")); err != nil {
		t.Fatal(err)
	}
	g.counts.SetText(describe(g.result.Stats))
	capture(t, g, "stopped")
	if got := a.Preferences().String("folder"); got != out {
		t.Fatalf("folder not remembered: %q", got)
	}
}

func TestBadInput(t *testing.T) {
	a := test.NewTempApp(t)
	g := newGUI(a, "")
	g.win.Show()
	test.Tap(g.start)
	if g.start.Text != "Start" || g.running {
		t.Fatal("started without a website")
	}
	if !g.js.Disabled() {
		t.Fatal("script option enabled with no browser")
	}
	g.url.SetText("example.com")
	g.limit.SetText("abc")
	test.Tap(g.start)
	if g.running {
		t.Fatal("started with a bad page limit")
	}
}

func TestDocsScreenshot(t *testing.T) {
	out := os.Getenv("WEBCLONER_DOCS_SHOT")
	if out == "" {
		t.Skip("set WEBCLONER_DOCS_SHOT to a .png path")
	}
	a := test.NewTempApp(t)
	g := newGUI(a, "/usr/bin/chrome-for-test")
	g.win.Show()
	g.url.SetText("https://example.com/")
	g.folder.SetText("/home/you/Downloads/WebCloner")
	g.status.SetText("Done. The copy is in /home/you/Downloads/WebCloner/example.com")
	g.counts.SetText(describe(cloner.Stats{Pages: 42, Files: 318, Bytes: 27 << 20}))
	g.lines = []string{"18:07:29  Page: https://example.com/", "18:07:30  Page: https://example.com/about", "18:07:31  Page: https://example.com/blog"}
	g.logs.Refresh()
	g.open.Enable()
	g.view.Enable()
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, g.win.Canvas().Capture()); err != nil {
		t.Fatal(err)
	}
}
