package gateway

import (
	"slices"
	"time"

	"gpt-load/internal/affinity"
	"gpt-load/internal/protocol"
	"gpt-load/internal/scheduler"
	"gpt-load/internal/state"
	"gpt-load/internal/telemetry"
)

type requestAffinity struct {
	key                   affinity.Key
	observation           affinity.Observation
	preferredCredentialID uint
	// boundCredentialID 是请求到达时会话绑定的账号；绑定已失效（分组移除、重新授权）时也保留，用于解释未命中。
	boundCredentialID uint
	continuityKey     string
	kind              string
	// outcome 是解析阶段的判定；有可用绑定时暂记为 miss_unavailable，首次尝试选中绑定账号后改为 hit。
	outcome telemetry.AffinityOutcome
	// missReason 是解析时绑定账号的不可用原因；请求没走到首次尝试时，日志保留这个值。
	missReason telemetry.AffinityMissReason
}

// affinitySettlement 是首次尝试后写进请求日志的亲和结论。
type affinitySettlement struct {
	outcome    telemetry.AffinityOutcome
	kind       string
	missReason telemetry.AffinityMissReason
}

// settle 给出首次尝试后的最终结果与命中来源。selectedAffinityEnabled 为选中分组是否参与亲和：
// 未参与的分组不会学习绑定，没有绑定的请求在那里记为不适用，而不是新会话。
// missReason 读取绑定账号此刻的状态；账号状态正常却没被选中，记为被调度降级。
func (request requestAffinity) settle(
	selectedCredentialID uint,
	selectedAffinityEnabled bool,
	missReason func(uint) telemetry.AffinityMissReason,
) affinitySettlement {
	if request.outcome == telemetry.AffinityOutcomeContinuation {
		return affinitySettlement{outcome: request.outcome, kind: request.kind}
	}
	if request.preferredCredentialID != 0 {
		if selectedCredentialID == request.preferredCredentialID {
			return affinitySettlement{outcome: telemetry.AffinityOutcomeHit, kind: request.kind}
		}
		reason := telemetry.AffinityMissReasonDeprioritized
		if missReason != nil {
			if current := missReason(request.preferredCredentialID); current != telemetry.AffinityMissReasonNone {
				reason = current
			}
		}
		return affinitySettlement{outcome: telemetry.AffinityOutcomeMissUnavailable, missReason: reason}
	}
	switch request.outcome {
	case telemetry.AffinityOutcomeNew, telemetry.AffinityOutcomeExpired:
		if !selectedAffinityEnabled {
			return affinitySettlement{outcome: telemetry.AffinityOutcomeNotApplicable}
		}
	case telemetry.AffinityOutcomeUnrecorded:
		return affinitySettlement{outcome: telemetry.AffinityOutcomeNotApplicable}
	}
	return affinitySettlement{outcome: request.outcome, missReason: request.missReason}
}

// logKey 是写进请求日志的会话标识；只有参与了亲和查找的请求才有。
func (request requestAffinity) logKey() string {
	return request.key.LogID()
}

// affinityMissReason 按账号此刻的状态解释它为什么不能接请求；账号可调度时返回空。
func (handler *Handler) affinityMissReason(credentialID uint) telemetry.AffinityMissReason {
	if handler == nil || handler.now == nil {
		return telemetry.AffinityMissReasonNone
	}
	source, ok := handler.registry.(interface {
		CredentialAvailability(uint, time.Time) state.CredentialAvailability
	})
	if !ok {
		return telemetry.AffinityMissReasonNone
	}
	availability := source.CredentialAvailability(credentialID, handler.now())
	switch {
	case !availability.Exists:
		return telemetry.AffinityMissReasonDeleted
	case availability.Disabled:
		return telemetry.AffinityMissReasonDisabled
	case availability.AuthPending:
		return telemetry.AffinityMissReasonAuthPending
	case availability.Blacklisted:
		return telemetry.AffinityMissReasonBlacklisted
	case availability.QuotaCooldown:
		return telemetry.AffinityMissReasonQuotaCooldown
	case availability.Cooldown:
		return telemetry.AffinityMissReasonCooldown
	case availability.ModelCooldown:
		return telemetry.AffinityMissReasonModelCooldown
	}
	return telemetry.AffinityMissReasonNone
}

