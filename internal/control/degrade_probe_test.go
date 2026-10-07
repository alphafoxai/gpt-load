package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/degrade"
	"gpt-load/internal/execution"
	"gpt-load/internal/storage/models"
	"gpt-load/internal/usage"
)

// degradeScriptedStep is one scripted upstream exchange: the payload the
// upstream streams, and how the exchange ends.
type degradeScriptedStep struct {
	answer    string
	status    int
	kind      execution.ErrorKind
	summary   string
	responded bool
}

// degradeScriptedExecutor replays a script per call, so a test can assert how
// many attempts the probe made and what each one asked for.
type degradeScriptedExecutor struct {
	mu    sync.Mutex
	steps []degradeScriptedStep
	calls int
}

func (executor *degradeScriptedExecutor) next() degradeScriptedStep {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	index := executor.calls
	executor.calls++
	if index >= len(executor.steps) {
		index = len(executor.steps) - 1
	}
	return executor.steps[index]
}

func (executor *degradeScriptedExecutor) callCount() int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.calls
}

func (*degradeScriptedExecutor) Execute(
	context.Context,
	execution.AttemptSpec,
) execution.AttemptResult {
	panic("degrade probe must stream")
}

func (executor *degradeScriptedExecutor) ExecuteStream(
	_ context.Context,
	spec execution.AttemptSpec,
	sink execution.StreamSink,
) execution.StreamResult {
	step := executor.next()
	if step.responded {
		if err := sink(execution.StreamEvent{
			Sequence: 1, Kind: execution.StreamEventReady,
			StatusCode: step.status, Header: http.Header{},
		}); err != nil {
			return execution.StreamResult{DispatchState: execution.DispatchMaybeSent, ResponseStarted: true}
		}
	}
	if step.answer != "" {
		if err := sink(execution.StreamEvent{
			Sequence: 2, Kind: execution.StreamEventData, Data: degradeDeltaStream(step.answer),
		}); err != nil {
			return execution.StreamResult{DispatchState: execution.DispatchMaybeSent, ResponseStarted: true}
		}
	}
	result := execution.StreamResult{
		DispatchState: execution.DispatchMaybeSent, ResponseStarted: step.responded,
		StatusCode: step.status, Header: http.Header{}, Model: spec.UpstreamModel,
		Usage: &execution.UsageEvidence{Normalized: usage.Result{Tokens: usage.Tokens{
			UncachedInput: 11, CacheRead: 5, Output: 7,
		}}},
	}
	if step.kind != "" {
		result.Error = &execution.ErrorEvidence{Kind: step.kind, StatusCode: step.status, Summary: step.summary}
	}
	return result
}

// degradeDeltaStream renders an answer the way Codex streams it: a delta per
// chunk and a terminal event whose output array is empty.
func degradeDeltaStream(answer string) []byte {
	events := make([]byte, 0, len(answer)+128)
	mid := len(answer) / 2
	for _, chunk := range []string{answer[:mid], answer[mid:]} {
		payload, err := json.Marshal(map[string]string{"type": "response.output_text.delta", "delta": chunk})
		if err != nil {
			panic(err)
		}
		events = append(events, []byte("data: ")...)
		events = append(events, payload...)
		events = append(events, '\n')
	}
	events = append(events, []byte(`data: {"type":"response.completed","response":{"output":[]}}`)...)
	events = append(events, '\n')
	return events
}

// newDegradeProbeFixture creates a Codex credential and returns the service plus
// the credential the probe needs.
func newDegradeProbeFixture(t *testing.T) (serviceFixture, DegradeCredentialResponse, string) {
	t.Helper()
	fixture := newServiceFixture(t)
	if err := fixture.service.EnsureInitialState(t.Context()); err != nil {
		t.Fatalf("EnsureInitialState() error = %v", err)
	}
	stage := mustImportSubscriptionStage(t, fixture, "degrade-probe", "degrade-probe@example.com")
	const model = "gpt-6-luna"
	created, err := fixture.service.CreateGroup(t.Context(), GroupCreateRequest{
		Name: stringPointer("degrade probe"), ChannelID: channel.Codex,
		ConnectionType:      models.ConnectionTypeSubscription,
		Models:              optionalGroupModels{Set: true, Values: []GroupModel{{ID: model}}},
		StagedCredentialIDs: []string{stage.StageID},
	})
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	var credential models.Credential
	if err := fixture.db.Where("group_id = ?", created.GroupID).Take(&credential).Error; err != nil {
		t.Fatal(err)
	}
	fixture.service.degradeRetryWait = func(context.Context, time.Duration) error { return nil }
	return fixture, DegradeCredentialResponse{
		CredentialID: credential.ID, GroupID: created.GroupID, GroupName: "degrade probe",
		Identity: "degrade-probe@example.com", Status: "available", Weight: 50,
	}, model
}

func degradeProbeChallenge(t *testing.T) degrade.Challenge {
	t.Helper()
	challenges := degrade.Challenges(time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC))
	if len(challenges) == 0 {
		t.Fatal("degrade.Challenges() returned nothing")
	}
	return challenges[0]
}

