// Package learn serves trainings: outlines, reading, quizzes and progress.
package learn

import (
	"encoding/json"
	"hash/fnv"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"

	"crucible/internal/content"
	"crucible/internal/scoring"
)

type Choice struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
}

type PublicQuestion struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Prompt     string            `json:"prompt"`
	Points     float64           `json:"points"`
	Options    []Choice          `json:"options,omitempty"`
	Left       []string          `json:"left,omitempty"`
	Right      []Choice          `json:"right,omitempty"`
	Human      bool              `json:"human,omitempty"`
	Submission *scoring.Feedback `json:"submission,omitempty"` // the trainee's latest answer to a human question
}

type Result struct {
	Score        float64         `json:"score"`
	Max          float64         `json:"max"`
	Percent      float64         `json:"percent"`
	Passed       bool            `json:"passed"`
	Correct      map[string]bool `json:"correct"`
	PendingHuman bool            `json:"pending_human"`
	Status       string          `json:"status"` // the quiz item after this attempt: in_progress | pending_review | complete
}

// idPerm returns the opaque public ids for one question's n choices: perm[original index] = public id.
// It depends on the per-user seed (which mixes in the server's quiz secret) and the question id, so the
// browser cannot recover the authored order (= the answer for order/match) from the ids it receives.
func idPerm(seed uint64, questionID string, n int) []int {
	h := fnv.New64a()
	h.Write([]byte(questionID))
	return rand.New(rand.NewPCG(seed, h.Sum64())).Perm(n)
}

// PublicQuiz is what the browser receives: no answers, opaque choice ids, and terminal questions are left to labs.
// Choices are listed in ascending public-id order, which for order/match is a seeded shuffle.
func PublicQuiz(q *content.Quiz, seed uint64) []PublicQuestion {
	choices := func(texts []string, perm []int) []Choice {
		c := make([]Choice, len(texts))
		for i, t := range texts {
			c[perm[i]] = Choice{ID: perm[i], Text: t}
		}
		return c
	}
	out := []PublicQuestion{}
	for _, x := range q.Questions {
		if x.Type == "terminal" {
			continue
		}
		pq := PublicQuestion{ID: x.ID, Type: x.Type, Prompt: x.Prompt, Points: x.Points, Human: content.IsHuman(x.Type)}
		switch x.Type {
		case "single", "multi", "order":
			if x.Type == "order" {
				pq.Options = choices(x.Options, idPerm(seed, x.ID, len(x.Options)))
			} else { // authored display order, opaque ids
				perm := idPerm(seed, x.ID, len(x.Options))
				for i, o := range x.Options {
					pq.Options = append(pq.Options, Choice{ID: perm[i], Text: o})
				}
			}
		case "match":
			right := make([]string, len(x.Pairs))
			for i, p := range x.Pairs {
				pq.Left = append(pq.Left, p[0])
				right[i] = p[1]
			}
			pq.Right = choices(right, idPerm(seed, x.ID, len(x.Pairs)))
		}
		out = append(out, pq)
	}
	return out
}

// Score grades answers given in public ids; seed must be the one PublicQuiz was called with.
func Score(q *content.Quiz, seed uint64, answers map[string]json.RawMessage) Result {
	res := Result{Correct: map[string]bool{}}
	for _, x := range q.Questions {
		if x.Type == "terminal" {
			continue
		}
		if content.IsHuman(x.Type) {
			res.PendingHuman = true // answered and scored separately (AnswerHuman)
			continue
		}
		res.Max += x.Points
		ok := correct(x, toOriginal(x, seed, answers[x.ID]))
		res.Correct[x.ID] = ok
		if ok {
			res.Score += x.Points
		}
	}
	if res.Max > 0 {
		res.Percent = res.Score / res.Max
	}
	res.Passed = res.Max > 0 && res.Percent >= q.PassThreshold-1e-9 // instant part only; SubmitQuiz decides the item
	return res
}

// toOriginal rewrites public choice ids in an answer back to authored indices (unknown ids become -1).
func toOriginal(x *content.Question, seed uint64, raw json.RawMessage) json.RawMessage {
	n := len(x.Options)
	switch x.Type {
	case "match":
		n = len(x.Pairs)
	case "single", "multi", "order":
	default:
		return raw
	}
	inv := make([]int, n)
	for orig, pub := range idPerm(seed, x.ID, n) {
		inv[pub] = orig
	}
	back := func(id int) int {
		if id < 0 || id >= n {
			return -1
		}
		return inv[id]
	}
	if x.Type == "single" {
		var id int
		if json.Unmarshal(raw, &id) != nil {
			return nil
		}
		b, _ := json.Marshal(back(id))
		return b
	}
	var ids []int
	if json.Unmarshal(raw, &ids) != nil {
		return nil
	}
	for i := range ids {
		ids[i] = back(ids[i])
	}
	b, _ := json.Marshal(ids)
	return b
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

// quizOutcome decides the quiz item from the best instant attempt and the latest human submissions (spec §7):
// complete only when every human question is scored and the total reaches the pass threshold; pending_review when the
// only things missing are scorers' decisions; otherwise in_progress.
func quizOutcome(q *content.Quiz, bestInstant float64, attempted bool, subs map[string]*scoring.Submission) (string, float64) {
	var instantMax, humanMax, humanScore float64
	waiting, open := false, false
	for _, x := range q.Questions {
		switch {
		case x.Type == "terminal":
		case content.IsHuman(x.Type):
			humanMax += x.Points
			sub := subs[x.ID]
			switch {
			case sub != nil && sub.Status == scoring.Scored:
				humanScore += sub.Points
			case sub != nil && sub.Status == scoring.Pending, sub == nil && x.Type == "signoff":
				waiting = true
			default: // unanswered, or returned for rework
				open = true
			}
		default:
			instantMax += x.Points
		}
	}
	if instantMax > 0 && !attempted {
		open = true
	}
	pct := 0.0
	if total := instantMax + humanMax; total > 0 {
		pct = (bestInstant + humanScore) / total
	}
	switch {
	case open:
		return "in_progress", pct
	case waiting:
		return "pending_review", pct
	case pct >= q.PassThreshold-1e-9:
		return "complete", pct
	}
	return "in_progress", pct
}
