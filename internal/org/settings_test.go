package org

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"crucible/internal/apperr"
	"crucible/internal/config"
	"crucible/internal/db/dbtest"
)

func ptr[T any](v T) *T { return &v }

func okTiers() *config.CostTiers { return &config.CostTiers{Tier1USD: 5, Tier2USD: 25} }

func TestSetSettingsRefusesBadValues(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	for name, b := range map[string]SettingsBody{
		"tiers out of order":  {Version: 1, DefaultTheme: "forge", CostTiers: &config.CostTiers{AutoApproveUSD: 10, Tier1USD: 5, Tier2USD: 25}},
		"tier1 zero":          {Version: 1, DefaultTheme: "forge", CostTiers: &config.CostTiers{Tier1USD: 0, Tier2USD: 1}},
		"unknown theme":       {Version: 1, DefaultTheme: "chrome", CostTiers: okTiers()},
		"negative escalation": {Version: 1, DefaultTheme: "forge", EscalationHours: -1, CostTiers: okTiers()},
		"masterwork not 100":  {Version: 1, DefaultTheme: "forge", CostTiers: okTiers(), Ranks: config.RankThresholds{Ingot: 20, Tempered: 45, Blade: 75, Sword: 90, Masterwork: 99}},
		"negative rate":       {Version: 1, DefaultTheme: "forge", CostTiers: okTiers(), ClusterUSDPerHour: ptr(-1.0)},
	} {
		if err := s.SetSettings(ctx, "admin@x", b); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%s: want an Invalid refusal, got %v", name, err)
		}
	}
	p, _ := s.Platform(ctx)
	if p.Settings.CostTiers != nil || p.Settings.DefaultTheme != "forge" {
		t.Error("a refused save must not have written anything")
	}
	var n int
	s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&n)
	if n != 0 {
		t.Errorf("refused saves left %d audit rows", n)
	}
}

func TestSetSettingsStoresAndAudits(t *testing.T) {
	pool := dbtest.New(t)
	s := &Store{DB: pool}
	ctx := context.Background()
	b := SettingsBody{Version: 1, DefaultTheme: "anvil", EscalationHours: 6,
		CostTiers: okTiers(), ClusterUSDPerHour: ptr(0.0)}
	if err := s.SetSettings(ctx, "Admin@x", b); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	p, _ := s.Platform(ctx)
	if p.Settings.DefaultTheme != "anvil" || p.Settings.CostTiers.Tier2USD != 25 || p.Settings.EscalationHours != 6 {
		t.Fatalf("settings = %+v", p.Settings)
	}
	if p.Settings.Ranks != config.DefaultRanks {
		t.Errorf("unset ranks must take the defaults, got %+v", p.Settings.Ranks)
	}
	if p.Settings.ClusterUSDPerHour == nil || *p.Settings.ClusterUSDPerHour != 0 {
		t.Error("an explicit 0 cluster rate means free on the node, and must survive as 0, not NULL")
	}
	var n, v int
	pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'settings.update' AND actor = 'admin@x'`).Scan(&n)
	if n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}
	pool.QueryRow(ctx, `SELECT version FROM settings`).Scan(&v)
	if v != 2 {
		t.Errorf("version = %d, want 2 after one save", v)
	}
}

func TestSetSettingsRefusesAStaleVersion(t *testing.T) {
	pool := dbtest.New(t)
	s := &Store{DB: pool}
	ctx := context.Background()
	ok := SettingsBody{Version: 1, DefaultTheme: "forge", CostTiers: okTiers()}
	if err := s.SetSettings(ctx, "admin@x", ok); err != nil {
		t.Fatal(err)
	}
	err := s.SetSettings(ctx, "other@x", ok)
	if !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "reload") {
		t.Errorf("a second save at version 1 must be a Conflict telling the person to reload, got %v", err)
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE actor = 'other@x'`).Scan(&n)
	if n != 0 {
		t.Error("a refused stale save must not be audited")
	}
}

