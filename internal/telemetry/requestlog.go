package telemetry

import (
	"time"

	"gpt-load/internal/automodel"
	"gpt-load/internal/channel"
	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
	"gpt-load/internal/reasoning"
	"gpt-load/internal/requestaudit"
	"gpt-load/internal/usage"
)

type RequestStatus string

const (
	RequestStatusSuccess    RequestStatus = "success"
	RequestStatusError      RequestStatus = "error"
	RequestStatusIncomplete RequestStatus = "incomplete"
	RequestStatusCanceled   RequestStatus = "canceled"
)

type ModelConsistency string

const (
	ModelConsistencyNotApplicable ModelConsistency = "not_applicable"
	ModelConsistencyMatch         ModelConsistency = "match"
	ModelConsistencyUnknown       ModelConsistency = "unknown"
	ModelConsistencyMismatch      ModelConsistency = "mismatch"
)

type FailureCategory string

const (
	FailureCategoryOK                     FailureCategory = "ok"
	FailureCategoryRateLimited            FailureCategory = "rate_limited"
	FailureCategoryModelUnavailable       FailureCategory = "model_unavailable"
	FailureCategoryInvalidKey             FailureCategory = "invalid_key"
	FailureCategoryUpstreamHost           FailureCategory = "upstream_host_error"
	FailureCategoryClientError            FailureCategory = "client_error"
	FailureCategoryConversionUnsupported  FailureCategory = "conversion_unsupported"
	FailureCategoryDownstreamCancel       FailureCategory = "downstream_cancel"
	FailureCategoryAuthenticationRequired FailureCategory = "authentication_required"
	FailureCategoryAmbiguous              FailureCategory = "ambiguous"
)

type RetryDirective string

const (
	RetryNone              RetryDirective = "none"
	RetryRefreshCredential RetryDirective = "refresh_credential"
	RetryNextCandidate     RetryDirective = "next_candidate"
)

func (value RetryDirective) Valid() bool {
	return value == RetryNone || value == RetryRefreshCredential || value == RetryNextCandidate
}

type Effect string

const (
	EffectNone                    Effect = "none"
	EffectCooldownCredential      Effect = "cooldown_credential"
	EffectCooldownModel           Effect = "cooldown_model"
	EffectRecordCredentialFailure Effect = "record_credential_failure"
	EffectSkipGroup               Effect = "skip_group"
)

func (value Effect) Valid() bool {
	return value == EffectNone || value == EffectCooldownCredential ||
		value == EffectCooldownModel ||
		value == EffectRecordCredentialFailure || value == EffectSkipGroup
}

type Action string

const (
	ActionTerminate          Action = "terminate"
	ActionRetry              Action = "retry"
	ActionCooldownCredential Action = "cooldown_credential"
	ActionFailCredential     Action = "fail_credential"
	ActionSkipGroup          Action = "skip_group"
)

type Attempt struct {
	Sequence          int
	CompletedAt       time.Time
	GroupID           uint
	GroupName         string
	ChannelID         channel.ID
	CredentialID      uint
	Operation         execution.Operation
	RouteMode         channel.RouteMode
	UpstreamModel     string
	UpstreamRequestID string
	DispatchState     execution.DispatchState
	ResponseStarted   bool
	UpstreamProtocol  protocol.Protocol
	Reasoning         reasoning.Config
	StatusCode        int
	DurationMs        int64
	FailureCategory   FailureCategory
	FailureOrigin     execution.ErrorOrigin
	FailureScope      execution.ErrorScope
	RetryDirective    RetryDirective
	Effect            Effect
	CooldownUntil     time.Time
	RuleID            string
	Action            Action
	WillRetry         bool
	ErrorCode         string
	ErrorSummary      string
	Committed         bool
}

// PricingObservation is the frozen, dependency-neutral quote selected by the
// gateway for the attempt whose usage is attributed to the request outcome.
type PricingObservation struct {
	UpstreamModel        string
	CostState            string
	PricingCompleteness  string
	EstimatedCostNanoUSD int64
	// 缓存读取节省与写入溢价，见 pricing.CacheValue；只在已计价时有值。
	CacheReadSavingsNanoUSD  int64
	CacheWritePremiumNanoUSD int64
	ReceiptJSON              string
}

