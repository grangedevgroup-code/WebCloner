package cloner

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestScreenshot(t *testing.T) {
	target, out := os.Getenv("WEBCLONER_SHOT_URL"), os.Getenv("WEBCLONER_SHOT_OUT")
	if target == "" || out == "" {
		t.Skip("set WEBCLONER_SHOT_URL and WEBCLONER_SHOT_OUT to capture a page")
	}
	r := newRenderer(FindBrowser(), DefaultUserAgent, 1)
	defer r.close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := r.render(ctx, target); err != nil {
		t.Fatal(err)
	}
	tab, cancelTab := chromedp.NewContext(r.browser)
	defer cancelTab()
	if err := chromedp.Do(tab, chromedp.Navigate(target)); err != nil {
		t.Fatal(err)
	}
	if _, err := chromedp.Run(tab, chromedp.Evaluate[bool](settleScript, chromedp.EvalAwaitPromise)); err != nil {
		t.Fatal(err)
	}
	png, err := chromedp.Run(tab, chromedp.FullScreenshot(80))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(string(png), "data:") {
		png, _ = base64.StdEncoding.DecodeString(strings.SplitN(string(png), ",", 2)[1])
	}
	if err := os.WriteFile(out, png, 0o644); err != nil {
		t.Fatal(err)
	}
}
