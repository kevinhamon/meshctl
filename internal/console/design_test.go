package console

import (
	"regexp"
	"strings"
	"testing"
)

// design_test.go — the stylesheet keeps to the design language (ui/DESIGN.md):
// type sizes, spacing and radii come from the tokens, never raw values.

func TestStylesheet_UsesTheDesignTokens(t *testing.T) {
	b, err := uiFS.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	css := page[strings.Index(page, "<style>"):strings.Index(page, "</style>")]
	// The token blocks are the only place raw values belong.
	css = regexp.MustCompile(`(?s):root \{.*?\n  \}`).ReplaceAllString(css, "")
	decl := regexp.MustCompile(`(font-size|border-radius|padding|margin|gap|row-gap|column-gap)(-[a-z]+)?:\s*([^;}]+)`)
	allowed := regexp.MustCompile(`^(0|auto|1px|2px|50%|inherit|var\(--[a-z0-9-]+\))$`)
	for _, m := range decl.FindAllStringSubmatch(css, -1) {
		for _, v := range strings.Fields(m[3]) {
			if !allowed.MatchString(v) {
				t.Errorf("%s: %q is not a design token (in %q)", m[1]+m[2], v, strings.TrimSpace(m[0]))
			}
		}
	}
}
