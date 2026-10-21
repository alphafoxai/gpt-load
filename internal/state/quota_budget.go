package state

import (
	"fmt"
	"math"
	"time"

	providerobservation "gpt-load/internal/subscription/providers/observation"
)

type QuotaBudgetConfig struct {
	Enabled                  bool  `json:"enabled"`
	FiveHourPercent          int   `json:"five_hour_percent"`
	SevenDayPercent          int   `json:"seven_day_percent"`
	ObservationMaxAgeSeconds int64 `json:"observation_max_age_seconds"`
}

func DefaultQuotaBudgetConfig() QuotaBudgetConfig {
	return QuotaBudgetConfig{FiveHourPercent: 50, SevenDayPercent: 80, ObservationMaxAgeSeconds: 300}
}

func parseQuotaBudgetConfig(value any) (QuotaBudgetConfig, error) {
	object, ok := value.(map[string]any)
	if !ok || len(object) != 4 {
		return QuotaBudgetConfig{}, fmt.Errorf("quota_budget must contain enabled, five_hour_percent, seven_day_percent and observation_max_age_seconds")
	}
	for key := range object {
		switch key {
		case "enabled", "five_hour_percent", "seven_day_percent", "observation_max_age_seconds":
		default:
			return QuotaBudgetConfig{}, fmt.Errorf("unknown quota_budget field %q", key)
		}
	}
	enabled, err := strictBoolean("quota_budget.enabled", object["enabled"])
	if err != nil {
		return QuotaBudgetConfig{}, err
	}
	five, err := wholeNumberInRange("quota_budget.five_hour_percent", object["five_hour_percent"], 1, 100)
	if err != nil {
		return QuotaBudgetConfig{}, err
	}
	seven, err := wholeNumberInRange("quota_budget.seven_day_percent", object["seven_day_percent"], 1, 100)
	if err != nil {
		return QuotaBudgetConfig{}, err
	}
	age, err := wholeNumberInRange("quota_budget.observation_max_age_seconds", object["observation_max_age_seconds"], 1, 86400)
	if err != nil {
		return QuotaBudgetConfig{}, err
	}
	return QuotaBudgetConfig{Enabled: enabled, FiveHourPercent: five, SevenDayPercent: seven, ObservationMaxAgeSeconds: int64(age)}, nil
}

type QuotaBudgetDecision struct {
	Allowed    bool
	Reason     string
	RetryAfter time.Duration
}

type quotaBudgetSample struct {
	observedAtMS int64
	resetAtMS    int64
	utilization  float64
	known        bool
}

// ApplyQuotaBudgetWindows accepts only timestamped evidence for the current account identity.
func (r *CredentialRegistry) ApplyQuotaBudgetWindows(ref CredentialRef, windows []providerobservation.QuotaWindow) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entryLocked(ref.ID)
	if !ok || !quotaBudgetIdentityMatches(entry, ref) || entry.Status != CredentialStatusActive {
		return false
	}
	for _, window := range windows {
		position := quotaBudgetWindowPosition(window)
		if position < 0 || window.ObservedAtMS == nil || *window.ObservedAtMS <= 0 {
			continue
		}
		previous := entry.quotaBudgetSamples[position]
		if *window.ObservedAtMS <= previous.observedAtMS {
			continue
		}
		sample := quotaBudgetSample{observedAtMS: *window.ObservedAtMS}
		if window.ResetAtMS != nil {
			sample.resetAtMS = *window.ResetAtMS
		}
		utilization, known := quotaBudgetUtilization(window)
		sample.known = known
		sample.utilization = utilization
		entry.quotaBudgetSamples[position] = sample
	}
	return true
}

func quotaBudgetWindowPosition(window providerobservation.QuotaWindow) int {
	if window.Scope != "account" && window.Scope != "" {
		return -1
	}
	switch window.ID {
	case "five_hour":
		return 0
	case "seven_day":
		return 1
	}
	return -1
}

