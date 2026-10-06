package learn

import (
	"maps"
	"slices"
	"strings"

	"crucible/internal/auth"
	"crucible/internal/rbac"
)

type TeamRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type CatalogEntry struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	Description    string    `json:"description"`
	EstimatedHours float64   `json:"estimated_hours"`
	Modules        int       `json:"modules"`
	Enrolled       []TeamRef `json:"enrolled"` // the teams you take it through
	Available      bool      `json:"available"`
}

// Catalog is the global training catalog: titles and descriptions from each training's branch head, plus the teams
// the user is enrolled through. No module bodies, quizzes, labs, rubrics or progress.
func (s *Service) Catalog(u *auth.User) ([]CatalogEntry, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	mine := map[string][]TeamRef{}
	for _, e := range (rbac.Checker{P: st.Platform}).Enrollments(u.Email) {
		mine[e.Program.Training] = append(mine[e.Program.Training], TeamRef{ID: e.Team.ID, Name: e.Team.Name})
	}
	out := []CatalogEntry{}
	for _, id := range slices.Sorted(maps.Keys(st.Platform.Trainings)) {
		c := CatalogEntry{ID: id, Title: id, Enrolled: mine[id]}
		if c.Enrolled == nil {
			c.Enrolled = []TeamRef{}
		}
		if t := st.Training(id, st.Heads[id]); t != nil {
			c.Title, c.Description, c.EstimatedHours, c.Modules, c.Available = t.Title, t.Description, t.EstimatedHours, len(t.Modules), true
		}
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b CatalogEntry) int { return strings.Compare(a.Title, b.Title) })
	return out, nil
}
