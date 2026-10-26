package health

import (
	"net/http"
	"testing"
	"time"

	"gpt-load/internal/execution"
)

func emptyResponseAttempt(committed bool) ExecutionAttempt {
	return ExecutionAttempt{
		DispatchState:       execution.DispatchMaybeSent,
		StatusCode:          http.StatusOK,
		DownstreamCommitted: committed,
		Evidence: &execution.ErrorEvidence{
			Kind:       execution.ErrorKindProvider,
			OriginHint: execution.ErrorOriginUpstream,
			ScopeHint:  execution.ErrorScopeRequest,
			StatusCode: http.StatusOK,
			Code:       EmptyResponseCode,
			Summary:    "Upstream completed without producing any content.",
		},
		Now: time.Date(2026, time.September, 22, 12, 0, 0, 0, time.UTC),
	}
}

// TestJudgeExecutionRetriesEmptyResponseWithoutPenalty 固定空回的处置：换下一个
// 候选重试，但不冷却、不拉黑、不计入凭据失败统计——空回通常源于请求本身，
// 把它算作凭据故障会连锁拉黑健康的凭据。
func TestJudgeExecutionRetriesEmptyResponseWithoutPenalty(t *testing.T) {
	t.Parallel()

	got := JudgeExecution(emptyResponseAttempt(false), DecisionContext{
		Method:    http.MethodPost,
		Operation: execution.OperationChatCompletion,
	})
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got.Retry != RetryNextCandidate {
		t.Fatalf("Retry = %v, want %v", got.Retry, RetryNextCandidate)
	}
	if got.Effect != EffectNone {
		t.Fatalf("Effect = %v, want %v", got.Effect, EffectNone)
	}
	if got.RuleID != "content.empty_response" {
		t.Fatalf("RuleID = %v, want content.empty_response", got.RuleID)
	}
	if !got.CooldownUntil.IsZero() {
		t.Fatalf("CooldownUntil = %v, want zero", got.CooldownUntil)
	}
}

// TestJudgeExecutionNeverRetriesCommittedEmptyResponse 保证已经下发给客户端的
// 响应不会因为空回判定而重试。
func TestJudgeExecutionNeverRetriesCommittedEmptyResponse(t *testing.T) {
	t.Parallel()

	got := JudgeExecution(emptyResponseAttempt(true), DecisionContext{
		Method:    http.MethodPost,
		Operation: execution.OperationChatCompletion,
	})
	if got.ShouldRetry() {
		t.Fatalf("Retry = %v, want no retry after commit", got.Retry)
	}
}

// TestJudgeExecutionRetriesStreamTruncatedBeforeContent 固定首个可见增量之前
// 断开的处置：客户端尚未收到任何字符，换下一个候选重试能把完整回答交给用户；与
// 空回一样不是凭据故障，因此不冷却、不拉黑。
func TestJudgeExecutionRetriesStreamTruncatedBeforeContent(t *testing.T) {
	t.Parallel()

	attempt := emptyResponseAttempt(false)
	attempt.Evidence.Code = StreamTruncatedBeforeContentCode
	attempt.Evidence.Summary = "Upstream stream ended before any visible content."

	got := JudgeExecution(attempt, DecisionContext{
		Method:    http.MethodPost,
		Operation: execution.OperationChatCompletion,
	})
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got.Retry != RetryNextCandidate {
		t.Fatalf("Retry = %v, want %v", got.Retry, RetryNextCandidate)
	}
	if got.Effect != EffectNone || !got.CooldownUntil.IsZero() {
		t.Fatalf("Effect/CooldownUntil = %v/%v, want none/zero", got.Effect, got.CooldownUntil)
	}
	if got.RuleID != "stream.truncated_before_content" {
		t.Fatalf("RuleID = %v, want stream.truncated_before_content", got.RuleID)
	}
}

// TestJudgeExecutionNeverRetriesCommittedTruncation 保证已经提交给客户端的截断
// 不再重试：内容已经写出，重试会把两段回答拼接在一起。
func TestJudgeExecutionNeverRetriesCommittedTruncation(t *testing.T) {
	t.Parallel()

	attempt := emptyResponseAttempt(true)
	attempt.Evidence.Code = StreamTruncatedBeforeContentCode

	got := JudgeExecution(attempt, DecisionContext{
		Method:    http.MethodPost,
		Operation: execution.OperationChatCompletion,
	})
	if got.ShouldRetry() {
		t.Fatalf("Retry = %v, want no retry after commit", got.Retry)
	}
}