func quotaBudgetIdentityMatches(entry *CredentialEntry, ref CredentialRef) bool {
	return ref.ID != 0 && ref.GroupID != 0 && ref.Version != 0 && ref.IdentityGeneration != 0 && ref.Fingerprint != "" &&
		entry.ID == ref.ID && entry.GroupID == ref.GroupID && entry.Version == ref.Version &&
		entry.IdentityGeneration == ref.IdentityGeneration && entry.Fingerprint == ref.Fingerprint
}

func (r *CredentialRegistry) CheckQuotaBudget(ref CredentialRef, config QuotaBudgetConfig, now time.Time) QuotaBudgetDecision {
	if !config.Enabled {
		return QuotaBudgetDecision{Allowed: true}
	}
	unavailable := QuotaBudgetDecision{Reason: "quota_observation_unavailable"}
	if r == nil || now.IsZero() || config.FiveHourPercent < 1 || config.FiveHourPercent > 100 || config.SevenDayPercent < 1 || config.SevenDayPercent > 100 || config.ObservationMaxAgeSeconds < 1 || config.ObservationMaxAgeSeconds > 86400 {
		return unavailable
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entryLocked(ref.ID)
	if !ok || !quotaBudgetIdentityMatches(entry, ref) || entry.Status != CredentialStatusActive || entry.AuthState.normalize() != CredentialAuthStateReady || entry.Blacklisted || effectiveCooldownUntil(entry).After(now) {
		return unavailable
	}
	thresholds := [2]int{config.FiveHourPercent, config.SevenDayPercent}
	decision := QuotaBudgetDecision{Allowed: true}
	for index, sample := range entry.quotaBudgetSamples {
		observed := time.UnixMilli(sample.observedAtMS)
		reset := time.UnixMilli(sample.resetAtMS)
		seconds := [2]int64{5 * 60 * 60, 7 * 24 * 60 * 60}[index]
		if !sample.known || sample.observedAtMS <= 0 || observed.After(now) || now.Sub(observed) >= time.Duration(config.ObservationMaxAgeSeconds)*time.Second || !reset.After(now) || observed.Before(reset.Add(-time.Duration(seconds)*time.Second)) {
			return unavailable
		}
		if sample.utilization >= float64(thresholds[index])/100 {
			decision.Allowed = false
			decision.Reason = "quota_budget_exceeded"
			decision.RetryAfter = max(decision.RetryAfter, reset.Sub(now))
		}
	}
	return decision
}

func quotaBudgetUtilization(window providerobservation.QuotaWindow) (float64, bool) {
	if window.State == "exhausted" {
		return 1, true
	}
	if window.Utilization != nil {
		value := *window.Utilization
		return value, !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
	}
	if window.Remaining != nil && window.Limit != nil && *window.Limit > 0 {
		value := (*window.Limit - *window.Remaining) / *window.Limit
		return value, !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
	}
	return 0, false
}

func (r *CredentialRegistry) QuotaBudgetRefreshDue(ref CredentialRef, config QuotaBudgetConfig, now time.Time) bool {
	if r == nil || !config.Enabled || config.ObservationMaxAgeSeconds < 1 {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entryLocked(ref.ID)
	if !ok || !quotaBudgetIdentityMatches(entry, ref) || entry.Status != CredentialStatusActive || entry.AuthState.normalize() != CredentialAuthStateReady {
		return false
	}
	for _, sample := range entry.quotaBudgetSamples {
		if !sample.known || sample.observedAtMS <= 0 || !time.UnixMilli(sample.resetAtMS).After(now) || now.Sub(time.UnixMilli(sample.observedAtMS)) >= time.Duration(config.ObservationMaxAgeSeconds)*time.Second/2 {
			return true
		}
	}
	return false
}

func (r *CredentialRegistry) InvalidateQuotaBudget(credentialID uint, at time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry, ok := r.entryLocked(credentialID); ok {
		for index, sample := range entry.quotaBudgetSamples {
			if sample.observedAtMS <= at.UnixMilli() {
				entry.quotaBudgetSamples[index] = quotaBudgetSample{observedAtMS: at.UnixMilli()}
			}
		}
	}
}
