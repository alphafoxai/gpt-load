package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"gpt-load/internal/dialect"
	"gpt-load/internal/execution"
	"gpt-load/internal/state"
)

const (
	// 预读窗口上限。事件数是主判据：真正的空回流很短（协议前导加终态就结束），
	// 而模型一旦开始工作就会持续产生事件，此时没有理由继续压着响应头不发。
	// 时间与字节数只作兜底，防止上游发得很慢或前导异常庞大。
	emptyResponsePrereadEvents = 8
	emptyResponsePrereadBytes  = 256 << 10
	emptyResponsePrereadWindow = 5 * time.Second
	// 仅推理（thinking）阶段的独立预算。这一阶段用户还没有看到任何内容，留着响应头
	// 未提交才能让「首个正文增量之前的截断」换候选重试。窗口就是延迟上限：推理会被
	// 压住最多这么久才交给客户端。
	//
	// 45s 来自实测：opus 的推理阶段常见 5–47s，而真实的截断发生在首事件后 17.6s 与
	// 27.6s（更早的 15s 窗口把这两次都提前提交了，一次都没救回来）。事件与字节只作
	// 兜底：推理以每秒约一个 ping 推进，45s 内事件数远低于上限。
	reasoningPrereadEvents = 4096
	reasoningPrereadBytes  = 8 << 20
	reasoningPrereadWindow = 45 * time.Second
)

// emptyResponsePreread 在提交响应前压住尚未产出内容的前导事件，使空回仍处于
// 可重试状态。未启用时所有方法都表示「不要压住」，提交时机与既有行为一致。
type emptyResponsePreread struct {
	enabled         bool
	window          time.Duration
	started         time.Time
	pending         []byte
	pendingTerminal bool
	now             func() time.Time
}

// newEmptyResponsePreread 创建预读缓冲；window 为 0 时使用默认窗口。
func newEmptyResponsePreread(enabled bool, now func() time.Time, window time.Duration) *emptyResponsePreread {
	if now == nil {
		now = time.Now
	}
	if window <= 0 {
		window = emptyResponsePrereadWindow
	}
	return &emptyResponsePreread{enabled: enabled, window: window, now: now}
}

func (preread *emptyResponsePreread) active() bool {
	return preread != nil && preread.enabled
}

func (preread *emptyResponsePreread) holding() bool {
	return preread != nil && len(preread.pending) > 0
}

// hold 在窗口未耗尽时记录待提交数据并返回 true。返回 false 表示应当立即提交，
// 此时调用方需要把 flush 的内容与当前数据一起写出。terminal 表示 data 中含有
// 协议终态边界，提交时需要据此标记终态已下发。ended 表示协议终态已经到达：事件
// 数上限只用来区分「模型仍在工作」，流已结束时不再适用，完整的空回要留给空回判定。
// reasoning 表示这部数据当前只包含隐藏的推理产出：此时用更宽的独立预算，让首个
// 可见增量之前的截断仍保持未提交。
func (preread *emptyResponsePreread) hold(data []byte, events int, terminal, ended, reasoning bool) bool {
	if !preread.active() {
		return false
	}
	now := preread.now()
	if preread.started.IsZero() {
		preread.started = now
	}
	eventLimit, byteLimit, window := emptyResponsePrereadEvents, emptyResponsePrereadBytes, preread.window
	if reasoning {
		eventLimit, byteLimit, window = reasoningPrereadEvents, reasoningPrereadBytes, reasoningPrereadWindow
	}
	if (!ended && events >= eventLimit) ||
		len(preread.pending)+len(data) >= byteLimit ||
		now.Sub(preread.started) >= window {
		return false
	}
	preread.pending = append(preread.pending, data...)
	preread.pendingTerminal = preread.pendingTerminal || terminal
	return true
}

// phaseWindow 报告当前阶段的预读窗口：仅推理阶段用更宽的窗口。
func (preread *emptyResponsePreread) phaseWindow(reasoning bool) time.Duration {
	if reasoning {
		return reasoningPrereadWindow
	}
	if preread == nil || preread.window <= 0 {
		return emptyResponsePrereadWindow
	}
	return preread.window
}

// replayableStreamFailure 只识别尚未完成的上游读流故障；不能覆盖取消、内部错误
// 或明确的业务拒绝。网关 EOF 校验与适配器读流错误均可提供这份证据。
func replayableStreamFailure(ctx context.Context, downstreamErr error, evidence *execution.ErrorEvidence) bool {
	if ctx.Err() != nil || errors.Is(downstreamErr, context.Canceled) ||
		errors.Is(downstreamErr, context.DeadlineExceeded) || errors.Is(downstreamErr, errRedactionStream) {
		return false
	}
	if evidence != nil {
		if evidence.OriginHint != "" && evidence.OriginHint != execution.ErrorOriginUpstream ||
			evidence.Hint != "" || evidence.StatusCode >= 300 {
			return false
		}
		switch evidence.Kind {
		case execution.ErrorKindTransport, execution.ErrorKindTimeout, execution.ErrorKindProvider:
		default:
			return false
		}
	}
	if downstreamErr != nil {
		var failure *streamFailure
		if errors.As(downstreamErr, &failure) {
			return failure.kind == streamFailureProtocol || failure.kind == streamFailureUpstreamRead || failure.kind == streamFailureIdle
		}
		return errors.Is(downstreamErr, ErrUpstreamProtocol)
	}
	return evidence != nil
}

// flush 取出已压住的数据并清空缓冲，同时报告其中是否含有协议终态边界。
func (preread *emptyResponsePreread) flush() ([]byte, bool) {
	if !preread.holding() {
		return nil, false
	}
	pending, terminal := preread.pending, preread.pendingTerminal
	preread.pending, preread.pendingTerminal = nil, false
	return pending, terminal
}

// emptyResponseRetryEnabled 判定本次请求是否适用空回检测。豁免项都是「按约定
// 就不产出内容」或「换候选必然失败」的路径，漏掉任何一条都会把正常请求变成
// 反复重试。
func emptyResponseRetryEnabled(
	group state.GroupView,
	metadata dialect.RequestMetadata,
	body []byte,
) bool {
	if !group.EmptyResponseRetry {
		return false
	}
	switch metadata.Operation {
	case execution.OperationChatCompletion, execution.OperationResponsesCreate:
	default:
		// 嵌入、图像、rerank、token 计数等没有「模型产出内容」的语义。
		return false
	}
	if metadata.PreviousResponseID != "" {
		// 续接请求已被锁定到单个凭据，换候选没有第二个目标可选。
		return false
	}
	if metadata.Operation != execution.OperationResponsesCreate {
		return true
	}
	var options struct {
		Generate     *bool           `json:"generate"`
		Conversation json.RawMessage `json:"conversation"`
	}
	if json.Unmarshal(body, &options) != nil {
		return true
	}
	if options.Generate != nil && !*options.Generate {
		// 预热请求按约定不产出内容，判空会把每次预热都变成失败重试。
		return false
	}
	// 会话状态由上游按账号持有：同组织多 key 时换候选重试会把同一条输入再次
	// 写进会话，因此带会话的请求不参与判定。
	conversation := bytes.TrimSpace(options.Conversation)
	return len(conversation) == 0 || bytes.Equal(conversation, []byte("null"))
}
