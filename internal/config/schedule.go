package config

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

var weekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"} // index = time.Weekday

// Schedule is a named set of weekly windows in one time zone (spec §9.2), e.g. Mon–Fri 08:00–19:00 Europe/Bucharest.
// A nil *Schedule means "any time": every method treats it as always open.
type Schedule struct {
	Timezone string   `yaml:"timezone" json:"timezone"`
	Windows  []Window `yaml:"windows" json:"windows"`
	loc      *time.Location
}

type Window struct {
	Days  []string `yaml:"days" json:"days"`   // mon … sun
	Start string   `yaml:"start" json:"start"` // "08:00"
	End   string   `yaml:"end" json:"end"`     // "19:00"; "24:00" is midnight; after Start (a window cannot cross midnight)
	days  [7]bool
	start int // minutes after midnight
	end   int
}

func parseHHMM(s string) (int, error) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || len(s) != 5 || h < 0 || m < 0 || m > 59 || h*60+m > 24*60 {
		return 0, fmt.Errorf("time %q must be HH:MM between 00:00 and 24:00", s)
	}
	return h*60 + m, nil
}

// Validate checks the zone and windows and prepares the schedule for use.
func (s *Schedule) Validate() error { return s.validate() }

func (s *Schedule) validate() error {
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil || s.Timezone == "" {
		return fmt.Errorf("timezone %q is not a known IANA zone", s.Timezone)
	}
	s.loc = loc
	if len(s.Windows) == 0 {
		return fmt.Errorf("at least one window is required")
	}
	for i := range s.Windows {
		w := &s.Windows[i]
		if len(w.Days) == 0 {
			return fmt.Errorf("window %d: days are required", i+1)
		}
		for _, d := range w.Days {
			idx := slices.Index(weekdays, strings.ToLower(d))
			if idx < 0 {
				return fmt.Errorf("window %d: unknown day %q (use mon … sun)", i+1, d)
			}
			w.days[idx] = true
		}
		if w.start, err = parseHHMM(w.Start); err != nil {
			return fmt.Errorf("window %d: %w", i+1, err)
		}
		if w.end, err = parseHHMM(w.End); err != nil {
			return fmt.Errorf("window %d: %w", i+1, err)
		}
		if w.end <= w.start {
			return fmt.Errorf("window %d: end must be after start (a window cannot cross midnight; add a second window)", i+1)
		}
	}
	return nil
}

type span struct{ from, to time.Time }

// spans returns the open intervals from the day before t through `days` days after it, merged and in order.
// Building each window with time.Date in the schedule's zone keeps wall-clock times right across DST changes.
func (s *Schedule) spans(t time.Time, days int) []span {
	lt := t.In(s.loc)
	day0 := time.Date(lt.Year(), lt.Month(), lt.Day()-1, 0, 0, 0, 0, s.loc)
	var out []span
	for i := 0; i <= days; i++ {
		d := day0.AddDate(0, 0, i)
		for _, w := range s.Windows {
			if w.days[d.Weekday()] {
				out = append(out, span{
					time.Date(d.Year(), d.Month(), d.Day(), 0, w.start, 0, 0, s.loc),
					time.Date(d.Year(), d.Month(), d.Day(), 0, w.end, 0, 0, s.loc),
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].from.Before(out[j].from) })
	merged := out[:0]
	for _, sp := range out {
		if n := len(merged); n > 0 && !sp.from.After(merged[n-1].to) {
			if sp.to.After(merged[n-1].to) {
				merged[n-1].to = sp.to
			}
			continue
		}
		merged = append(merged, sp)
	}
	return merged
}

const horizon = 8 // days looked ahead; an open stretch running past a week is treated as "never closes"

func (s *Schedule) Open(t time.Time) bool {
	if s == nil {
		return true
	}
	for _, sp := range s.spans(t, 2) {
		if !t.Before(sp.from) && t.Before(sp.to) {
			return true
		}
	}
	return false
}

// End is when the open stretch containing t closes. Zero when t is outside the schedule or it never closes (24/7).
func (s *Schedule) End(t time.Time) time.Time {
	if s == nil {
		return time.Time{}
	}
	for _, sp := range s.spans(t, horizon) {
		if !t.Before(sp.from) && t.Before(sp.to) {
			if sp.to.Sub(t) > 7*24*time.Hour {
				return time.Time{}
			}
			return sp.to
		}
	}
	return time.Time{}
}

// NextOpen is t when open, else the next opening within a week (zero if none).
func (s *Schedule) NextOpen(t time.Time) time.Time {
	if s == nil {
		return t
	}
	for _, sp := range s.spans(t, horizon) {
		if t.Before(sp.to) {
			if t.Before(sp.from) {
				return sp.from
			}
			return t
		}
	}
	return time.Time{}
}

// AddOpen returns the instant when d of open time has passed since t: business-hours arithmetic for escalations (spec §9.1).
func (s *Schedule) AddOpen(t time.Time, d time.Duration) time.Time {
	if s == nil {
		return t.Add(d)
	}
	for range 60 { // each round looks a week ahead; escalations are a few hours
		for _, sp := range s.spans(t, horizon) {
			if !sp.to.After(t) {
				continue
			}
			from := sp.from
			if t.After(from) {
				from = t
			}
			if left := sp.to.Sub(from); d <= left {
				return from.Add(d)
			} else {
				d -= left
			}
			t = sp.to
		}
	}
	return time.Time{}
}

// String describes the schedule for people, e.g. "mon,tue,wed,thu,fri 08:00–19:00 (Europe/Bucharest)".
func (s *Schedule) String() string {
	if s == nil {
		return "any time"
	}
	var parts []string
	for _, w := range s.Windows {
		parts = append(parts, strings.Join(w.Days, ",")+" "+w.Start+"–"+w.End)
	}
	return strings.Join(parts, "; ") + " (" + s.Timezone + ")"
}

// Location is the schedule's time zone (UTC for "any time"); format user-facing times in it.
func (s *Schedule) Location() *time.Location {
	if s == nil {
		return time.UTC
	}
	return s.loc
}
