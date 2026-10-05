package config

import (
	"strings"
	"testing"
	"time"
)

func businessHours(t *testing.T) *Schedule {
	t.Helper()
	s := &Schedule{Timezone: "Europe/Bucharest", Windows: []Window{{Days: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "08:00", End: "19:00"}}}
	if err := s.validate(); err != nil {
		t.Fatal(err)
	}
	return s
}

// local parses a Bucharest wall-clock time. 2026-10-05 is a Monday; DST ends on Sunday 2026-10-25.
func local(t *testing.T, s string) time.Time {
	t.Helper()
	loc, _ := time.LoadLocation("Europe/Bucharest")
	v, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestScheduleArithmetic(t *testing.T) {
	s := businessHours(t)
	for in, want := range map[string]bool{"2026-10-09 18:59": true, "2026-10-09 19:00": false, "2026-10-10 10:00": false, "2026-10-12 08:00": true} {
		if got := s.Open(local(t, in)); got != want {
			t.Errorf("Open(%s) = %v", in, got)
		}
	}
	if got := s.End(local(t, "2026-10-07 10:00")); !got.Equal(local(t, "2026-10-07 19:00")) {
		t.Errorf("End = %v", got)
	}
	if got := s.End(local(t, "2026-10-10 10:00")); !got.IsZero() {
		t.Errorf("End outside a window must be zero, got %v", got)
	}
	if got := s.NextOpen(local(t, "2026-10-09 20:00")); !got.Equal(local(t, "2026-10-12 08:00")) {
		t.Errorf("NextOpen = %v", got)
	}
	cases := []struct{ from, want string }{
		{"2026-10-09 17:00", "2026-10-12 10:00"}, // 2h on Friday + 2h on Monday
		{"2026-10-10 12:00", "2026-10-12 12:00"}, // requested on Saturday: the clock starts Monday 08:00
		{"2026-10-07 09:00", "2026-10-07 13:00"},
		{"2026-10-23 17:00", "2026-10-26 10:00"}, // across the DST change, wall-clock still right
	}
	for _, c := range cases {
		if got := s.AddOpen(local(t, c.from), 4*time.Hour); !got.Equal(local(t, c.want)) {
			t.Errorf("AddOpen(%s, 4h) = %v, want %s", c.from, got.In(s.Location()), c.want)
		}
	}
	if got := s.String(); got != "mon,tue,wed,thu,fri 08:00–19:00 (Europe/Bucharest)" {
		t.Errorf("String = %q", got)
	}
}

func TestAlwaysOpenAndNilSchedules(t *testing.T) {
	all := &Schedule{Timezone: "UTC", Windows: []Window{{Days: []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}, Start: "00:00", End: "24:00"}}}
	if err := all.validate(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	if !all.Open(now) || !all.End(now).IsZero() {
		t.Fatalf("24/7 schedule: open %v end %v", all.Open(now), all.End(now))
	}
	var none *Schedule
	if !none.Open(now) || !none.End(now).IsZero() || !none.NextOpen(now).Equal(now) || !none.AddOpen(now, time.Hour).Equal(now.Add(time.Hour)) || none.String() != "any time" {
		t.Fatal("a nil schedule means any time")
	}
}

func TestScheduleValidation(t *testing.T) {
	cases := map[string]struct {
		s    Schedule
		want string
	}{
		"bad zone":     {Schedule{Timezone: "Mars/Olympus", Windows: []Window{{Days: []string{"mon"}, Start: "08:00", End: "09:00"}}}, "timezone"},
		"no windows":   {Schedule{Timezone: "UTC"}, "at least one window"},
		"unknown day":  {Schedule{Timezone: "UTC", Windows: []Window{{Days: []string{"funday"}, Start: "08:00", End: "09:00"}}}, "unknown day"},
		"short time":   {Schedule{Timezone: "UTC", Windows: []Window{{Days: []string{"mon"}, Start: "8:00", End: "09:00"}}}, "HH:MM"},
		"end <= start": {Schedule{Timezone: "UTC", Windows: []Window{{Days: []string{"mon"}, Start: "22:00", End: "02:00"}}}, "end must be after start"},
	}
	for name, c := range cases {
		if err := c.s.validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", name, c.want, err)
		}
	}
}
