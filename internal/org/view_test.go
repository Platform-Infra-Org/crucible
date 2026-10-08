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
	mustTraining(t, f.s, "net-101")
	f.reload()
	pv := f.snap.Teams["platform"].Programs["forge-101"].Version
	prog := fmt.Sprintf(`{"version":%d,"enrolled":["new@x"],"budget_usd_month":0}`, pv)
	routes := []struct{ method, path, body string }{
		{"PUT", "/api/org/teams/platform/programs/forge-101", prog},
		{"POST", "/api/org/teams/platform/programs/net-101", `{"enrolled":["new@x"],"budget_usd_month":0}`},
		{"DELETE", "/api/org/teams/platform/programs/forge-101", ""},
	}
	before := count(t, f.s, `SELECT count(*) FROM audit_log`)
	for _, who := range []string{"senior@x", "new@x", "boss@x", "nobody@x"} {
		for _, rt := range routes {
			if w := f.do(who, rt.method, rt.path, rt.body); w.Code != 403 {
				t.Errorf("%s %s %s = %d, want 403", who, rt.method, rt.path, w.Code)
			}
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
	if w := f.do("lead@x", "PUT", routes[0].path, prog); w.Code != 200 || !strings.Contains(w.Body.String(), fmt.Sprintf(`"version":%d`, pv+1)) {
		t.Fatalf("leader PUT = %d %s", w.Code, w.Body)
	}
	if w := f.do("lead@x", "POST", routes[1].path, routes[1].body); w.Code != 200 || !strings.Contains(w.Body.String(), `"version":1`) {
		t.Fatalf("leader POST = %d %s", w.Code, w.Body)
	}
	if w := f.do("lead@x", "DELETE", routes[1].path, ""); w.Code != 200 {
		t.Fatalf("leader DELETE = %d %s", w.Code, w.Body)
	}
	if w := f.do("admin@x", "DELETE", routes[1].path, ""); w.Code != 404 {
		t.Errorf("deleting a missing program = %d, want 404", w.Code)
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
		{"/api/org/teams/platform/programs/forge-101", fmt.Sprintf(`{"version":%d,"enrolled":["new@x"],"budget_usd_month":0}`, pv+7)},
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
	if n := count(t, f.s, `SELECT count(*) FROM enrollments WHERE team = 'platform'`); n != 0 {
		t.Errorf("a stale program save enrolled %d people", n)
	}
	if count(t, f.s, `SELECT count(*) FROM audit_log`) != before {
		t.Error("a refused save left an audit row")
	}
}

func TestEnrollingSomeoneOutsideTheTeamIsRefused(t *testing.T) {
	f := newAPI(t)
	mustTraining(t, f.s, "net-101")
	f.reload()
	if w := f.do("lead@x", "POST", "/api/org/teams/platform/programs/net-101", `{"enrolled":["stranger@x"]}`); w.Code != 400 {
		t.Fatalf("enroll outsider = %d %s, want 400", w.Code, w.Body)
	}
	pv := f.snap.Teams["platform"].Programs["forge-101"].Version
	if w := f.do("lead@x", "PUT", "/api/org/teams/platform/programs/forge-101", fmt.Sprintf(`{"version":%d,"enrolled":["stranger@x"]}`, pv)); w.Code != 400 {
		t.Fatalf("save outsider = %d %s, want 400", w.Code, w.Body)
	}
	if n := count(t, f.s, `SELECT count(*) FROM enrollments WHERE email = 'stranger@x'`) + count(t, f.s, `SELECT count(*) FROM programs WHERE training = 'net-101'`); n != 0 {
		t.Errorf("%d rows were written", n)
	}
}

func TestNewRoutesRefuseUnknownFields(t *testing.T) {
	f := newAPI(t)
	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/api/org/teams/platform/programs/forge-101", `{"version":1,"enrolledd":[]}`},
		{"POST", "/api/org/teams/platform/programs/forge-101", `{"enrolledd":[]}`},
		{"PUT", "/api/org/teams/platform/budget", `{"version":0,"monthly_usd":1,"hard_cap":2}`},
	} {
		if w := f.do("admin@x", c.method, c.path, c.body); w.Code != 400 || !strings.Contains(w.Body.String(), "unknown field") {
			t.Errorf("%s %s = %d %s, want 400 naming the unknown field", c.method, c.path, w.Code, w.Body)
		}
	}
}

func TestGetThenPutBackStoresNoRoles(t *testing.T) {
	f := newAPI(t)
	m, _, body := f.teamJSON("lead@x", "platform")
	p := m["programs"].([]any)[0].(map[string]any)
	roles := p["roles"].(map[string]any)
	for k, v := range roles {
		if len(v.([]any)) != 0 {
			t.Fatalf("roles.%s = %v, want empty (defaulted) as stored: %s", k, v, body)
		}
	}
	if eff := p["effective_roles"].(map[string]any)["manager"].([]any); len(eff) != 1 || eff[0] != "lead@x" {
		t.Errorf("effective manager = %v, want the leader", eff)
	}
	put := map[string]any{"version": p["version"], "enrolled": p["enrolled"], "roles": p["roles"], "schedule": p["schedule"],
		"ttl": p["lab_defaults"].(map[string]any)["ttl"], "budget_usd_month": p["budget_usd_month"], "review_self_reported": p["review_self_reported"]}
	b, _ := json.Marshal(put)
	if w := f.do("lead@x", "PUT", "/api/org/teams/platform/programs/forge-101", string(b)); w.Code != 200 {
		t.Fatalf("PUT = %d %s", w.Code, w.Body)
	}
	if n := count(t, f.s, `SELECT count(*) FROM program_roles`); n != 0 {
		t.Errorf("%d program_roles rows after a round trip; defaults must stay dynamic", n)
	}
}
