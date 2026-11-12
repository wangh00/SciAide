package userinput

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestQuestionsSupportFreeTextAndCanonicalSelections(t *testing.T) {
	q, err := Decode(json.RawMessage(`{"questions":[{"id":"scope","question":"研究什么？","required":true,"selectionMode":"multiple","options":[{"id":"a","label":"甲"},{"id":"b","label":"乙"}]},{"id":"note","question":"补充？","required":false}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []Answer{{Text: "自己的方向"}, {OptionIDs: []string{"b", "a"}, Text: "只看近年"}} {
		normalized, e := NormalizeAnswers(q.Questions, Answers{"scope": a})
		if e != nil {
			t.Fatal(e)
		}
		if len(a.OptionIDs) > 0 && strings.Join(normalized["scope"].OptionIDs, ",") != "a,b" {
			t.Fatal("not canonical", normalized)
		}
		if !strings.Contains(AnswerText(q.Questions, normalized), a.Text) {
			t.Fatal("free text lost")
		}
	}
	for _, a := range []Answers{{}, {"unknown": {Text: "x"}}, {"scope": {OptionIDs: []string{"not-offered"}}}, {"scope": {OptionIDs: []string{"a", "a"}}}} {
		if _, e := NormalizeAnswers(q.Questions, a); e == nil {
			t.Fatal("invalid answer accepted", a)
		}
	}
	q.Questions[0].SelectionMode = "single"
	if _, e := NormalizeAnswers(q.Questions, Answers{"scope": {OptionIDs: []string{"a", "b"}}}); e == nil {
		t.Fatal("single allowed multiple")
	}
}

func TestDecodeRejectsMalformedQuestionButNotScientificAmbiguity(t *testing.T) {
	for _, raw := range []string{`{}`, `{"questions":[{"id":"a","question":"?"},{"id":"a","question":"?"}]}`, `{"questions":[{"id":"a","question":"?","options":[{"id":"o","label":"A"},{"id":"o","label":"B"}]}]}`} {
		if _, err := Decode(json.RawMessage(raw)); err == nil {
			t.Fatal("invalid protocol accepted", raw)
		}
	}
	if _, err := Decode(json.RawMessage(`{"questions":[{"id":"open","question":"你想做什么？","required":true}]}`)); err != nil {
		t.Fatal(err)
	}
}
