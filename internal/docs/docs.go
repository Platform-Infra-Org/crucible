// Package docs serves the in-app guide (the Docs tab): Markdown pages from docs/user with front matter, plus pages
// generated from the block registry. Pages explain Crucible itself and never contain training content, so any
// signed-in user may read all of them.
package docs

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"crucible/internal/apperr"
	"crucible/internal/httpx"
)

// Version is the Crucible build, set with -ldflags "-X crucible/internal/docs.Version=<git describe>".
var Version = "dev"

var (
	Roles    = []string{"everyone", "trainee", "author", "scorer", "approver", "leader", "admin"}
	Sections = []string{"getting-started", "trainees", "authors", "scorers", "leaders", "admins"}
)

type Page struct {
	Slug     string   `json:"slug"`
	Section  string   `json:"section"`
	Title    string   `json:"title"`
	Roles    []string `json:"roles"`
	Covers   []string `json:"covers"`
	Order    int      `json:"order"`
	Headings []string `json:"headings"`
	Body     string   `json:"-"`
}

type front struct {
	Title  string   `yaml:"title"`
	Roles  []string `yaml:"roles"`
	Covers []string `yaml:"covers"`
	Order  int      `yaml:"order"`
}

// Load reads every *.md under fsys (one folder per section) and sorts them by section, order and slug.
func Load(fsys fs.FS) ([]Page, error) {
	pages := []Page{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".md" {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		pg, err := parse(strings.TrimSuffix(p, ".md"), string(b))
		if err != nil {
			return fmt.Errorf("docs/user/%s: %w", p, err)
		}
		pages = append(pages, pg)
		return nil
	})
	Sort(pages)
	return pages, err
}

// Sort orders pages by section, then order, then slug.
func Sort(pages []Page) {
	slices.SortFunc(pages, func(a, b Page) int {
		return cmp.Or(cmp.Compare(slices.Index(Sections, a.Section), slices.Index(Sections, b.Section)), cmp.Compare(a.Order, b.Order), cmp.Compare(a.Slug, b.Slug))
	})
}

func parse(slug, src string) (Page, error) {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	rest, ok := strings.CutPrefix(src, "---\n")
	fm, body, ok2 := strings.Cut(rest, "\n---\n")
	if !ok || !ok2 {
		return Page{}, errors.New("a page starts with a --- front matter block")
	}
	var f front
	dec := yaml.NewDecoder(strings.NewReader(fm))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return Page{}, fmt.Errorf("front matter: %w", err)
	}
	section, _, _ := strings.Cut(slug, "/")
	switch {
	case f.Title == "":
		return Page{}, errors.New("title is required")
	case len(f.Roles) == 0:
		return Page{}, errors.New("roles is required")
	case !slices.Contains(Sections, section) || !strings.Contains(slug, "/"):
		return Page{}, fmt.Errorf("pages live in one of %v", Sections)
	}
	for _, r := range f.Roles {
		if !slices.Contains(Roles, r) {
			return Page{}, fmt.Errorf("unknown role %q (one of %v)", r, Roles)
		}
	}
	body = strings.TrimLeft(body, "\n")
	return Page{Slug: slug, Section: section, Title: f.Title, Roles: f.Roles, Covers: nonNil(f.Covers), Order: f.Order, Headings: headings(body), Body: body}, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// headings are the ## and ### titles outside code fences (client-side search matches them).
func headings(body string) []string {
	out, fence := []string{}, false
	for _, l := range strings.Split(body, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = !fence
			continue
		}
		if fence {
			continue
		}
		if h, ok := strings.CutPrefix(t, "## "); ok {
			out = append(out, strings.TrimSpace(h))
		} else if h, ok := strings.CutPrefix(t, "### "); ok {
			out = append(out, strings.TrimSpace(h))
		}
	}
	return out
}

type Service struct {
	pages  []Page
	bySlug map[string]Page
}

func New(pages []Page) *Service {
	s := &Service{pages: pages, bySlug: map[string]Page{}}
	for _, p := range pages {
		s.bySlug[p.Slug] = p
	}
	return s
}

// Routes: the slug is only ever a map key, never a file path.
func (s *Service) Routes(r chi.Router) {
	r.Get("/api/docs", func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]any{"version": Version, "pages": s.pages})
	})
	r.Get("/api/docs/*", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.bySlug[chi.URLParam(r, "*")]
		if !ok {
			httpx.Error(w, apperr.Wrap(apperr.NotFound, "no such page"))
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"slug": p.Slug, "title": p.Title, "markdown": p.Body, "version": Version})
	})
}
