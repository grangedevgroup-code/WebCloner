package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/grangedevgroup-code/WebCloner/cloner"
)

const (
	appID      = "io.github.grangedevgroup_code.webcloner"
	maxLogRows = 2000
)

type gui struct {
	app    fyne.App
	win    fyne.Window
	prefs  fyne.Preferences
	url    *widget.Entry
	folder *widget.Entry
	browse *widget.Button
	limit  *widget.Entry
	js     *widget.Check
	robots *widget.Check
	status *widget.Label
	counts *widget.Label
	bar    *widget.ProgressBarInfinite
	logs   *widget.List
	start  *widget.Button
	open   *widget.Button
	view   *widget.Button

	mu      sync.Mutex
	lines   []string
	latest  cloner.Stats
	dirty   bool
	cancel  context.CancelFunc
	running bool
	result  *cloner.Result
	browser string
}

func defaultFolder() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "WebCloner"
	}
	if st, err := os.Stat(filepath.Join(home, "Downloads")); err == nil && st.IsDir() {
		return filepath.Join(home, "Downloads", "WebCloner")
	}
	return filepath.Join(home, "WebCloner")
}

func runGUI() {
	a := app.NewWithID(appID)
	g := &gui{app: a, prefs: a.Preferences(), browser: cloner.FindBrowser()}
	g.win = a.NewWindow("WebCloner")
	g.build()
	g.win.SetCloseIntercept(g.exit)
	g.win.Resize(fyne.NewSize(680, 560))
	g.win.CenterOnScreen()
	g.win.Canvas().Focus(g.url)
	go g.refreshLoop()
	g.win.ShowAndRun()
}