func TestDegradeProbeRetriesRateLimitedAttempts(t *testing.T) {
	fixture, credential, model := newDegradeProbeFixture(t)
	challenge := degradeProbeChallenge(t)
	answer := degradeTestAnswerText(challenge.ExpectedCount)
	executor := &degradeScriptedExecutor{steps: []degradeScriptedStep{
		{status: http.StatusTooManyRequests, responded: true},
		{status: http.StatusBadGateway, responded: true},
		{status: http.StatusOK, responded: true, answer: answer},
	}}
	fixture.service.executor = executor

	sample, _, err := fixture.service.degradeProbe(t.Context(), credential, model, challenge)
	if err != nil {
		t.Fatalf("degradeProbe() error = %v", err)
	}
	if calls := executor.callCount(); calls != degradeProbeAttempts {
		t.Fatalf("upstream calls = %d, want %d retries", calls, degradeProbeAttempts)
	}
	if sample.Attempts != degradeProbeAttempts {
		t.Fatalf("sample.Attempts = %d, want %d", sample.Attempts, degradeProbeAttempts)
	}
	if !sample.Accepted || sample.Parsed < degrade.MinimumNumbers(challenge.ExpectedCount) {
		t.Fatalf("sample = %#v, want an accepted answer", sample)
	}
	if sample.Error != "" {
		t.Fatalf("sample.Error = %q, want empty after a successful retry", sample.Error)
	}
}

func TestDegradeProbeDoesNotRetryAShortAnswer(t *testing.T) {
	fixture, credential, model := newDegradeProbeFixture(t)
	challenge := degradeProbeChallenge(t)
	// A complete answer that is merely short is a verdict about the model, so
	// the probe must spend exactly one attempt on it.
	executor := &degradeScriptedExecutor{steps: []degradeScriptedStep{
		{status: http.StatusOK, responded: true, answer: "1, 2, 3, 4, 5"},
	}}
	fixture.service.executor = executor

	sample, _, err := fixture.service.degradeProbe(t.Context(), credential, model, challenge)
	if err != nil {
		t.Fatalf("degradeProbe() error = %v", err)
	}
	if calls := executor.callCount(); calls != 1 {
		t.Fatalf("upstream calls = %d, want a single attempt", calls)
	}
	if sample.Accepted {
		t.Fatalf("sample = %#v, want a rejected short answer", sample)
	}
	if sample.Error != "有效数字不足" {
		t.Fatalf("sample.Error = %q, want %q", sample.Error, "有效数字不足")
	}
}

func TestDegradeProbeKeepsNumbersStreamedBeforeAFailure(t *testing.T) {
	fixture, credential, model := newDegradeProbeFixture(t)
	challenge := degradeProbeChallenge(t)
	answer := degradeTestAnswerText(challenge.ExpectedCount)
	// The upstream answers in full and then the stream breaks. The numbers are
	// already on the wire and must be scored, not thrown away with the error.
	executor := &degradeScriptedExecutor{steps: []degradeScriptedStep{
		{status: http.StatusOK, responded: true, answer: answer,
			kind: execution.ErrorKindTransport, summary: "upstream stream terminated before completion"},
	}}
	fixture.service.executor = executor

	sample, _, err := fixture.service.degradeProbe(t.Context(), credential, model, challenge)
	if err != nil {
		t.Fatalf("degradeProbe() error = %v", err)
	}
	if sample.Parsed < degrade.MinimumNumbers(challenge.ExpectedCount) {
		t.Fatalf("sample = %#v, want the streamed numbers to be kept", sample)
	}
	if !sample.Accepted {
		t.Fatalf("sample = %#v, want an accepted answer recovered from a broken stream", sample)
	}
}

func TestDegradeProbeReportsNothingWhenTheUpstreamNeverAnswers(t *testing.T) {
	fixture, credential, model := newDegradeProbeFixture(t)
	challenge := degradeProbeChallenge(t)
	// Every attempt fails before a byte of the answer arrives, so the board must
	// say the upstream failed rather than blame the model's numbers.
	executor := &degradeScriptedExecutor{steps: []degradeScriptedStep{
		{kind: execution.ErrorKindTransport, summary: "upstream stream terminated before completion"},
	}}
	fixture.service.executor = executor

	sample, _, err := fixture.service.degradeProbe(t.Context(), credential, model, challenge)
	if err == nil {
		t.Fatal("degradeProbe() error = nil, want a failure")
	}
	if calls := executor.callCount(); calls != degradeProbeAttempts {
		t.Fatalf("upstream calls = %d, want %d", calls, degradeProbeAttempts)
	}
	if sample.Text != "" || sample.Parsed != 0 {
		t.Fatalf("sample = %#v, want no scored text", sample)
	}
}

