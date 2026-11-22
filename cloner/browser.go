package cloner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

func FindBrowser() string {
	var names, paths []string
	switch runtime.GOOS {
	case "windows":
		names = []string{"chrome.exe", "msedge.exe", "brave.exe", "chromium.exe"}
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			root := os.Getenv(env)
			if root == "" {
				continue
			}
			paths = append(paths,
				filepath.Join(root, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(root, `Microsoft\Edge\Application\msedge.exe`),
				filepath.Join(root, `BraveSoftware\Brave-Browser\Application\brave.exe`),
				filepath.Join(root, `Chromium\Application\chrome.exe`),
			)
		}
	case "darwin":
		for _, app := range []string{
			"Google Chrome.app/Contents/MacOS/Google Chrome",
			"Chromium.app/Contents/MacOS/Chromium",
			"Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"Brave Browser.app/Contents/MacOS/Brave Browser",
		} {
			paths = append(paths, filepath.Join("/Applications", app))
			if home, err := os.UserHomeDir(); err == nil {
				paths = append(paths, filepath.Join(home, "Applications", app))
			}
		}
		names = []string{"google-chrome", "chromium"}
	default:
		names = []string{
			"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
			"brave", "brave-browser", "microsoft-edge", "microsoft-edge-stable",
		}
		paths = []string{"/snap/bin/chromium", "/var/lib/flatpak/exports/bin/com.google.Chrome"}
	}
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

type renderer struct {
	mu        sync.Mutex
	execPath  string
	userAgent string
	alloc     context.Context
	browser   context.Context
	cancel    []context.CancelFunc
	slots     chan struct{}
}

func newRenderer(execPath, userAgent string, parallel int) *renderer {
	return &renderer{execPath: execPath, userAgent: userAgent, slots: make(chan struct{}, parallel)}
}

func (r *renderer) start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.browser != nil {
		return nil
	}
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		chromedp.ExecPath(r.execPath),
		chromedp.UserAgent(r.userAgent),
		chromedp.WindowSize(1366, 900),
		chromedp.Flag("mute-audio", true),
	)
	alloc, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	browser, cancelBrowser := chromedp.NewContext(alloc)
	if err := chromedp.Do(browser); err != nil {
		cancelBrowser()
		cancelAlloc()
		return fmt.Errorf("could not start the browser at %s: %w", r.execPath, err)
	}
	r.alloc, r.browser = alloc, browser
	r.cancel = []context.CancelFunc{cancelBrowser, cancelAlloc}
	return nil
}

const settleScript = `(async () => {
  const wait = ms => new Promise(r => setTimeout(r, ms));
  const step = Math.max(300, Math.floor(innerHeight * 0.7));
  for (let y = 0; y < document.documentElement.scrollHeight && y < 60000; y += step) {
    scrollTo(0, y);
    await wait(150);
  }
  scrollTo(0, 0);
  const deadline = Date.now() + 8000;
  while (Date.now() < deadline) {
    const imgs = [...document.images].every(i => i.complete);
    if (imgs && document.fonts.status === 'loaded') break;
    await wait(200);
  }
  await wait(500);
  return true;
})()`

const snapshotScript = `(() => {
  const frozen = new Set();
  for (const a of document.getAnimations()) {
    try {
      const target = a.effect && a.effect.target;
      if (!target || !target.isConnected || target instanceof CSSPseudoElement) continue;
      const timed = !a.timeline || a.timeline instanceof DocumentTimeline;
      const finite = a.effect.getTiming().iterations !== Infinity;
      if (!timed || !finite) continue;
      a.finish();
      a.commitStyles();
      if (typeof CSSAnimation !== 'undefined' && a instanceof CSSAnimation) frozen.add(target);
    } catch (e) {}
  }
  for (const el of frozen) el.style.setProperty('animation', 'none', 'important');
  for (const img of document.images) {
    if (img.loading === 'lazy') img.removeAttribute('loading');
  }
  const dt = document.doctype ? '<!DOCTYPE ' + document.doctype.name + '>\n' : '';
  return dt + document.documentElement.outerHTML;
})()`

func (r *renderer) render(ctx context.Context, pageURL string) (string, error) {
	if err := r.start(ctx); err != nil {
		return "", err
	}
	select {
	case r.slots <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-r.slots }()
	tab, cancelTab := chromedp.NewContext(r.browser)
	defer cancelTab()
	tab, cancelTimeout := context.WithTimeout(tab, 90*time.Second)
	defer cancelTimeout()
	stop := context.AfterFunc(ctx, cancelTab)
	defer stop()
	err := chromedp.Do(tab,
		chromedp.Navigate(pageURL),
		chromedp.WaitReady(chromedp.CSS("body")),
	)
	if err != nil {
		return "", err
	}
	if _, err := chromedp.Run(tab, chromedp.Evaluate[bool](settleScript, chromedp.EvalAwaitPromise)); err != nil {
		return "", err
	}
	return chromedp.Run(tab, chromedp.Evaluate[string](snapshotScript))
}

func (r *renderer) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.cancel {
		c()
	}
	r.browser, r.alloc, r.cancel = nil, nil, nil
}
