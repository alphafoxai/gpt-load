package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"gpt-load/internal/channel"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/storage/models"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

func (s *Service) SendCredentialEmailCode(ctx context.Context, stageID, email string) error {
	email = strings.TrimSpace(email)
	if s == nil || s.sendSubscriptionEmailCode == nil || strings.TrimSpace(stageID) == "" || email == "" {
		return app_errors.ErrValidation
	}
	reserved, err := s.reserveEmailSend(ctx, stageID, email)
	if err != nil {
		return err
	}
	if err := s.sendSubscriptionEmailCode(ctx, reserved.channelID, reserved.driverState, email); err != nil {
		return emailLoginError(err)
	}
	return s.markEmailMailed(ctx, stageID, email)
}

func (s *Service) VerifyCredentialEmailCode(ctx context.Context, stageID, email, code string) (CredentialStageResult, error) {
	email = strings.TrimSpace(email)
	code = strings.TrimSpace(code)
	if s == nil || s.exchangeSubscriptionEmailCode == nil || strings.TrimSpace(stageID) == "" || email == "" || code == "" {
		return CredentialStageResult{}, app_errors.ErrValidation
	}
	reserved, err := s.reserveEmailVerify(ctx, stageID, email)
	if err != nil {
		return CredentialStageResult{}, err
	}
	accessToken, refreshToken, err := s.exchangeSubscriptionEmailCode(ctx, reserved.channelID, reserved.driverState, email, code)
	if err != nil {
		return CredentialStageResult{}, emailLoginError(err)
	}
	return s.completeCredentialAuthorization(ctx, stageID, "", reserved.state, "", accessToken, refreshToken)
}

type reservedEmailLogin struct {
	channelID   channel.ID
	driverState []byte
	state       string
}

func (s *Service) reserveEmailSend(ctx context.Context, stageID, email string) (reservedEmailLogin, error) {
	return s.updatePendingEmailLogin(ctx, stageID, func(now time.Time, payload *stagedSubscriptionPayload) error {
		state := payload.EmailLogin
		if state == nil {
			state = &stagedEmailLogin{}
		}
		if state.Mailed && state.Email != "" && state.Email != email {
			return app_errors.ErrValidation
		}
		if state.Sends >= maxEmailCodeSends || state.Attempts >= maxEmailVerifyAttempts {
			return app_errors.ErrEmailLoginLimited
		}
		if state.SentAtMS > 0 && now.Sub(time.UnixMilli(state.SentAtMS)) < emailCodeSendInterval {
			return app_errors.ErrEmailLoginLimited
		}
		state.Sends++
		state.SentAtMS = now.UnixMilli()
		payload.EmailLogin = state
		return nil
	}, nil)
}

func (s *Service) markEmailMailed(ctx context.Context, stageID, email string) error {
	_, err := s.updatePendingEmailLogin(ctx, stageID, func(_ time.Time, payload *stagedSubscriptionPayload) error {
		state := payload.EmailLogin
		if state == nil {
			state = &stagedEmailLogin{}
		}
		if state.Mailed && state.Email != "" && state.Email != email {
			return app_errors.ErrValidation
		}
		state.Email = email
		state.Mailed = true
		payload.EmailLogin = state
		return nil
	}, nil)
	return err
}

func (s *Service) reserveEmailVerify(ctx context.Context, stageID, email string) (reservedEmailLogin, error) {
	return s.updatePendingEmailLogin(ctx, stageID, func(_ time.Time, payload *stagedSubscriptionPayload) error {
		state := payload.EmailLogin
		if state == nil || !state.Mailed || state.Email == "" || state.Email != email {
			return app_errors.ErrValidation
		}
		if state.Attempts >= maxEmailVerifyAttempts {
			return app_errors.ErrEmailLoginLimited
		}
		state.Attempts++
		payload.EmailLogin = state
		return nil
	}, nil)
}

func (s *Service) updatePendingEmailLogin(
	ctx context.Context,
	stageID string,
	mutate func(now time.Time, payload *stagedSubscriptionPayload) error,
	reserved *reservedEmailLogin,
) (reservedEmailLogin, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		var value reservedEmailLogin
		lastErr = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var row models.CredentialStage
			if err := tx.Where("id = ? AND status = ?", stageID, models.CredentialStagePendingAuthorization).Take(&row).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return app_errors.ErrAuthorizationStateInvalid
				}
				return app_errors.ParseDBError(err)
			}
			now := s.now().UTC()
			if now.UnixMilli() >= row.ExpiresAtMS {
				return app_errors.ErrStagedCredentialExpired
			}
			plaintext, err := s.encryption.Decrypt(row.EncryptedPayload)
			if err != nil {
				return app_errors.ErrInternalServer
			}
			payload, err := decodeStagedAuthorizationPayload(row.PayloadSchemaVersion, []byte(plaintext))
			plaintext = ""
			if err != nil || payload.State == "" || len(payload.DriverState) == 0 {
				return app_errors.ErrAuthorizationStateInvalid
			}
			if err := mutate(now, &payload); err != nil {
				return err
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				return app_errors.ErrInternalServer
			}
			ciphertext, err := s.encryption.Encrypt(string(encoded))
			clear(encoded)
			if err != nil {
				return app_errors.ErrInternalServer
			}
			result := tx.Model(&models.CredentialStage{}).
				Where("id = ? AND status = ? AND updated_at_ms = ?", row.ID, models.CredentialStagePendingAuthorization, row.UpdatedAtMS).
				Updates(map[string]any{
					"encrypted_payload": ciphertext,
					"updated_at_ms":     now.UnixMilli(),
				})
			if result.Error != nil {
				return app_errors.ParseDBError(result.Error)
			}
			if result.RowsAffected != 1 {
				return errEmailLoginConflict
			}
			value = reservedEmailLogin{
				channelID: channel.ID(row.ChannelID), driverState: append([]byte(nil), payload.DriverState...), state: payload.State,
			}
			return nil
		})
		if errors.Is(lastErr, errEmailLoginConflict) {
			continue
		}
		if lastErr != nil {
			return reservedEmailLogin{}, lastErr
		}
		if reserved != nil {
			*reserved = value
		}
		return value, nil
	}
	return reservedEmailLogin{}, app_errors.ErrEmailLoginLimited
}

var errEmailLoginConflict = errors.New("email login stage changed")

func emailLoginError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, subscriptionruntime.ErrEmailLoginUnavailable):
		return app_errors.ErrBadGateway
	case errors.Is(err, subscriptionruntime.ErrEmailLoginRejected):
		return app_errors.ErrEmailLoginRejected
	default:
		message := err.Error()
		if strings.Contains(message, "invalid Mirasim") || strings.Contains(message, "invalid OAuth") {
			return app_errors.ErrValidation
		}
		return app_errors.ErrEmailLoginRejected
	}
}