func TestDegradeProbesRunChallengesInParallel(t *testing.T) {
	fixture, credential, model := newDegradeProbeFixture(t)
	challenges := degrade.Challenges(time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC))
	answer := degradeTestAnswerText(challenges[0].ExpectedCount)
	executor := &degradeScriptedExecutor{steps: []degradeScriptedStep{
		{status: http.StatusOK, responded: true, answer: answer},
	}}
	fixture.service.executor = executor

	collected, usage := fixture.service.degradeProbes(
		t.Context(), credential, model, challenges)
	if len(collected) != len(challenges) {
		t.Fatalf("samples = %d, want %d", len(collected), len(challenges))
	}
	for index, sample := range collected {
		if sample.ExpectedCount != challenges[index].ExpectedCount {
			t.Fatalf("sample %d expected count = %d, want %d; samples must keep challenge order",
				index, sample.ExpectedCount, challenges[index].ExpectedCount)
		}
	}
	if usage.input <= 0 || usage.output <= 0 {
		t.Fatalf("usage = %#v, want the attempts to be accounted", usage)
	}
	if want := int64(len(challenges) * 16); usage.input != want {
		t.Fatalf("usage.input = %d, want %d", usage.input, want)
	}
}

func TestDegradeProbeNamesAnUpstreamTimeoutAsATimeout(t *testing.T) {
	fixture, credential, model := newDegradeProbeFixture(t)
	challenge := degradeProbeChallenge(t)
	// The upstream accepted the request and then went quiet. The board has to
	// name that as an upstream timeout, not as a model that answered too little.
	executor := &degradeScriptedExecutor{steps: []degradeScriptedStep{
		{status: http.StatusOK, responded: true,
			kind: execution.ErrorKindTimeout, summary: "upstream request timed out"},
	}}
	fixture.service.executor = executor

	sample, _, err := fixture.service.degradeProbe(t.Context(), credential, model, challenge)
	if err == nil {
		t.Fatal("degradeProbe() error = nil, want a timeout")
	}
	if calls := executor.callCount(); calls != degradeProbeAttempts {
		t.Fatalf("upstream calls = %d, want %d timeouts to be retried", calls, degradeProbeAttempts)
	}
	if sample.Error != "上游响应超时" {
		t.Fatalf("sample.Error = %q, want %q", sample.Error, "上游响应超时")
	}
}

func TestDegradeProbeDoesNotRetryARejectedRequest(t *testing.T) {
	fixture, credential, model := newDegradeProbeFixture(t)
	challenge := degradeProbeChallenge(t)
	// A 400 describes the request, so repeating it would waste the budget.
	executor := &degradeScriptedExecutor{steps: []degradeScriptedStep{
		{status: http.StatusBadRequest, responded: true},
	}}
	fixture.service.executor = executor

	_, _, err := fixture.service.degradeProbe(t.Context(), credential, model, challenge)
	if err == nil {
		t.Fatal("degradeProbe() error = nil, want a rejection")
	}
	if calls := executor.callCount(); calls != 1 {
		t.Fatalf("upstream calls = %d, want a single attempt for a 400", calls)
	}
}

// TestDegradeObservedStreamReadsTerminalUsage covers the token counts the board
// shows. The subscription executor reports usage on the terminal stream event
// and leaves its result's usage empty, so a board that only read the result
// reported zero tokens for every run.
func TestDegradeObservedStreamReadsTerminalUsage(t *testing.T) {
	events := []byte(`data: {"type":"response.output_text.delta","delta":"12, 34, 56"}` + "\n" +
		`data: {"type":"response.completed","response":{"output":[],"usage":{"input_tokens":120,"input_tokens_details":{"cached_tokens":40},"output_tokens":80,"total_tokens":200}}}` + "\n")
	observed := degradeObservedStreamOf(events)
	if observed.text != "12, 34, 56" {
		t.Fatalf("text = %q, want the streamed numbers", observed.text)
	}
	if observed.usage.input != 120 {
		t.Fatalf("usage.input = %d, want 120 (uncached input plus cache reads)", observed.usage.input)
	}
	if observed.usage.output != 80 {
		t.Fatalf("usage.output = %d, want 80", observed.usage.output)
	}
}

func TestDegradeInconclusiveReasonSeparatesUpstreamFailureFromShortAnswers(t *testing.T) {
	upstream := []degrade.Sample{
		{Error: "上游响应超时"}, {Error: "上游响应超时"}, {Error: "上游响应超时"},
	}
	if reason := degradeInconclusiveReason(upstream, fmt.Errorf("未采集到有效数字序列")); reason != "上游响应超时" {
		t.Fatalf("reason = %q, want the upstream failure", reason)
	}
	short := []degrade.Sample{{Parsed: 12}, {Parsed: 30}, {Parsed: 44}}
	if reason := degradeInconclusiveReason(short, fmt.Errorf("未采集到有效数字序列")); reason != "模型回答的有效数字不足" {
		t.Fatalf("reason = %q, want the short-answer reason", reason)
	}
	silent := []degrade.Sample{{}, {}, {}}
	if reason := degradeInconclusiveReason(silent, fmt.Errorf("未采集到有效数字序列")); reason != "未采集到有效数字序列" {
		t.Fatalf("reason = %q, want the analysis error as a fallback", reason)
	}
}
