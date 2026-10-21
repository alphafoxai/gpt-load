package gateway

import (
	"net/http"
	"strconv"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/ratelimit"
	"gpt-load/internal/state"
)

func quotaBudgetReason(decision state.QuotaBudgetDecision) reason {
	message := "Credential quota budget exceeded."
	if decision.Reason == "quota_observation_unavailable" {
		message = "A fresh credential quota observation is required."
	}
	return reason{Status: http.StatusTooManyRequests, Code: decision.Reason, Message: message}
}

func localCapacityReason(code string) *reason {
	var value reason
	switch code {
	case ratelimit.CredentialConcurrencyExceeded:
		value = reason{http.StatusTooManyRequests, code, "Credential concurrency limit exceeded."}
	case ratelimit.CredentialRateExceeded:
		value = reason{http.StatusTooManyRequests, code, "Credential request rate limit exceeded."}
	case "quota_budget_exceeded", "quota_observation_unavailable":
		value = quotaBudgetReason(state.QuotaBudgetDecision{Reason: code})
	default:
		return nil
	}
	return &value
}

func setCapacityRetryAfter(header http.Header, retry time.Duration) {
	if retry > 0 {
		header.Set("Retry-After", strconv.Itoa(retryAfterSeconds(retry)))
	}
}

// acquireCredentialCapacity reads current policy after preparation and never selects a replacement.
func (h *Handler) acquireCredentialCapacity(ref state.CredentialRef) (lease ratelimit.CredentialLease, failure *reason, retry time.Duration) {
	failure = &reasonConfigurationChanged
	h.manager.WithCurrentSnapshotRead(func(snapshot *state.ConfigSnapshot) bool {
		if snapshot == nil {
			return false
		}
		group, exists := snapshot.Groups[ref.GroupID]
		if !exists {
			return false
		}
		if _, active := h.registry.ActiveEncryptedCredentialDataIfMatch(ref); !active {
			return false
		}
		now := h.now()
		if group.ChannelID == channel.Claude && group.ConnectionType == "subscription" {
			decision := h.registry.CheckQuotaBudget(ref, snapshot.Settings.QuotaBudget, now)
			if !decision.Allowed {
				value := quotaBudgetReason(decision)
				failure, retry = &value, decision.RetryAfter
				return true
			}
		}
		decision := ratelimit.CredentialCapacityDecision{Allowed: true}
		// Keep existing in-flight counts even while limits are disabled, so enabling a cap is immediate.
		lease, decision = h.credentialCapacity.TryAcquire(ref.ID, snapshot.Settings.CredentialConcurrencyLimit, snapshot.Settings.CredentialRPMLimit, now)
		if !decision.Allowed {
			message := "Credential concurrency limit exceeded."
			if decision.Reason == ratelimit.CredentialRateExceeded {
				message = "Credential request rate limit exceeded."
			}
			value := reason{Status: http.StatusTooManyRequests, Code: decision.Reason, Message: message}
			failure, retry = &value, decision.RetryAfter
		} else {
			failure = nil
		}
		return true
	})
	return
}
