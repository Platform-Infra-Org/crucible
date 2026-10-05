package learn

import (
	"encoding/json"
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

func correctAnswers() map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"q-ps": raw(`0`), "q-registries": raw(`[3,0,1]`), "q-port": raw(`" 80 "`),
		"q-version": raw(`"2.10.0"`), "q-order": raw(`[0,1,2,3]`), "q-match": raw(`[0,1,2]`),
	}
}

func TestScore(t *testing.T) {
	q := forge101(t).Module("01-welcome").Quiz
	res := Score(q, correctAnswers())
	if res.Score != 6 || res.Max != 6 || !res.Passed {
		t.Fatalf("all correct: %+v", res)
	}

	one := correctAnswers()
	one["q-port"] = raw(`"8080"`)
	if res := Score(q, one); !res.Passed || res.Correct["q-port"] {
		t.Fatalf("5/6 = 83%% should pass the 80%% threshold: %+v", res)
	}

	two := correctAnswers()
	two["q-port"], two["q-order"] = raw(`"8080"`), raw(`[1,0,2,3]`)
	if res := Score(q, two); res.Passed {
		t.Fatalf("4/6 must fail: %+v", res)
	}

	if res := Score(q, map[string]json.RawMessage{"q-ps": raw(`"not a number"`)}); res.Score != 0 {
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