// affinitySignals 是请求里可以当作会话身份的信号，按优先级：prompt_cache_key、Claude Code 会话、提示词前缀。
type affinitySignals struct {
	prefix              []byte
	promptCacheKey      string
	claudeCodeSessionID string
	model               string
}

func (handler *Handler) resolveRequestAffinity(
	snapshot *state.ConfigSnapshot,
	accessKeyID uint,
	clientProtocol protocol.Protocol,
	signals affinitySignals,
	allowedCredentialRefs map[uint]state.CredentialRef,
	candidateGroupIDs []uint,
) requestAffinity {
	if handler == nil || snapshot == nil {
		return requestAffinity{outcome: telemetry.AffinityOutcomeNotApplicable}
	}
	key := affinity.DeriveKey(
		handler.encryption,
		accessKeyID,
		clientProtocol,
		signals.prefix,
	)
	// 执行层私有 replay scope 仍由提示词派生，不把客户端缓存分组或会话 ID 当作 replay 身份。
	result := requestAffinity{
		continuityKey: string(key), kind: telemetry.AffinityPromptPrefix,
		outcome: telemetry.AffinityOutcomeNotApplicable,
	}
	if signals.promptCacheKey != "" {
		key = affinity.DerivePromptCacheKey(handler.encryption, accessKeyID, clientProtocol, signals.promptCacheKey)
		result.kind = telemetry.AffinityPromptCacheKey
	} else if sessionKey := affinity.DeriveClaudeCodeSessionKey(
		handler.encryption, accessKeyID, clientProtocol, signals.model, signals.claudeCodeSessionID,
	); sessionKey.Valid() {
		key = sessionKey
		result.kind = telemetry.AffinityClaudeCodeSession
	}
	if handler.affinityCache == nil ||
		!handler.affinityCache.Configure(
			snapshot.Revision,
			snapshot.Settings.AffinityCapacity,
			snapshot.Settings.AffinityTTL,
		) {
		return result
	}
	if !key.Valid() {
		return result
	}
	result.key = key
	observation := handler.affinityCache.Lookup(key)
	resolved := result
	resolved.observation = observation
	if !observation.Found() {
		resolved.outcome = telemetry.AffinityOutcomeNew
		if observation.Expired() {
			resolved.outcome = telemetry.AffinityOutcomeExpired
		}
		return resolved
	}
	target := observation.Target
	resolved.boundCredentialID = target.CredentialID
	resolved.outcome = telemetry.AffinityOutcomeMissExcluded
	group, exists := snapshot.Groups[target.GroupID]
	if !exists || !group.AffinityEnabled {
		return resolved
	}
	ref, allowed := allowedCredentialRefs[target.CredentialID]
	if !allowed || ref.GroupID != target.GroupID {
		// 分组仍服务本次请求，但账号不在可用列表里：账号被停用、删除或等待重新授权，记为不可用。
		if !allowed && slices.Contains(candidateGroupIDs, target.GroupID) {
			resolved.outcome = telemetry.AffinityOutcomeMissUnavailable
			resolved.missReason = handler.affinityMissReason(target.CredentialID)
		}
		return resolved
	}
	if ref.IdentityGeneration != target.IdentityGeneration {
		resolved.outcome = telemetry.AffinityOutcomeMissReauthorized
		return resolved
	}
	resolved.preferredCredentialID = target.CredentialID
	resolved.outcome = telemetry.AffinityOutcomeMissUnavailable
	resolved.missReason = handler.affinityMissReason(target.CredentialID)
	return resolved
}

func (handler *Handler) recordAffinitySuccess(
	request requestAffinity,
	selection scheduler.Selection,
	ref state.CredentialRef,
) {
	if handler == nil || handler.affinityCache == nil || !request.key.Valid() ||
		!selection.Group.AffinityEnabled {
		return
	}
	handler.affinityCache.RecordSuccess(
		request.key,
		request.observation,
		affinity.Target{
			GroupID: selection.GroupID, CredentialID: selection.CredentialID,
			IdentityGeneration: ref.IdentityGeneration,
		},
	)
}

// AffinityCache exposes the soft-affinity cache so the process can attach
// write-behind persistence before the handler serves requests.
func (handler *Handler) AffinityCache() *affinity.Cache {
	if handler == nil {
		return nil
	}
	return handler.affinityCache
}