func TestFreshInstanceHasNoCostTiers(t *testing.T) {
	p, err := (&Store{DB: dbtest.New(t)}).Platform(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.Settings.CostTiers != nil {
		t.Fatal("a fresh instance must have no tiers, so labs.quote refuses paid labs instead of pricing them free")
	}
}

func TestSetScheduleValidatesWindows(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	bad := config.Schedule{Timezone: "Europe/Bucharest", Windows: []config.Window{{Days: []string{"funday"}, Start: "08:00", End: "19:00"}}}
	if err := s.SetSchedule(ctx, "admin@x", "business-hours", bad); err == nil {
		t.Error("an unknown day must be refused")
	}
	night := config.Schedule{Timezone: "Europe/Bucharest", Windows: []config.Window{{Days: []string{"mon"}, Start: "20:00", End: "04:00"}}}
	if err := s.SetSchedule(ctx, "admin@x", "nights", night); err == nil {
		t.Error("a window crossing midnight must be refused")
	}
	good := config.Schedule{Timezone: "Europe/Bucharest", Windows: []config.Window{{Days: []string{"mon", "fri"}, Start: "08:00", End: "19:00"}}}
	if err := s.SetSchedule(ctx, "admin@x", "business-hours", good); err != nil {
		t.Fatal(err)
	}
	good.Windows[0].End = "18:00"
	if err := s.SetSchedule(ctx, "admin@x", "business-hours", good); err != nil {
		t.Fatalf("saving an existing name must replace it: %v", err)
	}
	p, err := s.Platform(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sc := p.Settings.Schedules["business-hours"]; sc == nil || sc.Windows[0].End != "18:00" {
		t.Errorf("the schedule must read back updated, got %+v", sc)
	}
	if p.Settings.Schedules["nights"] != nil {
		t.Error("a refused schedule must not be stored")
	}
}

func TestDeleteScheduleRefusedWhileAProgramUsesIt(t *testing.T) {
	pool := dbtest.New(t)
	s := &Store{DB: pool}
	ctx := context.Background()
	sc := config.Schedule{Timezone: "UTC", Windows: []config.Window{{Days: []string{"mon"}, Start: "08:00", End: "19:00"}}}
	for _, n := range []string{"days", "spare"} {
		if err := s.SetSchedule(ctx, "admin@x", n, sc); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(t, pool, `INSERT INTO teams (id, name) VALUES ('forge', 'The Forge')`)
	mustExec(t, pool, `INSERT INTO team_members (team, email, role) VALUES ('forge','leader@x','leader')`)
	mustExec(t, pool, `INSERT INTO trainings (id, repo) VALUES ('forge-101','https://git/x.git')`)
	mustExec(t, pool, `INSERT INTO programs (team, training, schedule_name) VALUES ('forge','forge-101','days')`)
	err := s.DeleteSchedule(ctx, "admin@x", "days")
	if !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "forge") || !strings.Contains(err.Error(), "forge-101") {
		t.Fatalf("want a Conflict naming the team and training, got %v", err)
	}
	if err := s.DeleteSchedule(ctx, "admin@x", "spare"); err != nil {
		t.Fatalf("an unused schedule deletes: %v", err)
	}
	if err := s.DeleteSchedule(ctx, "admin@x", "spare"); !errors.Is(err, apperr.NotFound) {
		t.Errorf("deleting twice = %v, want NotFound", err)
	}
	if _, err := s.Platform(ctx); err != nil {
		t.Fatalf("Platform must still read: %v", err)
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'schedule.delete'`).Scan(&n)
	if n != 1 {
		t.Errorf("schedule.delete audit rows = %d, want 1 (refusals roll back)", n)
	}
}

func TestSetQuotesReplacesTheList(t *testing.T) {
	pool := dbtest.New(t)
	s := &Store{DB: pool}
	ctx := context.Background()
	if err := s.SetQuotes(ctx, "admin@x", []string{"one", "two"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetQuotes(ctx, "admin@x", []string{"three"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetQuotes(ctx, "admin@x", []string{"x", "  "}); !errors.Is(err, apperr.Invalid) {
		t.Errorf("a blank quote = %v, want Invalid", err)
	}
	p, _ := s.Platform(ctx)
	if len(p.Settings.Quotes) != 1 || p.Settings.Quotes[0] != "three" {
		t.Errorf("quotes = %v, want [three]", p.Settings.Quotes)
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'quotes.update'`).Scan(&n)
	if n != 2 {
		t.Errorf("quotes.update audit rows = %d, want 2", n)
	}
}

func TestScheduleForeignKeyIsTheRealGuard(t *testing.T) {
	pool := dbtest.New(t)
	s := &Store{DB: pool}
	ctx := context.Background()
	sc := config.Schedule{Timezone: "UTC", Windows: []config.Window{{Days: []string{"mon"}, Start: "08:00", End: "19:00"}}}
	if err := s.SetSchedule(ctx, "admin@x", "days", sc); err != nil {
		t.Fatal(err)
	}
	mustExec(t, pool, `INSERT INTO teams (id, name) VALUES ('forge', 'The Forge')`)
	mustExec(t, pool, `INSERT INTO trainings (id, repo) VALUES ('forge-101','https://git/x.git')`)
	mustExec(t, pool, `INSERT INTO programs (team, training, schedule_name) VALUES ('forge','forge-101','days')`)
	_, err := pool.Exec(ctx, `DELETE FROM schedules WHERE name = 'days'`)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23503" {
		t.Fatalf("a direct delete of a used schedule must fail with a foreign key violation, got %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO programs (team, training, schedule_name) VALUES ('forge','x','nope')`); err == nil {
		t.Error("a program naming a missing schedule must be refused")
	}
}

func TestForeignKeyViolationSurfacesAsConflict(t *testing.T) {
	err := scheduleInUse(fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "23503"}), "days")
	if !errors.Is(err, apperr.Conflict) {
		t.Errorf("23503 = %v, want Conflict", err)
	}
	other := errors.New("boom")
	if scheduleInUse(other, "days") != other {
		t.Error("other errors must pass through")
	}
}

