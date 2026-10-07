package degrade

import (
	"strings"
	"testing"
)

func TestParseNumbersKeepsTheLongestRun(t *testing.T) {
	text := "先给出 3 个示例。然后是 12, 40, 7, 200, 15 完成。"
	got := ParseNumbers(text)
	if len(got) != 5 || got[0] != 12 || got[4] != 15 {
		t.Fatalf("numbers = %v", got)
	}
}

func TestAnalyzeRejectsShortAnswers(t *testing.T) {
	_, err := Analyze([]Sample{{ExpectedCount: 300, Text: "1 2 3"}})
	if err == nil {
		t.Fatal("short answer was accepted")
	}
}

func TestAnalyzeNamesTheDominantDigit(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 300; i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString("7")
	}
	result, err := Analyze([]Sample{{ExpectedCount: 300, Text: b.String()}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Used != 1 || result.Prediction == "" || result.Probability <= 0 {
		t.Fatalf("attribution = %+v", result)
	}
	if !result.Consistent("not-a-real-model") && result.Consistent(result.Prediction) != true {
		t.Fatal("prediction did not match itself")
	}
}

func TestSameModelKeepsVersionDigits(t *testing.T) {
	if !sameModel("GPT-6-Luna", "gpt-6-luna") {
		t.Fatal("case or nothing else should matter")
	}
	if sameModel("gpt-6.1-luna", "gpt-6-luna") {
		t.Fatal("a dotted minor version collapsed into the dashed model")
	}
	if sameModel("gpt-6-luna", "gpt-6-sol") {
		t.Fatal("different models were treated as the same")
	}
}
