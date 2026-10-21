package control

import (
	"context"
	"sort"
	"time"

	"github.com/sirupsen/logrus"

	"gpt-load/internal/channel"
)

const quotaBudgetRefreshInterval = 15 * time.Second
const quotaBudgetRefreshBatchSize = 8

// RunQuotaBudgetRefresh shares the control runtime's cancellation and provider observation path.
func (s *Service) RunQuotaBudgetRefresh(ctx context.Context) {
	if s == nil || s.registry == nil || s.manager == nil {
		return
	}
	ticker := time.NewTicker(quotaBudgetRefreshInterval)
	defer ticker.Stop()
	var cursor uint
	hydrated := false
	for {
		if !hydrated {
			if err := s.restoreCredentialQuotaObservations(ctx); err == nil {
				hydrated = true
			} else {
				logrus.WithField("event", "control.quota_budget_hydration_failed").Warn("quota budget observation hydration failed")
			}
		}
		cursor = s.refreshQuotaBudgetBatch(ctx, cursor)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) quotaBudgetRefreshEligible(groupID, credentialID uint) bool {
	if s == nil || s.manager == nil || s.registry == nil {
		return false
	}
	snapshot := s.manager.Current()
	if snapshot == nil || !snapshot.Settings.QuotaBudget.Enabled {
		return false
	}
	group, ok := snapshot.Groups[groupID]
	if !ok || group.ChannelID != channel.Claude || group.ConnectionType != "subscription" {
		return false
	}
	_, ready := s.registry.ActiveEncryptedCredentialData(credentialID, groupID)
	return ready
}

func (s *Service) refreshQuotaBudgetBatch(ctx context.Context, cursor uint) uint {
	snapshot := s.manager.Current()
	if snapshot == nil || !snapshot.Settings.QuotaBudget.Enabled || ctx.Err() != nil {
		return cursor
	}
	groups := make([]uint, 0)
	for id, group := range snapshot.Groups {
		if group.ChannelID == channel.Claude && group.ConnectionType == "subscription" {
			groups = append(groups, id)
		}
	}
	refs := s.registry.CaptureActiveCredentialRefs(groups)
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	if len(refs) == 0 {
		return 0
	}
	start := 0
	for index, ref := range refs {
		if ref.ID > cursor {
			start = index
			break
		}
	}
	attempts := 0
	for offset := 0; offset < len(refs) && attempts < quotaBudgetRefreshBatchSize; offset++ {
		ref := refs[(start+offset)%len(refs)]
		cursor = ref.ID
		if _, currentIdentity := s.registry.ActiveEncryptedCredentialDataIfMatch(ref); !currentIdentity {
			continue
		}
		if ctx.Err() != nil || !s.quotaBudgetRefreshEligible(ref.GroupID, ref.ID) {
			continue
		}
		current := s.manager.Current()
		if !s.registry.QuotaBudgetRefreshDue(ref, current.Settings.QuotaBudget, s.now()) {
			continue
		}
		attempts++
		_, _ = s.refreshCredentialObservation(ctx, ref.GroupID, ref.ID, observationRefreshBudget)
	}
	return cursor
}
