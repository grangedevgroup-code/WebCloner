package main

import (
	_ "embed"
	"regexp"
)

//go:embed FyneApp.toml
var fyneApp string

var version = func() string {
	if m := regexp.MustCompile(`(?m)^\s*Version\s*=\s*"([^"]+)"`).FindStringSubmatch(fyneApp); m != nil {
		return m[1]
	}
	return "dev"
}()