func settingsAuditDetail(t *testing.T, s *Store, n int) map[string]any {
	t.Helper()
	var raw []byte
	err := s.DB.QueryRow(context.Background(), `SELECT detail FROM audit_log WHERE action = 'settings.update' ORDER BY id OFFSET $1 LIMIT 1`, n).Scan(&raw)
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSettingsAuditRecordsNewValuesAndDistinguishesClearedFromZero(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	steps := []SettingsBody{
		{Version: 1, DefaultTheme: "anvil", CostTiers: &config.CostTiers{AutoApproveUSD: 2, Tier1USD: 5, Tier2USD: 25}, ClusterUSDPerHour: ptr(0.0)},
		{Version: 2, DefaultTheme: "anvil", CostTiers: &config.CostTiers{Tier1USD: 5, Tier2USD: 25}},
		{Version: 3, DefaultTheme: "anvil"},
	}
	for _, b := range steps {
		if err := s.SetSettings(ctx, "admin@x", b); err != nil {
			t.Fatal(err)
		}
	}
	d0 := settingsAuditDetail(t, s, 0)
	tiers, _ := d0["cost_tiers"].(map[string]any)
	if d0["default_theme"] != "anvil" || tiers["tier2_usd"] != 25.0 || tiers["auto_approve_usd"] != 2.0 || d0["cost_tiers_cleared"] != false {
		t.Errorf("tier change detail = %v", d0)
	}
	if v, ok := d0["cluster_usd_per_hour"]; !ok || v != 0.0 {
		t.Errorf("an explicit 0 rate must be recorded as 0, got %v", d0["cluster_usd_per_hour"])
	}
	if v, ok := settingsAuditDetail(t, s, 1)["cluster_usd_per_hour"]; !ok || v != nil {
		t.Errorf("an unset rate must be recorded as null, got %v", v)
	}
	d1, d2 := settingsAuditDetail(t, s, 1), settingsAuditDetail(t, s, 2)
	zero, _ := d1["cost_tiers"].(map[string]any)
	if zero["auto_approve_usd"] != 0.0 || d1["cost_tiers_cleared"] != false {
		t.Errorf("zero auto-approve detail = %v", d1)
	}
	if _, has := d2["cost_tiers"]; has || d2["cost_tiers_cleared"] != true {
		t.Errorf("clearing tiers must be explicit and carry no tier values, got %v", d2)
	}
}

func TestInTxRollsBackTheChangeWhenTheAuditWriteFails(t *testing.T) {
	pool := dbtest.New(t)
	s := &Store{DB: pool}
	ctx := context.Background()
	// A chan cannot be marshalled into the jsonb detail, so the audit insert fails after the change succeeded.
	err := s.inTx(ctx, "admin@x", "quotes.update", "quotes", map[string]any{"bad": make(chan int)}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO quotes (text) VALUES ('must not survive')`)
		return err
	})
	if err == nil {
		t.Fatal("want the audit failure to surface")
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM quotes`).Scan(&n)
	if n != 0 {
		t.Errorf("the change survived a failed audit write: %d quotes", n)
	}
}