type UsageObservation struct {
	Result          usage.Result
	GroupID         uint
	ChannelID       channel.ID
	CredentialID    uint
	AttemptSequence int
	Pricing         PricingObservation
}

type RequestEvent struct {
	RequestAudit          *requestaudit.Result
	AutoDecision          *automodel.Decision
	RequestID             string
	CompletedAt           time.Time
	AccessKeyID           uint
	Protocol              protocol.Protocol
	Operation             execution.Operation
	ClientIP              string
	ClientModel           string
	UpstreamModel         string
	UpstreamReportedModel string
	ModelConsistency      ModelConsistency
	Status                RequestStatus
	StatusCode            int
	ErrorCode             string
	ErrorSummary          string
	Stream                bool
	FirstResponseMs       *int64
	DurationMs            int64
	AffinityHit           bool
	AffinityKind          string
	// AffinityOutcome 是路由时（首次尝试）对会话绑定的判定；AffinityHit 恰好在结果为命中时为 true。
	AffinityOutcome AffinityOutcome
	// AffinityKey 是亲和键摘要的前 16 位十六进制（affinity.Key.LogID），同一会话的请求取值相同；没有亲和键时为空。
	AffinityKey string
	// AffinityBoundCredentialID 是请求到达时会话绑定的账号，仅在结果为命中或未命中时非零。
	AffinityBoundCredentialID uint
	// AffinityMissReason 说明绑定账号为什么没有接到首次尝试，仅在 miss_unavailable 时非空。
	AffinityMissReason AffinityMissReason
	Reasoning          reasoning.Config
	Attempts           []Attempt
	Usage              UsageObservation
	// RequestBody 只在访问密钥开启请求内容记录时携带，是网关收到并解码后的客户端原始请求体。
	RequestBody []byte
}

type RequestLogSink interface {
	Emit(RequestEvent)
}

type NoopRequestLogSink struct{}

func (NoopRequestLogSink) Emit(RequestEvent) {}

// 亲和类型仅描述实际选中账号的依据，不代表上游缓存命中。
const (
	AffinityPromptPrefix       = "prompt_prefix"
	AffinityPromptCacheKey     = "prompt_cache_key"
	AffinityResponseContinuity = "response_continuity"
	// AffinityClaudeCodeSession：按 Anthropic metadata.user_id 里的 Claude Code 会话 ID 与模型绑定。
	AffinityClaudeCodeSession = "claude_code_session"
)

// AffinityMissReason 是 miss_unavailable 时绑定账号不可用的原因，取首次尝试那一刻账号的状态。
type AffinityMissReason string

const (
	AffinityMissReasonNone AffinityMissReason = ""
	// AffinityMissReasonDeleted：绑定账号已不在注册表里。
	AffinityMissReasonDeleted AffinityMissReason = "deleted"
	// AffinityMissReasonDisabled：绑定账号被停用。
	AffinityMissReasonDisabled AffinityMissReason = "disabled"
	// AffinityMissReasonAuthPending：绑定账号的凭据未就绪（等待刷新或重新授权）。
	AffinityMissReasonAuthPending AffinityMissReason = "auth_pending"
	// AffinityMissReasonBlacklisted：绑定账号被拉黑。
	AffinityMissReasonBlacklisted AffinityMissReason = "blacklisted"
	// AffinityMissReasonCooldown：绑定账号处于错误冷却（限流、上游错误）。
	AffinityMissReasonCooldown AffinityMissReason = "cooldown"
	// AffinityMissReasonQuotaCooldown：绑定账号额度用尽，冷却到额度恢复。
	AffinityMissReasonQuotaCooldown AffinityMissReason = "quota_cooldown"
	// AffinityMissReasonModelCooldown：绑定账号有模型级冷却（含额度类）。
	AffinityMissReasonModelCooldown AffinityMissReason = "model_cooldown"
	// AffinityMissReasonDeprioritized：绑定账号可用但没有被选中（分组优先级、路由方式或权重为 0）。
	AffinityMissReasonDeprioritized AffinityMissReason = "deprioritized"
)

