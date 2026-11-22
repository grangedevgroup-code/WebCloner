package cloner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

const (
	DefaultUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0 Safari/537.36 WebCloner"
	maxHTMLBytes     = 50 << 20
	maxFileBytes     = 512 << 20
)

type Options struct {
	URL          string
	OutDir       string
	MaxPages     int
	MaxDepth     int
	Workers      int
	RenderJS     bool
	BrowserPath  string
	FollowRobots bool
	UserAgent    string
	Client       *http.Client
	Log          func(string)
	Progress     func(Stats)
}

type Stats struct {
	Pages   int
	Files   int
	Failed  int
	Skipped int
	Queued  int
	Bytes   int64
	Phase   string
}

type Result struct {
	Root    string
	Index   string
	Stats   Stats
	Stopped bool
}

type job struct {
	u     *url.URL
	depth int
	kind  refKind
}

type record struct {
	path string
	base *url.URL
	html bool
}

type cloner struct {
	opts     Options
	site     *url.URL
	root     string
	client   *http.Client
	render   *renderer
	robots   *robotsRules
	q        *queue
	mu       sync.Mutex
	seen     map[string]bool
	saved    map[string]string
	used     map[string]string
	records  []record
	stats    Stats
	pageJobs int
	index    string
}

func Clone(ctx context.Context, opts Options) (*Result, error) {
	start, err := ParseSiteURL(opts.URL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(opts.OutDir) == "" {
		return nil, errors.New("choose a folder to save the site in")
	}
	if opts.Workers <= 0 {
		opts.Workers = 6
	}
	if opts.UserAgent == "" {
		opts.UserAgent = DefaultUserAgent
	}
	if opts.Log == nil {
		opts.Log = func(string) {}
	}
	if opts.Progress == nil {
		opts.Progress = func(Stats) {}
	}
	c := &cloner{
		opts:  opts,
		site:  start,
		root:  filepath.Join(opts.OutDir, hostDir(start.Host)),
		seen:  map[string]bool{},
		saved: map[string]string{},
		used:  map[string]string{},
	}
	c.client = opts.Client
	if c.client == nil {
		c.client = &http.Client{Timeout: 10 * time.Minute}
	}
	if err := os.MkdirAll(c.root, 0o755); err != nil {
		return nil, fmt.Errorf("cannot create %s: %w", c.root, err)
	}
	if opts.RenderJS {
		path := opts.BrowserPath
		if path == "" {
			path = FindBrowser()
		}
		if path == "" {
			opts.Log("No Chrome, Edge, Brave or Chromium found, so pages are saved without running their scripts.")
		} else {
			c.render = newRenderer(path, opts.UserAgent, 2)
			defer c.render.close()
			opts.Log("Running pages in " + filepath.Base(path))
		}
	}
	if opts.FollowRobots {
		c.loadRobots(ctx)
	}

	c.setPhase("Downloading")
	c.q = newQueue()
	c.enqueue(start, 0, refPage)
	stopWatch := context.AfterFunc(ctx, c.q.close)
	var wg sync.WaitGroup
	for range opts.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				j, ok := c.q.pop()
				if !ok {
					return
				}
				c.process(ctx, j)
				c.q.done()
			}
		}()
	}
	wg.Wait()
	stopWatch()
	stopped := ctx.Err() != nil

	c.setPhase("Fixing links")
	c.rewriteAll()
	c.setPhase("Done")

	res := &Result{Root: c.root, Stats: c.snapshot(), Stopped: stopped}
	if c.index != "" {
		res.Index = filepath.Join(c.root, c.index)
	}
	return res, nil
}

func ParseSiteURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("enter the address of the website to copy")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("%q is not a website address", raw)
	}
	n, _ := normalize(u.String(), u)
	return n, nil
}

func key(u *url.URL) string {
	k := strings.TrimPrefix(strings.ToLower(u.Host), "www.") + u.EscapedPath()
	if u.RawQuery != "" {
		k += "?" + u.RawQuery
	}
	return k
}

func (c *cloner) loadRobots(ctx context.Context) {
	ru := *c.site
	ru.Path, ru.RawQuery = "/robots.txt", ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ru.String(), nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", c.opts.UserAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		c.robots = parseRobots(resp.Body, "WebCloner")
	}
}

