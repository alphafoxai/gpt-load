package gateway

import (
	"slices"

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
	continuityKey         string
	kind                  string
	// outcome 是解析阶段的判定；有可用绑定时暂记为 miss_unavailable，首次尝试选中绑定账号后改为 hit。
	outcome telemetry.AffinityOutcome
}

// settle 给出首次尝试后的最终结果与命中来源。selectedAffinityEnabled 为选中分组是否参与亲和：
// 未参与的分组不会学习绑定，没有绑定的请求在那里记为不适用，而不是新会话。
func (request requestAffinity) settle(selectedCredentialID uint, selectedAffinityEnabled bool) (telemetry.AffinityOutcome, string) {
	if request.outcome == telemetry.AffinityOutcomeContinuation {
		return request.outcome, request.kind
	}
	if request.preferredCredentialID != 0 {
		if selectedCredentialID == request.preferredCredentialID {
			return telemetry.AffinityOutcomeHit, request.kind
		}
		return telemetry.AffinityOutcomeMissUnavailable, ""
	}
	switch request.outcome {
	case telemetry.AffinityOutcomeNew, telemetry.AffinityOutcomeExpired:
		if !selectedAffinityEnabled {
			return telemetry.AffinityOutcomeNotApplicable, ""
		}
	case telemetry.AffinityOutcomeUnrecorded:
		return telemetry.AffinityOutcomeNotApplicable, ""
	}
	return request.outcome, ""
}

func (handler *Handler) resolveRequestAffinity(
	snapshot *state.ConfigSnapshot,
	accessKeyID uint,
	clientProtocol protocol.Protocol,
	prefix []byte,
	allowedCredentialRefs map[uint]state.CredentialRef,
	candidateGroupIDs []uint,
	promptCacheKey string,
) requestAffinity {
	if handler == nil || snapshot == nil {
		return requestAffinity{outcome: telemetry.AffinityOutcomeNotApplicable}
	}
	key := affinity.DeriveKey(
		handler.encryption,
		accessKeyID,
		clientProtocol,
		prefix,
	)
	// 执行层私有 replay scope 仍由提示词派生，不把客户端缓存分组当作会话身份。
	result := requestAffinity{
		continuityKey: string(key), kind: telemetry.AffinityPromptPrefix,
		outcome: telemetry.AffinityOutcomeNotApplicable,
	}
	if promptCacheKey != "" {
		key = affinity.DerivePromptCacheKey(handler.encryption, accessKeyID, clientProtocol, promptCacheKey)
		result.kind = telemetry.AffinityPromptCacheKey
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
		}
		return resolved
	}
	if ref.IdentityGeneration != target.IdentityGeneration {
		resolved.outcome = telemetry.AffinityOutcomeMissReauthorized
		return resolved
	}
	resolved.preferredCredentialID = target.CredentialID
	resolved.outcome = telemetry.AffinityOutcomeMissUnavailable
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
