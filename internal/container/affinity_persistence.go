package container

import (
	"context"
	"errors"

	"gpt-load/internal/affinity"
	"gpt-load/internal/app"
	"gpt-load/internal/state"
)

// affinityPersistence binds the gateway's affinity cache to its store and to
// the live config snapshot, whose policy governs what is restored.
type affinityPersistence struct {
	persister *affinity.Persister
	manager   *state.Manager
}

func newAffinityPersistence(cache *affinity.Cache, store affinity.Store, manager *state.Manager) app.AffinityPersistence {
	return affinityPersistence{persister: affinity.NewPersister(cache, store), manager: manager}
}

func (persistence affinityPersistence) Restore(ctx context.Context) (int, error) {
	snapshot := persistence.manager.Current()
	if snapshot == nil {
		return 0, errors.New("config snapshot is not loaded")
	}
	return persistence.persister.Restore(
		ctx, snapshot.Revision, snapshot.Settings.AffinityCapacity, snapshot.Settings.AffinityTTL,
	)
}

func (persistence affinityPersistence) Start() {
	persistence.persister.Start()
}

func (persistence affinityPersistence) Stop(ctx context.Context) error {
	return persistence.persister.Stop(ctx)
}