func (c *cloner) enqueue(u *url.URL, depth int, kind refKind) {
	k := key(u)
	c.mu.Lock()
	if c.seen[k] {
		c.mu.Unlock()
		return
	}
	if kind == refPage {
		if !sameSite(u, c.site) {
			c.mu.Unlock()
			return
		}
		if c.opts.MaxDepth > 0 && depth > c.opts.MaxDepth {
			c.mu.Unlock()
			return
		}
		if c.opts.MaxPages > 0 && c.pageJobs >= c.opts.MaxPages {
			c.mu.Unlock()
			return
		}
		p := u.EscapedPath()
		if u.RawQuery != "" {
			p += "?" + u.RawQuery
		}
		if depth > 0 && !c.robots.allowed(p) {
			c.seen[k] = true
			c.stats.Skipped++
			c.mu.Unlock()
			c.opts.Log("Skipped (robots.txt): " + u.String())
			return
		}
		c.pageJobs++
	}
	c.seen[k] = true
	c.stats.Queued++
	c.mu.Unlock()
	c.q.push(job{u: u, depth: depth, kind: kind})
	c.report()
}

func (c *cloner) report() {
	c.opts.Progress(c.snapshot())
}

func (c *cloner) snapshot() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

func (c *cloner) setPhase(p string) {
	c.mu.Lock()
	c.stats.Phase = p
	c.mu.Unlock()
	c.report()
}

func (c *cloner) fail(u *url.URL, err error) {
	c.mu.Lock()
	c.stats.Failed++
	c.mu.Unlock()
	c.opts.Log(fmt.Sprintf("Failed: %s (%v)", u, err))
	c.report()
}