func (g *gui) build() {
	g.url = widget.NewEntry()
	g.url.SetPlaceHolder("https://example.com")
	g.url.SetText(g.prefs.String("url"))
	g.url.OnSubmitted = func(string) { g.toggle() }

	g.folder = widget.NewEntry()
	g.folder.SetText(g.prefs.StringWithFallback("folder", defaultFolder()))
	g.browse = widget.NewButtonWithIcon("Choose", theme.FolderOpenIcon(), g.chooseFolder)

	g.limit = widget.NewEntry()
	g.limit.SetPlaceHolder("No limit")
	g.limit.SetText(g.prefs.String("limit"))
	g.limit.Validator = func(s string) error {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err != nil || n < 0 {
			return fmt.Errorf("enter a whole number")
		}
		return nil
	}

	jsLabel := "Run each page's scripts first, for sites built with Wix, Squarespace, React and similar"
	g.js = widget.NewCheck(jsLabel, nil)
	if g.browser == "" {
		g.js.Text = "Run page scripts first (install Chrome, Edge, Brave or Chromium to use this)"
		g.js.Disable()
	} else {
		g.js.SetChecked(g.prefs.BoolWithFallback("js", true))
	}
	g.robots = widget.NewCheck("Skip pages the site's robots.txt asks tools to skip", nil)
	g.robots.SetChecked(g.prefs.BoolWithFallback("robots", true))

	form := widget.NewForm(
		widget.NewFormItem("Website", g.url),
		widget.NewFormItem("Save to", container.NewBorder(nil, nil, nil, g.browse, g.folder)),
		widget.NewFormItem("Page limit", g.limit),
	)

	g.status = widget.NewLabelWithStyle("Enter a website and press Start.", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	g.status.Wrapping = fyne.TextWrapWord
	g.counts = widget.NewLabel("")
	g.bar = widget.NewProgressBarInfinite()
	g.bar.Stop()
	g.bar.Hide()

	g.logs = widget.NewList(
		func() int {
			g.mu.Lock()
			defer g.mu.Unlock()
			return len(g.lines)
		},
		func() fyne.CanvasObject {
			l := widget.NewLabel("")
			l.Truncation = fyne.TextTruncateEllipsis
			return l
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			g.mu.Lock()
			text := ""
			if id < len(g.lines) {
				text = g.lines[id]
			}
			g.mu.Unlock()
			o.(*widget.Label).SetText(text)
		},
	)

	g.start = widget.NewButtonWithIcon("Start", theme.MediaPlayIcon(), g.toggle)
	g.start.Importance = widget.HighImportance
	g.open = widget.NewButtonWithIcon("Open folder", theme.FolderIcon(), g.openFolder)
	g.open.Disable()
	g.view = widget.NewButtonWithIcon("View copy", theme.ComputerIcon(), g.viewCopy)
	g.view.Disable()
	exit := widget.NewButtonWithIcon("Exit", theme.LogoutIcon(), g.exit)

	top := container.NewVBox(form, g.js, g.robots, widget.NewSeparator(), g.status, g.counts, g.bar)
	buttons := container.NewHBox(g.start, g.open, g.view, layoutSpacer(), exit)
	g.win.SetContent(container.NewPadded(container.NewBorder(top, buttons, nil, nil, g.logs)))
}

func layoutSpacer() fyne.CanvasObject {
	return container.NewStack()
}

func (g *gui) chooseFolder() {
	d := dialog.NewFolderOpen(func(lu fyne.ListableURI, err error) {
		if err != nil {
			dialog.ShowError(err, g.win)
			return
		}
		if lu != nil {
			g.folder.SetText(lu.Path())
		}
	}, g.win)
	dir := strings.TrimSpace(g.folder.Text)
	for dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			if lu, err := storage.ListerForURI(storage.NewFileURI(dir)); err == nil {
				d.SetLocation(lu)
			}
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	d.Resize(fyne.NewSize(640, 460))
	d.Show()
}

func (g *gui) toggle() {
	g.mu.Lock()
	running := g.running
	g.mu.Unlock()
	if running {
		g.stop()
		return
	}
	g.begin()
}

func (g *gui) begin() {
	site, err := cloner.ParseSiteURL(g.url.Text)
	if err != nil {
		dialog.ShowError(err, g.win)
		return
	}
	folder := strings.TrimSpace(g.folder.Text)
	if folder == "" {
		dialog.ShowError(fmt.Errorf("choose a folder to save the site in"), g.win)
		return
	}
	if abs, err := filepath.Abs(folder); err == nil {
		folder = abs
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		dialog.ShowError(fmt.Errorf("cannot use %s: %w", folder, err), g.win)
		return
	}
	if err := g.limit.Validate(); err != nil {
		dialog.ShowError(fmt.Errorf("page limit: %w", err), g.win)
		return
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(g.limit.Text))

	g.url.SetText(site.String())
	g.folder.SetText(folder)
	g.prefs.SetString("url", site.String())
	g.prefs.SetString("folder", folder)
	g.prefs.SetString("limit", strings.TrimSpace(g.limit.Text))
	g.prefs.SetBool("robots", g.robots.Checked)
	if !g.js.Disabled() {
		g.prefs.SetBool("js", g.js.Checked)
	}

	ctx, cancel := context.WithCancel(context.Background())
	g.mu.Lock()
	g.running, g.cancel, g.result = true, cancel, nil
	g.lines = nil
	g.latest = cloner.Stats{Phase: "Starting"}
	g.dirty = true
	g.mu.Unlock()

	g.setInputs(false)
	g.start.SetText("Stop")
	g.start.SetIcon(theme.MediaStopIcon())
	g.start.Importance = widget.DangerImportance
	g.start.Refresh()
	g.open.Disable()
	g.view.Disable()
	g.status.SetText("Copying " + site.Host + "…")
	g.bar.Show()
	g.bar.Start()

	opts := cloner.Options{
		URL:          site.String(),
		OutDir:       folder,
		MaxPages:     limit,
		RenderJS:     g.js.Checked && !g.js.Disabled(),
		BrowserPath:  g.browser,
		FollowRobots: g.robots.Checked,
		Log:          g.addLine,
		Progress: func(s cloner.Stats) {
			g.mu.Lock()
			g.latest = s
			g.dirty = true
			g.mu.Unlock()
		},
	}
	go func() {
		res, err := cloner.Clone(ctx, opts)
		cancel()
		fyne.Do(func() { g.finish(res, err) })
	}()
}

func (g *gui) stop() {
	g.mu.Lock()
	cancel := g.cancel
	g.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	g.start.Disable()
	g.start.SetText("Stopping…")
	g.status.SetText("Stopping. Links in the pages saved so far are being fixed…")
}

func (g *gui) finish(res *cloner.Result, err error) {
	g.mu.Lock()
	g.running, g.cancel, g.result = false, nil, res
	if res != nil {
		g.latest = res.Stats
		g.dirty = true
	}
	g.mu.Unlock()
	g.bar.Stop()
	g.bar.Hide()
	g.setInputs(true)
	g.start.Enable()
	g.start.SetText("Start")
	g.start.SetIcon(theme.MediaPlayIcon())
	g.start.Importance = widget.HighImportance
	g.start.Refresh()
	switch {
	case err != nil:
		g.status.SetText("Could not start: " + err.Error())
	case res.Stopped:
		g.status.SetText("Stopped. What was downloaded is in " + res.Root)
	default:
		g.status.SetText("Done. The copy is in " + res.Root)
	}
	if res != nil {
		g.open.Enable()
		if res.Index != "" {
			g.view.Enable()
		}
	}
}

func (g *gui) setInputs(on bool) {
	for _, w := range []fyne.Disableable{g.url, g.folder, g.browse, g.limit, g.robots} {
		if on {
			w.Enable()
		} else {
			w.Disable()
		}
	}
	if g.browser != "" {
		if on {
			g.js.Enable()
		} else {
			g.js.Disable()
		}
	}
}

func (g *gui) addLine(s string) {
	g.mu.Lock()
	g.lines = append(g.lines, time.Now().Format("15:04:05  ")+s)
	if over := len(g.lines) - maxLogRows; over > 0 {
		g.lines = append([]string(nil), g.lines[over:]...)
	}
	g.dirty = true
	g.mu.Unlock()
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

func describe(s cloner.Stats) string {
	text := fmt.Sprintf("%d pages, %d files, %s", s.Pages, s.Files, formatBytes(s.Bytes))
	if s.Failed > 0 {
		text += fmt.Sprintf(", %d failed", s.Failed)
	}
	if s.Skipped > 0 {
		text += fmt.Sprintf(", %d skipped", s.Skipped)
	}
	if s.Phase != "" && s.Phase != "Done" {
		text = s.Phase + ": " + text
	}
	return text
}

func (g *gui) refreshLoop() {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		g.mu.Lock()
		if !g.dirty {
			g.mu.Unlock()
			continue
		}
		g.dirty = false
		stats := g.latest
		rows := len(g.lines)
		g.mu.Unlock()
		fyne.Do(func() {
			g.counts.SetText(describe(stats))
			g.logs.Refresh()
			if rows > 0 {
				g.logs.ScrollToBottom()
			}
		})
	}
}

func (g *gui) openFolder() {
	g.mu.Lock()
	res := g.result
	g.mu.Unlock()
	if res == nil {
		return
	}
	g.openPath(res.Root)
}

func (g *gui) viewCopy() {
	g.mu.Lock()
	res := g.result
	g.mu.Unlock()
	if res == nil || res.Index == "" {
		return
	}
	g.openPath(res.Index)
}

func (g *gui) openPath(p string) {
	u := &url.URL{Scheme: "file", Path: filepath.ToSlash(p)}
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	if err := g.app.OpenURL(u); err != nil {
		dialog.ShowError(err, g.win)
	}
}

func (g *gui) exit() {
	g.mu.Lock()
	running := g.running
	g.mu.Unlock()
	if !running {
		g.app.Quit()
		return
	}
	dialog.ShowConfirm("Exit WebCloner", "A copy is still running. Stop it and exit?", func(ok bool) {
		if !ok {
			return
		}
		g.mu.Lock()
		if g.cancel != nil {
			g.cancel()
		}
		g.mu.Unlock()
		g.app.Quit()
	}, g.win)
}
