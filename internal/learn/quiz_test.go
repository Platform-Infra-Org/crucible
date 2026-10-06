package learn

import (
	"crucible/internal/scoring"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"crucible/internal/content"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func forge101(t *testing.T) *content.Training {
	t.Helper()
	tr, probs := content.Load("../../examples/forge-101")
	if len(probs) > 0 {
		t.Fatalf("fixture invalid: %v", probs)
	}
	return tr
}

// asPublic rewrites answers written in authored indices into the public ids a browser would send for seed.
func asPublic(q *content.Quiz, seed uint64, answers map[string]json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, x := range q.Questions {
		a, ok := answers[x.ID]
		if !ok {
			continue
		}
		n := len(x.Options)
		if x.Type == "match" {
			n = len(x.Pairs)
		}
		perm := idPerm(seed, x.ID, n)
		var one int
		var many []int
		switch {
		case x.Type == "single" && json.Unmarshal(a, &one) == nil && one >= 0 && one < n:
			a, _ = json.Marshal(perm[one])
		case (x.Type == "multi" || x.Type == "order" || x.Type == "match") && json.Unmarshal(a, &many) == nil:
			for i := range many {
				many[i] = perm[many[i]]
			}
			a, _ = json.Marshal(many)
		}
		out[x.ID] = a
	}
	return out
}

// correctAnswers is the right answer to 01-welcome in authored indices (see asPublic).
func correctAnswers() map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"q-ps": raw(`0`), "q-registries": raw(`[3,0,1]`), "q-port": raw(`" 80 "`),
		"q-version": raw(`"2.10.0"`), "q-order": raw(`[0,1,2,3]`), "q-match": raw(`[0,1,2]`),
	}
}

func TestScore(t *testing.T) {
	q := forge101(t).Module("01-welcome").Quiz
	const seed = 99
	score := func(a map[string]json.RawMessage) Result { return Score(q, seed, asPublic(q, seed, a)) }
	res := score(correctAnswers())
	if res.Score != 6 || res.Max != 6 || !res.Passed {
		t.Fatalf("all correct: %+v", res)
	}

	one := correctAnswers()
	one["q-port"] = raw(`"8080"`)
	if res := score(one); !res.Passed || res.Correct["q-port"] {
		t.Fatalf("5/6 = 83%% should pass the 80%% threshold: %+v", res)
	}

	two := correctAnswers()
	two["q-port"], two["q-order"] = raw(`"8080"`), raw(`[1,0,2,3]`)
	if res := score(two); res.Passed {
		t.Fatalf("4/6 must fail: %+v", res)
	}

	if res := Score(q, seed, map[string]json.RawMessage{"q-ps": raw(`"not a number"`)}); res.Score != 0 {
		t.Fatalf("garbage answers score 0: %+v", res)
	}
}

func TestPublicQuizHidesAnswers(t *testing.T) {
	tr := forge101(t)
	b, _ := json.Marshal(PublicQuiz(tr.Module("01-welcome").Quiz, 42))
	s := string(b)
	for _, leak := range []string{`"answer"`, `"rubric"`, `checks/`} {
		if strings.Contains(s, leak) {
			t.Fatalf("public quiz leaks %s: %s", leak, s)
		}
	}
	if got := PublicQuiz(tr.Module("02-first-lab").Quiz, 42); len(got) != 0 {
		t.Fatalf("terminal questions must not appear in the standalone quiz: %+v", got)
	}
	a, _ := json.Marshal(PublicQuiz(tr.Module("01-welcome").Quiz, 7))
	b2, _ := json.Marshal(PublicQuiz(tr.Module("01-welcome").Quiz, 7))
	if string(a) != string(b2) {
		t.Fatal("shuffle must be deterministic per seed")
	}
}