func (c *cloner) get(ctx context.Context, u *url.URL) (*http.Response, error) {
	var lastErr error
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.opts.UserAgent)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,text/css,image/avif,image/webp,*/*;q=0.8")
		resp, err := c.client.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			lastErr = fmt.Errorf("server said %s", resp.Status)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("server said %s", resp.Status)
		}
		return resp, nil
	}
	return nil, lastErr
}

func (c *cloner) process(ctx context.Context, j job) {
	if ctx.Err() != nil {
		return
	}
	resp, err := c.get(ctx, j.u)
	if err != nil {
		if ctx.Err() == nil {
			c.fail(j.u, err)
		}
		return
	}
	defer resp.Body.Close()
	final, ok := normalize(resp.Request.URL.String(), resp.Request.URL)
	if !ok {
		final = j.u
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(resp.Body, head)
	head = head[:n]
	ctype := resp.Header.Get("Content-Type")
	if ctype == "" {
		ctype = http.DetectContentType(head)
	}
	body := io.MultiReader(bytes.NewReader(head), resp.Body)

	if isHTMLType(ctype) {
		if j.kind != refPage || !sameSite(final, c.site) {
			c.skip()
			return
		}
		c.savePage(ctx, j, final, ctype, body)
		return
	}
	if isCSSType(ctype) {
		c.saveCSS(j, final, ctype, body)
		return
	}
	c.saveFile(j, final, ctype, body)
}

func (c *cloner) skip() {
	c.mu.Lock()
	c.stats.Skipped++
	c.mu.Unlock()
	c.report()
}

func (c *cloner) claimPath(u, final *url.URL, ctype string) string {
	rel := localPath(final, ctype, c.site)
	c.mu.Lock()
	defer c.mu.Unlock()
	fk := key(final)
	if p, ok := c.saved[fk]; ok {
		c.saved[key(u)] = p
		return ""
	}
	for {
		lk := strings.ToLower(rel)
		owner, taken := c.used[lk]
		if !taken || owner == fk {
			c.used[lk] = fk
			break
		}
		ext := filepath.Ext(rel)
		rel = strings.TrimSuffix(rel, ext) + "-" + shortHash(fk+rel) + ext
	}
	c.saved[key(u)] = rel
	c.saved[fk] = rel
	return rel
}

func (c *cloner) writeFile(rel string, r io.Reader, limit int64) (int64, error) {
	full := filepath.Join(c.root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".part-*")
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(tmp, io.LimitReader(r, limit+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = fmt.Errorf("larger than %d MB", limit>>20)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return 0, err
	}
	if err := os.Rename(tmp.Name(), full); err != nil {
		os.Remove(full)
		if err := os.Rename(tmp.Name(), full); err != nil {
			os.Remove(tmp.Name())
			return 0, err
		}
	}
	return n, nil
}

func (c *cloner) forget(u, final *url.URL, rel string) {
	c.mu.Lock()
	delete(c.saved, key(u))
	delete(c.saved, key(final))
	delete(c.used, strings.ToLower(rel))
	c.mu.Unlock()
}

func (c *cloner) saved1(rel string, n int64, rec *record, page bool) {
	c.mu.Lock()
	c.stats.Bytes += n
	if page {
		c.stats.Pages++
	} else {
		c.stats.Files++
	}
	if rec != nil {
		c.records = append(c.records, *rec)
	}
	c.mu.Unlock()
	c.report()
}

func (c *cloner) savePage(ctx context.Context, j job, final *url.URL, ctype string, body io.Reader) {
	utf8Body, err := charset.NewReader(io.LimitReader(body, maxHTMLBytes), ctype)
	if err != nil {
		utf8Body = io.LimitReader(body, maxHTMLBytes)
	}
	raw, err := io.ReadAll(utf8Body)
	if err != nil {
		c.fail(j.u, err)
		return
	}
	rendered := false
	if c.render != nil {
		doc, err := c.render.render(ctx, final.String())
		switch {
		case err == nil:
			raw, rendered = []byte(doc), true
		case ctx.Err() != nil:
			return
		default:
			c.opts.Log(fmt.Sprintf("Could not run scripts on %s, saving it as sent (%v)", final, err))
		}
	}
	doc, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		c.fail(j.u, err)
		return
	}
	base := final
	if href, node := findBase(doc); node != nil {
		if b, ok := normalize(href, final); ok {
			base = b
		}
		node.Parent.RemoveChild(node)
	}
	walkHTML(doc, func(r string, kind refKind) (string, bool) {
		if u, ok := normalize(r, base); ok {
			if kind == refPage {
				c.enqueue(u, j.depth+1, refPage)
			} else {
				c.enqueue(u, j.depth, refResource)
			}
		}
		return "", false
	})
	if rendered {
		stripScripts(doc)
	}
	setUTF8Charset(doc)
	var out bytes.Buffer
	if err := html.Render(&out, doc); err != nil {
		c.fail(j.u, err)
		return
	}
	rel := c.claimPath(j.u, final, "text/html")
	if rel == "" {
		return
	}
	n, err := c.writeFile(rel, &out, maxHTMLBytes*2)
	if err != nil {
		c.forget(j.u, final, rel)
		c.fail(j.u, err)
		return
	}
	if j.depth == 0 {
		c.mu.Lock()
		c.index = rel
		c.mu.Unlock()
	}
	c.opts.Log("Page: " + final.String())
	c.saved1(rel, n, &record{path: rel, base: base, html: true}, true)
}

func (c *cloner) saveCSS(j job, final *url.URL, ctype string, body io.Reader) {
	raw, err := io.ReadAll(io.LimitReader(body, maxHTMLBytes))
	if err != nil {
		c.fail(j.u, err)
		return
	}
	walkCSS(string(raw), func(r string, _ refKind) (string, bool) {
		if u, ok := normalize(r, final); ok {
			c.enqueue(u, j.depth, refResource)
		}
		return "", false
	})
	rel := c.claimPath(j.u, final, ctype)
	if rel == "" {
		return
	}
	n, err := c.writeFile(rel, bytes.NewReader(raw), maxHTMLBytes)
	if err != nil {
		c.forget(j.u, final, rel)
		c.fail(j.u, err)
		return
	}
	c.saved1(rel, n, &record{path: rel, base: final}, false)
}

func (c *cloner) saveFile(j job, final *url.URL, ctype string, body io.Reader) {
	rel := c.claimPath(j.u, final, ctype)
	if rel == "" {
		return
	}
	n, err := c.writeFile(rel, body, maxFileBytes)
	if err != nil {
		c.forget(j.u, final, rel)
		c.fail(j.u, err)
		return
	}
	c.saved1(rel, n, nil, false)
}

func (c *cloner) resolve(fromRel string, base *url.URL, raw string, kind refKind) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	u, ok := normalize(trimmed, base)
	if !ok {
		return "", false
	}
	frag := ""
	if i := strings.IndexByte(trimmed, '#'); i >= 0 {
		frag = trimmed[i:]
	}
	c.mu.Lock()
	target, found := c.saved[key(u)]
	c.mu.Unlock()
	if found {
		if kind == refPage && target == fromRel && frag != "" {
			return frag, true
		}
		return relativeRef(filepath.Join(c.root, fromRel), filepath.Join(c.root, target)) + frag, true
	}
	return u.String() + frag, true
}

func (c *cloner) rewriteAll() {
	c.mu.Lock()
	recs := append([]record(nil), c.records...)
	c.mu.Unlock()
	for _, rec := range recs {
		full := filepath.Join(c.root, rec.path)
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		fn := func(raw string, kind refKind) (string, bool) {
			return c.resolve(rec.path, rec.base, raw, kind)
		}
		var out []byte
		if rec.html {
			doc, err := html.Parse(bytes.NewReader(data))
			if err != nil {
				continue
			}
			walkHTML(doc, fn)
			dropIntegrity(doc)
			var buf bytes.Buffer
			if err := html.Render(&buf, doc); err != nil {
				continue
			}
			out = buf.Bytes()
		} else {
			out = []byte(walkCSS(string(data), fn))
		}
		if _, err := c.writeFile(rec.path, bytes.NewReader(out), int64(len(out))); err != nil {
			c.opts.Log(fmt.Sprintf("Could not fix links in %s (%v)", rec.path, err))
		}
	}
}
