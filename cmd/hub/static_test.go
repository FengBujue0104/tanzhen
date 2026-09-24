package main

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// The hub serves its SPA under `Content-Security-Policy: style-src 'self'`
// with no 'unsafe-inline'. That forbids two things that are easy to reintroduce
// by accident while editing the HTML: a `style="..."` attribute, and a
// `<style>` block (including one spliced in through innerHTML). Either one
// silently breaks the page's styling in production while the file looks fine on
// disk, so the shipped markup is asserted here rather than eyeballed.

var (
	// Matches style="..." and style='...' as an HTML attribute.
	reStyleAttr = regexp.MustCompile(`(?i)\sstyle\s*=\s*["']`)
	reStyleOpen = regexp.MustCompile(`(?i)<\s*style[\s>]`)
	reStyleEnd  = regexp.MustCompile(`(?i)</\s*style\s*>`)
	reInnerHTML = regexp.MustCompile(`\.innerHTML\s*=`)
)

func TestStaticHTMLHasNoInlineStyles(t *testing.T) {
	for _, name := range []string{"index.html", "admin.html"} {
		b, err := fs.ReadFile(staticRoot, "static/"+name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(b)

		if loc := reStyleAttr.FindStringIndex(src); loc != nil {
			line := 1 + strings.Count(src[:loc[0]], "\n")
			t.Errorf("%s:%d has a style=\"...\" attribute, which style-src 'self' blocks", name, line)
		}
		if reStyleOpen.MatchString(src) || reStyleEnd.MatchString(src) {
			t.Errorf("%s has a <style> block, which style-src 'self' blocks", name)
		}
		// A favicon data URI is the one inline image the CSP allows (img-src
		// data:); everything else must come from /assets/.
		if !strings.Contains(src, `rel="icon"`) {
			t.Errorf("%s has no favicon", name)
		}
	}
}

func TestStaticJSBuildsDOMNotHTML(t *testing.T) {
	for _, name := range []string{"assets/app.js", "assets/admin.js"} {
		b, err := fs.ReadFile(staticRoot, "static/"+name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if loc := reInnerHTML.FindIndex(b); loc != nil {
			line := 1 + strings.Count(string(b[:loc[0]]), "\n")
			t.Errorf("%s:%d assigns innerHTML, which bypasses text escaping", name, line)
		}
	}
}

// The public SPA must not carry the admin UI's markup: an operator who mounts
// only the public listener should not be able to pull the admin form from it.
func TestStaticFilesAreTheOnlyOnes(t *testing.T) {
	want := map[string]bool{
		"index.html":       false,
		"admin.html":       false,
		"install.sh":       false,
		"install.ps1":      false,
		"install-hub.sh":   false,
		"assets/app.js":    false,
		"assets/admin.js":  false,
		"assets/style.css": false,
	}
	sub, err := fs.Sub(staticRoot, "static")
	if err != nil {
		t.Fatal(err)
	}
	var extra []string
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if _, ok := want[p]; !ok {
			extra = append(extra, p)
			return nil
		}
		want[p] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("static/%s is missing from the embed", name)
		}
	}
	if len(extra) > 0 {
		t.Errorf("unexpected files in static/: %v", extra)
	}
}