// AffinityMissReasons 列出所有可记录的未命中原因。
var AffinityMissReasons = []AffinityMissReason{
	AffinityMissReasonDeleted,
	AffinityMissReasonDisabled,
	AffinityMissReasonAuthPending,
	AffinityMissReasonBlacklisted,
	AffinityMissReasonCooldown,
	AffinityMissReasonQuotaCooldown,
	AffinityMissReasonModelCooldown,
	AffinityMissReasonDeprioritized,
}

// Valid 接受已定义的原因；空值表示没有原因（非 miss_unavailable，或记录该字段之前的日志）。
func (reason AffinityMissReason) Valid() bool {
	for _, candidate := range AffinityMissReasons {
		if reason == candidate {
			return true
		}
	}
	return false
}

// AffinityOutcome 描述请求到达时会话绑定的状态，以及首次尝试是否落在绑定的账号上。
type AffinityOutcome string

const (
	// AffinityOutcomeUnrecorded 只出现在记录该字段之前的日志里。
	AffinityOutcomeUnrecorded AffinityOutcome = ""
	// AffinityOutcomeNotApplicable：选中分组未开启亲和，或请求没有可用的亲和键。
	AffinityOutcomeNotApplicable AffinityOutcome = "not_applicable"
	// AffinityOutcomeNew：没有存活的绑定（新会话，或绑定已被淘汰）。
	AffinityOutcomeNew AffinityOutcome = "new"
	// AffinityOutcomeExpired：曾有绑定，但会话空闲超过亲和有效期。
	AffinityOutcomeExpired AffinityOutcome = "expired"
	// AffinityOutcomeHit：首次尝试落在绑定的账号上（提示前缀或 prompt_cache_key）。
	AffinityOutcomeHit AffinityOutcome = "hit"
	// AffinityOutcomeContinuation：previous_response_id 续接，账号由响应绑定决定。
	AffinityOutcomeContinuation AffinityOutcome = "continuation"
	// AffinityOutcomeMissUnavailable：绑定账号在候选中，但首次尝试时不可用（冷却、限流、并发、额度）。
	AffinityOutcomeMissUnavailable AffinityOutcome = "miss_unavailable"
	// AffinityOutcomeMissExcluded：绑定账号不在本次请求的候选里（分组被删除或关闭亲和，或不在访问密钥/模型的候选范围）。
	AffinityOutcomeMissExcluded AffinityOutcome = "miss_excluded"
	// AffinityOutcomeMissReauthorized：绑定账号已重新授权为另一个身份。
	AffinityOutcomeMissReauthorized AffinityOutcome = "miss_reauthorized"
)

// AffinityOutcomes 按展示顺序列出所有已记录的结果。
var AffinityOutcomes = []AffinityOutcome{
	AffinityOutcomeHit,
	AffinityOutcomeContinuation,
	AffinityOutcomeMissUnavailable,
	AffinityOutcomeMissExcluded,
	AffinityOutcomeMissReauthorized,
	AffinityOutcomeExpired,
	AffinityOutcomeNew,
	AffinityOutcomeNotApplicable,
}

// Valid 接受已记录的结果；空值（未记录）只用于历史数据，不是有效的新结果。
func (outcome AffinityOutcome) Valid() bool {
	for _, candidate := range AffinityOutcomes {
		if outcome == candidate {
			return true
		}
	}
	return false
}

// Pinned 表示请求到达时会话已绑定账号。
func (outcome AffinityOutcome) Pinned() bool {
	return outcome.Hit() || outcome.Miss()
}

// Hit 表示首次尝试落在绑定的账号上。
func (outcome AffinityOutcome) Hit() bool {
	return outcome == AffinityOutcomeHit || outcome == AffinityOutcomeContinuation
}

// Miss 表示会话已绑定，但首次尝试没有落在绑定的账号上。
func (outcome AffinityOutcome) Miss() bool {
	switch outcome {
	case AffinityOutcomeMissUnavailable, AffinityOutcomeMissExcluded, AffinityOutcomeMissReauthorized:
		return true
	default:
		return false
	}
}
