package org

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func (f *apiFixture) teamJSON(user, id string) (map[string]any, int, string) {
	w := f.do(user, "GET", "/api/org/teams/"+id, "")
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return m, w.Code, w.Body.String()
}

func TestTeamReadCarriesVersionsAndNeverAWebhook(t *testing.T) {
	f := newAPI(t)
	const secret = "https://hooks.example.com/services/T000/B000/s3cr3t"
	if w := f.do("admin@x", "PUT", "/api/admin/teams/platform/webhooks/slack", `{"url":"`+secret+`"}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := f.do("admin@x", "PUT", "/api/org/teams/platform/budget", `{"version":0,"monthly_usd":50}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	for _, who := range []string{"lead@x", "senior@x", "new@x", "admin@x"} {
		m, code, body := f.teamJSON(who, "platform")
		if code != 200 || strings.Contains(body, "s3cr3t") || strings.Contains(strings.ToLower(body), "webhook") {
			t.Fatalf("%s GET = %d %s", who, code, body)
		}
		if m["version"].(float64) != float64(f.teamVersion("platform")) {
			t.Errorf("%s: team version = %v", who, m["version"])
		}
		if ps := m["programs"].([]any); len(ps) != 1 || ps[0].(map[string]any)["version"].(float64) < 1 {
			t.Errorf("%s: programs = %v, want forge-101 with its version", who, m["programs"])
		}
	}
	m, _, _ := f.teamJSON("lead@x", "platform")
	if b, _ := m["budget"].(map[string]any); b == nil || b["version"].(float64) != 1 || b["monthly_usd"].(float64) != 50 {
		t.Errorf("leader budget = %v, want version 1 and $50", m["budget"])
	}
	if m, _, _ := f.teamJSON("senior@x", "platform"); m["budget"] != nil {
		t.Errorf("a senior saw the budget: %v", m["budget"])
	}
	for _, who := range []string{"nobody@x", "boss@x"} {
		if _, code, _ := f.teamJSON(who, "platform"); code != 404 {
			t.Errorf("%s GET = %d, want 404", who, code)
		}
	}
	if w := f.do("", "GET", "/api/org/teams/platform", ""); w.Code != 401 {
		t.Errorf("signed out = %d", w.Code)
	}
}

func TestProgramAndBudgetRoutesByRole(t *testing.T) {
	f := newAPI(t)
	pv := f.snap.Teams["platform"].Programs["forge-101"].Version
	prog := fmt.Sprintf(`{"version":%d,"roles":{"participants":["new@x"]},"budget_usd_month":0}`, pv)
	path := "/api/org/teams/platform/programs/forge-101"
	before := count(t, f.s, `SELECT count(*) FROM audit_log`)
	for _, who := range []string{"new@x", "boss@x", "nobody@x"} {
		if w := f.do(who, "PUT", path, prog); w.Code != 403 {
			t.Errorf("%s PUT = %d, want 403", who, w.Code)
		}
	}
	for _, who := range []string{"senior@x", "lead@x", "boss@x"} {
		if w := f.do(who, "PUT", "/api/org/teams/platform/budget", `{"version":0,"monthly_usd":5}`); w.Code != 403 {
			t.Errorf("%s budget = %d, want 403", who, w.Code)
		}
	}
	if count(t, f.s, `SELECT count(*) FROM audit_log`) != before || f.refreshes != 0 {
		t.Fatal("refused requests changed something")
	}
	if w := f.do("senior@x", "PUT", path, prog); w.Code != 200 || !strings.Contains(w.Body.String(), fmt.Sprintf(`"version":%d`, pv+1)) {
		t.Fatalf("a senior of the owning team PUT = %d %s", w.Code, w.Body)
	}
	if w := f.do("admin@x", "PUT", "/api/org/teams/other/programs/forge-101", prog); w.Code != 404 {
		t.Errorf("a team that does not own it = %d, want 404", w.Code)
	}
	if w := f.do("admin@x", "PUT", "/api/org/teams/platform/budget", `{"version":0,"monthly_usd":5,"hard_cap_usd":9}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"version":1`) {
		t.Errorf("admin budget = %d %s", w.Code, w.Body)
	}
	if w := f.do("admin@x", "PUT", "/api/org/teams/ghost/budget", `{"version":0,"monthly_usd":5}`); w.Code != 404 {
		t.Errorf("budget for a missing team = %d, want 404", w.Code)
	}
}

func TestStaleVersionIs409AndWritesNothing(t *testing.T) {
	f := newAPI(t)
	pv := f.snap.Teams["platform"].Programs["forge-101"].Version
	if w := f.do("admin@x", "PUT", "/api/org/teams/platform/budget", `{"version":0,"monthly_usd":5}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	before := count(t, f.s, `SELECT count(*) FROM audit_log`)
	for _, c := range []struct{ path, body string }{
		{"/api/org/teams/platform/programs/forge-101", fmt.Sprintf(`{"version":%d,"roles":{"participants":["x@x"]},"budget_usd_month":0}`, pv+7)},
		{"/api/org/teams/platform/budget", `{"version":0,"monthly_usd":99}`},
		{"/api/org/teams/platform/budget", `{"version":9,"monthly_usd":99}`},
	} {
		if w := f.do("admin@x", "PUT", c.path, c.body); w.Code != 409 || !strings.Contains(w.Body.String(), "reload") {
			t.Errorf("PUT %s = %d %s, want 409", c.path, w.Code, w.Body)
		}
	}
	var monthly float64
	if err := f.s.DB.QueryRow(t.Context(), `SELECT monthly_usd FROM team_budgets WHERE team = 'platform'`).Scan(&monthly); err != nil || monthly != 5 {
		t.Errorf("budget = %v (%v), want it untouched at 5", monthly, err)
	}
	if n := count(t, f.s, `SELECT count(*) FROM training_roles`); n != 0 {
		t.Errorf("a stale program save enrolled %d people", n)
	}
	if count(t, f.s, `SELECT count(*) FROM audit_log`) != before {
		t.Error("a refused save left an audit row")
	}
}

func TestNewRoutesRefuseUnknownFields(t *testing.T) {
	f := newAPI(t)
	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/api/org/teams/platform/programs/forge-101", `{"version":1,"enrolled":[]}`},
		{"PUT", "/api/org/teams/platform/budget", `{"version":0,"monthly_usd":1,"hard_cap":2}`},
	} {
		if w := f.do("admin@x", c.method, c.path, c.body); w.Code != 400 || !strings.Contains(w.Body.String(), "unknown field") {
			t.Errorf("%s %s = %d %s, want 400 naming the unknown field", c.method, c.path, w.Code, w.Body)
		}
	}
}

// What the page reads is what it saves back: roles as given (groups stay groups), with who they are now beside them.
func TestGetThenPutBackKeepsGroups(t *testing.T) {
	f := newAPI(t)
	pv := f.snap.Teams["platform"].Programs["forge-101"].Version
	if w := f.do("lead@x", "PUT", "/api/org/teams/platform/programs/forge-101", fmt.Sprintf(`{"version":%d,"budget_usd_month":0,"roles":{"participants":["platform_trainees"],"editors":["senior@x"]}}`, pv)); w.Code != 200 {
		t.Fatal(w.Body)
	}
	m, _, body := f.teamJSON("lead@x", "platform")
	p := m["programs"].([]any)[0].(map[string]any)
	if got := fmt.Sprint(p["roles"].(map[string]any)["participants"]); got != "[platform_trainees]" {
		t.Fatalf("roles as given = %s: %s", got, body)
	}
	if got := fmt.Sprint(p["resolved"].(map[string]any)["participants"]); got != "[new@x]" {
		t.Errorf("resolved = %s", got)
	}
	put := map[string]any{"version": p["version"], "roles": p["roles"], "schedule": p["schedule"],
		"ttl": p["lab_defaults"].(map[string]any)["ttl"], "budget_usd_month": p["budget_usd_month"], "review_self_reported": p["review_self_reported"]}
	b, _ := json.Marshal(put)
	if w := f.do("lead@x", "PUT", "/api/org/teams/platform/programs/forge-101", string(b)); w.Code != 200 {
		t.Fatalf("PUT = %d %s", w.Code, w.Body)
	}
	if n := count(t, f.s, `SELECT count(*) FROM training_roles WHERE ref = 'platform_trainees'`); n != 1 {
		t.Errorf("the group must survive a round trip: %d rows", n)
	}
}

// The team page lists members with their tags, strongest first.
func TestTeamPageListsMembersByTag(t *testing.T) {
	f := newAPI(t)
	m, _, _ := f.teamJSON("new@x", "platform")
	if got := fmt.Sprint(m["members"]); got != "[map[email:lead@x tag:leader] map[email:senior@x tag:senior] map[email:new@x tag:trainee]]" {
		t.Errorf("members = %s", got)
	}
	if m["can_edit_team"] != false || m["is_admin"] != false {
		t.Errorf("a trainee edits nothing: %v %v", m["can_edit_team"], m["is_admin"])
	}
	if m, _, _ := f.teamJSON("lead@x", "platform"); m["can_edit_team"] != true {
		t.Error("the leader retags")
	}
}
