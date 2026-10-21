package state

import "time"

// CredentialAvailability 是一个凭据在某一时刻能否被调度的事实快照，用来解释路由为什么没有选中它。
// 各项互不排斥，调用方自行决定优先级。
type CredentialAvailability struct {
	Exists        bool
	Disabled      bool
	Suspended     bool
	AuthPending   bool
	Blacklisted   bool
	Cooldown      bool
	QuotaCooldown bool
	ModelCooldown bool
}

// CredentialAvailability 读取凭据当前的停用、授权、拉黑和各级冷却状态；凭据不存在时 Exists 为 false。
func (r *CredentialRegistry) CredentialAvailability(credentialID uint, now time.Time) CredentialAvailability {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entryLocked(credentialID)
	if !ok {
		return CredentialAvailability{}
	}
	availability := CredentialAvailability{
		Exists:        true,
		Disabled:      entry.Status != CredentialStatusActive,
		Suspended:     entry.Status == CredentialStatusSuspended,
		AuthPending:   entry.AuthState.normalize() != CredentialAuthStateReady,
		Blacklisted:   entry.Blacklisted,
		Cooldown:      entry.CooldownUntil.After(now),
		QuotaCooldown: entry.QuotaCooldownUntil.After(now),
	}
	for _, until := range entry.ModelCooldowns {
		if until.After(now) {
			availability.ModelCooldown = true
			return availability
		}
	}
	for _, until := range entry.QuotaModelCooldowns {
		if until.After(now) {
			availability.ModelCooldown = true
			return availability
		}
	}
	return availability
}
