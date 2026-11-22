package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/grangedevgroup-code/WebCloner/cloner"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		runGUI()
		return
	}
	if os.Args[1] == "version" || os.Args[1] == "--version" {
		fmt.Println("WebCloner " + version)
		return
	}
	fs := flag.NewFlagSet("webcloner", flag.ExitOnError)
	site := fs.String("url", "", "website to copy")
	out := fs.String("out", defaultFolder(), "folder to save the copy in")
	pages := fs.Int("pages", 0, "stop after this many pages (0 means no limit)")
	js := fs.Bool("js", true, "run each page's scripts in Chrome, Edge, Brave or Chromium first")
	robots := fs.Bool("robots", true, "skip pages that robots.txt disallows")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Run WebCloner with no arguments to open the window, or copy a site from the command line:")
		fmt.Fprintln(fs.Output(), "  webcloner -url https://example.com -out ~/Sites")
		fs.PrintDefaults()
	}
	fs.Parse(os.Args[1:])
	if *site == "" {
		fs.Usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res, err := cloner.Clone(ctx, cloner.Options{
		URL:          *site,
		OutDir:       *out,
		MaxPages:     *pages,
		RenderJS:     *js,
		FollowRobots: *robots,
		Log:          func(s string) { fmt.Println(s) },
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("%s\nSaved in %s\n", describe(res.Stats), res.Root)
	if res.Stopped {
		os.Exit(130)
	}
}
