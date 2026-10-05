// Package learn serves trainings: outlines, reading, quizzes and progress.
package learn

import (
	"encoding/json"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"

	"crucible/internal/content"
)

type Choice struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
}

type PublicQuestion struct {
	ID      string   `json:"id"`
	Type    string   `json:"type"`
	Prompt  string   `json:"prompt"`
	Points  float64  `json:"points"`
	Options []Choice `json:"options,omitempty"`
	Left    []string `json:"left,omitempty"`
	Right   []Choice `json:"right,omitempty"`
	Human   bool     `json:"human,omitempty"`
}

type Result struct {
	Score        float64         `json:"score"`
	Max          float64         `json:"max"`
	Percent      float64         `json:"percent"`
	Passed       bool            `json:"passed"`
	Correct      map[string]bool `json:"correct"`
	PendingHuman bool            `json:"pending_human"`
}

// PublicQuiz is what the browser receives: no answers, and terminal questions are left to labs.
func PublicQuiz(q *content.Quiz, seed uint64) []PublicQuestion {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	shuffle := func(c []Choice) { r.Shuffle(len(c), func(i, j int) { c[i], c[j] = c[j], c[i] }) }
	out := []PublicQuestion{}
	for _, x := range q.Questions {
		if x.Type == "terminal" {
			continue
		}
		pq := PublicQuestion{ID: x.ID, Type: x.Type, Prompt: x.Prompt, Points: x.Points, Human: content.IsHuman(x.Type)}
		switch x.Type {
		case "single", "multi", "order":
			for i, o := range x.Options {
				pq.Options = append(pq.Options, Choice{ID: i, Text: o})
			}
			if x.Type == "order" {
				shuffle(pq.Options)
			}
		case "match":
			for i, p := range x.Pairs {
				pq.Left = append(pq.Left, p[0])
				pq.Right = append(pq.Right, Choice{ID: i, Text: p[1]})
			}
			shuffle(pq.Right)
		}
		out = append(out, pq)
	}
	return out
}

func Score(q *content.Quiz, answers map[string]json.RawMessage) Result {
	res := Result{Correct: map[string]bool{}}
	for _, x := range q.Questions {
		if x.Type == "terminal" {
			continue
		}
		if content.IsHuman(x.Type) {
			res.PendingHuman = true // scored by people in M5
			continue
		}
		res.Max += x.Points
		ok := correct(x, answers[x.ID])
		res.Correct[x.ID] = ok
		if ok {
			res.Score += x.Points
		}
	}
	if res.Max > 0 {
		res.Percent = res.Score / res.Max
	}
	res.Passed = res.Max > 0 && !res.PendingHuman && res.Percent >= q.PassThreshold-1e-9
	return res
}

func correct(x *content.Question, raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	switch x.Type {
	case "single":
		var got, want int
		return json.Unmarshal(raw, &got) == nil && x.Answer.Decode(&want) == nil && got == want
	case "multi":
		var got, want []int
		if json.Unmarshal(raw, &got) != nil || x.Answer.Decode(&want) != nil {
			return false
		}
		slices.Sort(got)
		slices.Sort(want)
		return slices.Equal(slices.Compact(got), slices.Compact(want))
	case "exact":
		var got, want string
		if json.Unmarshal(raw, &got) != nil || x.Answer.Decode(&want) != nil {
			return false
		}
		got, want = strings.TrimSpace(got), strings.TrimSpace(want)
		if x.CaseSensitive {
			return got == want
		}
		return strings.EqualFold(got, want)
	case "regex":
		var got, pat string
		if json.Unmarshal(raw, &got) != nil || x.Answer.Decode(&pat) != nil {
			return false
		}
		flags := "(?i)"
		if x.CaseSensitive {
			flags = ""
		}
		re, err := regexp.Compile(flags + "^(?:" + pat + ")$")
		return err == nil && re.MatchString(strings.TrimSpace(got))
	case "order", "match":
		var got []int
		n := len(x.Options)
		if x.Type == "match" {
			n = len(x.Pairs)
		}
		if json.Unmarshal(raw, &got) != nil || len(got) != n {
			return false
		}
		for i, v := range got {
			if v != i { // options/pairs are authored in the correct order
				return false
			}
		}
		return true
	}
	return false
}