// The authored order is the answer for order/match questions, so public ids must not encode it.
func TestPublicIDsDoNotRevealOrderOrMatch(t *testing.T) {
	q := forge101(t).Module("01-welcome").Quiz
	const seed = 12345
	pub := PublicQuiz(q, seed)
	byID := map[string]PublicQuestion{}
	for _, p := range pub {
		byID[p.ID] = p
	}
	for _, x := range q.Questions {
		if x.Type != "order" && x.Type != "match" {
			continue
		}
		p := byID[x.ID]
		texts, choices := x.Options, p.Options
		if x.Type == "match" {
			texts, choices = nil, p.Right
			for _, pair := range x.Pairs {
				texts = append(texts, pair[1])
			}
		}
		// A correct answer built only from what the browser sees (texts → ids) passes.
		ids := []int{}
		for _, txt := range texts {
			for _, c := range choices {
				if c.Text == txt {
					ids = append(ids, c.ID)
				}
			}
		}
		good, _ := json.Marshal(ids)
		if res := Score(q, seed, map[string]json.RawMessage{x.ID: good}); !res.Correct[x.ID] {
			t.Fatalf("%s: answer built from texts %s must be correct", x.ID, good)
		}
		// Sorting the public ids ([0..n-1]) must not be the answer.
		sorted := make([]int, len(texts))
		identity := true
		for i := range sorted {
			sorted[i] = i
			identity = identity && ids[i] == i
		}
		if identity {
			t.Fatalf("%s: seed %d gives the identity permutation; pick another seed", x.ID, seed)
		}
		bad, _ := json.Marshal(sorted)
		if res := Score(q, seed, map[string]json.RawMessage{x.ID: bad}); res.Correct[x.ID] {
			t.Fatalf("%s: submitting sorted public ids must fail", x.ID)
		}
	}
}

func TestQuizOutcome(t *testing.T) {
	tr, probs := content.Load("../../examples/forge-301")
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	q := tr.Module("01-temper").Quiz // instant 1 pt; human: q-why 5, q-log 2, q-demo (signoff) 3; threshold 0.6
	sub := func(status string, pts float64) *scoring.Submission {
		return &scoring.Submission{Status: status, Points: pts}
	}
	cases := []struct {
		name      string
		best      float64
		attempted bool
		subs      map[string]*scoring.Submission
		want      string
		pct       float64
	}{
		{"nothing yet", 0, false, nil, "in_progress", 0},
		{"answers pending, sign-off not given", 1, true, map[string]*scoring.Submission{"q-why": sub("pending", 0), "q-log": sub("pending", 0)}, "pending_review", 1.0 / 11},
		{"one returned", 1, true, map[string]*scoring.Submission{"q-why": sub("scored", 5), "q-log": sub("returned", 0)}, "in_progress", 6.0 / 11},
		{"one unanswered", 1, true, map[string]*scoring.Submission{"q-why": sub("scored", 5)}, "in_progress", 6.0 / 11},
		{"all scored", 1, true, map[string]*scoring.Submission{"q-why": sub("scored", 5), "q-log": sub("scored", 2), "q-demo": sub("scored", 3)}, "complete", 1},
		{"all scored, too low", 0, true, map[string]*scoring.Submission{"q-why": sub("scored", 1), "q-log": sub("scored", 0), "q-demo": sub("scored", 3)}, "in_progress", 4.0 / 11},
		{"older version's instant points", 3, true, map[string]*scoring.Submission{"q-why": sub("scored", 5), "q-log": sub("scored", 2), "q-demo": sub("scored", 3)}, "complete", 1},
		{"instant part never tried", 0, false, map[string]*scoring.Submission{"q-why": sub("scored", 5), "q-log": sub("scored", 2), "q-demo": sub("scored", 3)}, "in_progress", 10.0 / 11},
	}
	for _, c := range cases {
		got, pct := quizOutcome(q, c.best, c.attempted, c.subs)
		if got != c.want || math.Abs(pct-c.pct) > 1e-9 {
			t.Errorf("%s: got %s %.3f, want %s %.3f", c.name, got, pct, c.want, c.pct)
		}
	}
	allHuman := &content.Quiz{PassThreshold: 0.5, Questions: []*content.Question{{ID: "a", Type: "text", Points: 2}}}
	if got, _ := quizOutcome(allHuman, 0, false, map[string]*scoring.Submission{"a": sub("scored", 2)}); got != "complete" {
		t.Errorf("an all-human quiz needs no instant attempt: %s", got)
	}
}
