package learn

import "testing"

func TestCatalogListsEveryTrainingAndYourTeams(t *testing.T) {
	s, u, leader := fixture(t)
	s.State().Heads = map[string]string{"forge-101": "abc"}
	cat, err := s.Catalog(u)
	if err != nil || len(cat) == 0 {
		t.Fatalf("catalog %v %v", cat, err)
	}
	var f101 *CatalogEntry
	for i := range cat {
		if cat[i].ID == "forge-101" {
			f101 = &cat[i]
		}
	}
	if f101 == nil || !f101.Available || f101.Modules != 3 || len(f101.Enrolled) != 1 || f101.Enrolled[0].ID != "forge" {
		t.Fatalf("forge-101 entry %+v", f101)
	}
	cat, _ = s.Catalog(leader)
	for _, c := range cat {
		if len(c.Enrolled) != 0 {
			t.Fatalf("the leader is enrolled in nothing: %+v", c)
		}
	}
}
