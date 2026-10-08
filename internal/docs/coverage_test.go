package docs

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"crucible/internal/content/blocks"
)

// TestDocsCoverEveryRouteAndRole keeps the Docs tab current (CLAUDE.md: a capability change updates docs/user in the
// same commit). It fails when an SPA route or a role has no page, or a page covers a route that no longer exists.
func TestDocsCoverEveryRouteAndRole(t *testing.T) {
	pages, err := All()
	if err != nil {
		t.Fatal(err)
	}
	app, err := os.ReadFile("../../web/src/App.tsx")
	if err != nil {
		t.Fatal(err)
	}
	routes := map[string]bool{}
	for _, m := range regexp.MustCompile(`<Route path="([^"]+)"`).FindAllStringSubmatch(string(app), -1) {
		if m[1] != "*" {
			routes[m[1]] = false
		}
	}
	if len(routes) < 20 {
		t.Fatalf("found only %d routes in App.tsx: did the route syntax change?", len(routes))
	}
	roles, blocksCovered := map[string]bool{}, map[string]bool{}
	for _, p := range pages {
		if strings.TrimSpace(p.Body) == "" {
			t.Errorf("%s is empty", p.Slug)
		}
		for _, r := range p.Roles {
			roles[r] = true
		}
		for _, c := range p.Covers {
			kind, v, _ := strings.Cut(c, ":")
			switch kind {
			case "route":
				if _, ok := routes[v]; !ok {
					t.Errorf("%s covers route %s, which web/src/App.tsx no longer has", p.Slug, v)
				}
				routes[v] = true
			case "block":
				if _, ok := blocks.Find(v); !ok {
					t.Errorf("%s covers block %s, which the catalog doesn't have", p.Slug, v)
				}
				blocksCovered[v] = true
			case "feature":
			default:
				t.Errorf("%s: unknown cover %q (use route:, block: or feature:)", p.Slug, c)
			}
		}
	}
	for r, covered := range routes {
		if !covered {
			t.Errorf("no page in docs/user covers route %s: add `route:%s` to the covers of the page that explains it", r, r)
		}
	}
	for _, b := range blocks.Catalog {
		if !blocksCovered[b.ID] {
			t.Errorf("no page covers block %s: add `block:%s` to the covers of the page that explains it", b.ID, b.ID)
		}
	}
	for _, r := range Roles {
		if !roles[r] {
			t.Errorf("no page for role %s", r)
		}
	}
	if !slices.ContainsFunc(pages, func(p Page) bool { return p.Slug == "authors/building-blocks" }) {
		t.Error("the generated building-blocks page is missing")
	}
}
