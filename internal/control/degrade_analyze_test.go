package control

import (
	"strconv"
	"strings"
	"testing"

	"gpt-load/internal/degrade"
)

// degradeAnswerText builds one plausible answer: a long run of in-range integers.
func degradeTestAnswerText(count int) string {
	parts := make([]string, count)
	for i := range parts {
		// Stay inside ModelTrace's 1..355 window.
		parts[i] = strconv.Itoa(1 + (i*37)%355)
	}
	return strings.Join(parts, ", ")
}

// TestAnalyzeDegradeSamplesScoresBeforeScrubbingText covers the bug where the
// collected bodies were blanked before Analyze ran, so every real answer was
// reported as "未采集到有效数字序列" even when the model answered correctly.
func TestAnalyzeDegradeSamplesScoresBeforeScrubbingText(t *testing.T) {
	collected := []degrade.Sample{
		{ExpectedCount: 292, Text: degradeTestAnswerText(292), Parsed: 292, Accepted: true},
		{ExpectedCount: 305, Text: degradeTestAnswerText(305), Parsed: 305, Accepted: true},
		{ExpectedCount: 318, Text: degradeTestAnswerText(318), Parsed: 318, Accepted: true},
	}

	attribution, err := analyzeDegradeSamples(collected)
	if err != nil {
		t.Fatalf("analyzeDegradeSamples() error = %v, want a scored verdict from real answers", err)
	}
	if attribution.Used != len(collected) {
		t.Fatalf("attribution.Used = %d, want %d scored samples", attribution.Used, len(collected))
	}
	if attribution.Prediction == "" {
		t.Fatal("attribution.Prediction is empty, want the closest model")
	}

	// The board must not keep the full answers.
	for i, sample := range collected {
		if sample.Text != "" {
			t.Fatalf("collected[%d].Text = %q, want the answer body dropped after scoring", i, sample.Text)
		}
	}
	if len(attribution.Diagnostics) != len(collected) {
		t.Fatalf("diagnostics = %d, want %d entries kept for the board", len(attribution.Diagnostics), len(collected))
	}
}

// TestAnalyzeDegradeSamplesStillReportsEmptyAnswers keeps the honest failure
// path: with no usable numbers the caller must see the explicit reason.
func TestAnalyzeDegradeSamplesStillReportsEmptyAnswers(t *testing.T) {
	collected := []degrade.Sample{
		{ExpectedCount: 292, Text: "", Error: "有效数字不足"},
		{ExpectedCount: 305, Text: "", Error: "有效数字不足"},
		{ExpectedCount: 318, Text: "", Error: "有效数字不足"},
	}

	if _, err := analyzeDegradeSamples(collected); err == nil {
		t.Fatal("analyzeDegradeSamples() error = nil, want a reason when no numbers were collected")
	} else if !strings.Contains(err.Error(), "未采集到有效数字序列") {
		t.Fatalf("analyzeDegradeSamples() error = %v, want the empty-sequence reason", err)
	}
}
