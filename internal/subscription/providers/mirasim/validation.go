package mirasim

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// credentialValidationTimeout bounds the signed relay call that decides whether
// a freshly imported credential is usable.
const credentialValidationTimeout = 60 * time.Second

// ValidateRemote confirms the credential against the relay with a signed
// control call. Importing a credential that the relay would refuse leaves an
// account that looks healthy and fails every request, so the login flow treats
// this answer as the gate that decides whether the credential is persisted.
func (c *Client) ValidateRemote(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("Mirasim client is unavailable")
	}
	validationCtx, cancel := context.WithTimeout(ctx, credentialValidationTimeout)
	defer cancel()
	if _, err := c.ListModels(validationCtx); err != nil {
		var upstream *StatusError
		if errors.As(err, &upstream) && upstream.StatusCode() > 0 {
			return fmt.Errorf("validate Mirasim credentials: upstream returned HTTP %d", upstream.StatusCode())
		}
		return fmt.Errorf("validate Mirasim credentials: %w", err)
	}
	return nil
}
