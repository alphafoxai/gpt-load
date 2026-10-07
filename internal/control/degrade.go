package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/sirupsen/logrus"

	"gpt-load/internal/channel"
	"gpt-load/internal/degrade"
	"gpt-load/internal/execution"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/protocol"
	"gpt-load/internal/storage/models"
)

const (
	degradeScheduleSetting = models.InternalSystemSettingPrefix + "degrade.schedule"
	degradeResultsSetting  = models.InternalSystemSettingPrefix + "degrade.results"
	degradeDefaultModel    = "gpt-6-luna"
	degradeMinInterval     = 5 * time.Minute
	degradeMaxInterval     = 24 * time.Hour
	degradeDefaultInterval = 30 * time.Minute
	degradeHistory         = 20
	degradeConcurrency     = 2
	degradeAttemptTimeout  = 3 * time.Minute
)

// DegradeSchedule is the inspection the board repeats. It only records results;
// it never changes a credential's weight.
type DegradeSchedule struct {
	Enabled     bool   `json:"enabled"`
	IntervalMS  int64  `json:"interval_ms"`
	Model       string `json:"model"`
	ChannelID   string `json:"channel_id"`
	NextRunAtMS int64  `json:"next_run_at_ms"`
	LastRunAtMS int64  `json:"last_run_at_ms"`
	Running     bool   `json:"running"`
}

type degradeStoredResult struct {
	ID           string               `json:"id"`
	CredentialID uint                 `json:"credential_id"`
	GroupID      uint                 `json:"group_id"`
	Model        string               `json:"model"`
	Status       string               `json:"status"`
	Degraded     *bool                `json:"degraded"`
	Error        string               `json:"error,omitempty"`
	StartedAtMS  int64                `json:"started_at_ms"`
	DurationMS   int64                `json:"duration_ms"`
	InputTokens  int64                `json:"input_tokens"`
	OutputTokens int64                `json:"output_tokens"`
	Samples      []degrade.Sample     `json:"samples,omitempty"`
	Attribution  *degrade.Attribution `json:"attribution,omitempty"`
	BankRevision string               `json:"bank_revision"`
}

// DegradeResultResponse is one stored test, without the raw number sequences.
type DegradeResultResponse struct {
	ID           string               `json:"id"`
	CredentialID uint                 `json:"credential_id"`
	GroupID      uint                 `json:"group_id"`
	Model        string               `json:"model"`
	Status       string               `json:"status"`
	Degraded     *bool                `json:"degraded"`
	Error        string               `json:"error,omitempty"`
	StartedAtMS  int64                `json:"started_at_ms"`
	DurationMS   int64                `json:"duration_ms"`
	InputTokens  int64                `json:"input_tokens"`
	OutputTokens int64                `json:"output_tokens"`
	Prediction   string               `json:"prediction,omitempty"`
	Probability  float64              `json:"probability,omitempty"`
	Family       string               `json:"family,omitempty"`
	FamilyProb   float64              `json:"family_probability,omitempty"`
	Attribution  *degrade.Attribution `json:"attribution,omitempty"`
}

// DegradeCredentialResponse is one Codex credential plus its latest verdict.
type DegradeCredentialResponse struct {
	CredentialID uint                   `json:"credential_id"`
	GroupID      uint                   `json:"group_id"`
	GroupName    string                 `json:"group_name"`
	Identity     string                 `json:"identity"`
	Plan         string                 `json:"plan,omitempty"`
	Status       string                 `json:"status"`
	Weight       int                    `json:"weight"`
	Running      bool                   `json:"running"`
	Latest       *DegradeResultResponse `json:"latest,omitempty"`
}

// DegradeBoardResponse is everything the board renders in one read.
type DegradeBoardResponse struct {
	Schedule    DegradeSchedule             `json:"schedule"`
	Credentials []DegradeCredentialResponse `json:"credentials"`
	History     []DegradeResultResponse     `json:"history"`
}

type degradeRunRequest struct {
	Model         string `json:"model"`
	CredentialIDs []uint `json:"credential_ids"`
	All           bool   `json:"all"`
}

type degradeScheduleRequest struct {
	Enabled    *bool   `json:"enabled"`
	IntervalMS *int64  `json:"interval_ms"`
	Model      *string `json:"model"`
	ChannelID  *string `json:"channel_id"`
}

type degradeState struct {
	mu       sync.Mutex
	running  map[uint]struct{}
	sweeping bool
}

func (s *Service) degrade() *degradeState {
	s.degradeOnce.Do(func() { s.degradeState = &degradeState{running: map[uint]struct{}{}} })
	return s.degradeState
}

func defaultDegradeSchedule(now time.Time) DegradeSchedule {
	return DegradeSchedule{
		Enabled: false, IntervalMS: degradeDefaultInterval.Milliseconds(),
		Model: degradeDefaultModel, ChannelID: string(channel.Codex),
		NextRunAtMS: now.Add(degradeDefaultInterval).UnixMilli(),
	}
}

func (s *Service) loadDegradeSchedule(ctx context.Context) (DegradeSchedule, error) {
	schedule := defaultDegradeSchedule(s.now())
	var row models.SystemSetting
	err := s.db.WithContext(ctx).Where(&models.SystemSetting{Key: degradeScheduleSetting}).Take(&row).Error
	if err == nil && row.Value != "" {
		if decodeErr := json.Unmarshal([]byte(row.Value), &schedule); decodeErr != nil {
			return DegradeSchedule{}, app_errors.ErrInternalServer
		}
	} else if err != nil && err != gorm.ErrRecordNotFound {
		return DegradeSchedule{}, app_errors.ParseDBError(err)
	}
	if schedule.IntervalMS < degradeMinInterval.Milliseconds() {
		schedule.IntervalMS = degradeDefaultInterval.Milliseconds()
	}
	if strings.TrimSpace(schedule.Model) == "" {
		schedule.Model = degradeDefaultModel
	}
	if strings.TrimSpace(schedule.ChannelID) == "" {
		schedule.ChannelID = string(channel.Codex)
	}
	state := s.degrade()
	state.mu.Lock()
	schedule.Running = state.sweeping || len(state.running) > 0
	state.mu.Unlock()
	return schedule, nil
}

func (s *Service) saveDegradeSchedule(ctx context.Context, schedule DegradeSchedule) error {
	schedule.Running = false
	encoded, err := json.Marshal(schedule)
	if err != nil {
		return app_errors.ErrInternalServer
	}
	return s.withControlTransaction(ctx, func(tx *gorm.DB) error {
		row := models.SystemSetting{Key: degradeScheduleSetting, Value: string(encoded)}
		return app_errors.ParseDBError(tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at_ms"}),
		}).Create(&row).Error)
	})
}

func (s *Service) loadDegradeResults(ctx context.Context) (map[uint][]degradeStoredResult, error) {
	stored := map[uint][]degradeStoredResult{}
	var row models.SystemSetting
	err := s.db.WithContext(ctx).Where(&models.SystemSetting{Key: degradeResultsSetting}).Take(&row).Error
	if err == gorm.ErrRecordNotFound || (err == nil && row.Value == "") {
		return stored, nil
	}
	if err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	if err := json.Unmarshal([]byte(row.Value), &stored); err != nil {
		return nil, app_errors.ErrInternalServer
	}
	return stored, nil
}

func (s *Service) saveDegradeResults(ctx context.Context, stored map[uint][]degradeStoredResult) error {
	encoded, err := json.Marshal(stored)
	if err != nil {
		return app_errors.ErrInternalServer
	}
	return s.withControlTransaction(ctx, func(tx *gorm.DB) error {
		row := models.SystemSetting{Key: degradeResultsSetting, Value: string(encoded)}
		return app_errors.ParseDBError(tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at_ms"}),
		}).Create(&row).Error)
	})
}

func (s *Service) DegradeBoard(ctx context.Context) (DegradeBoardResponse, error) {
	schedule, err := s.loadDegradeSchedule(ctx)
	if err != nil {
		return DegradeBoardResponse{}, err
	}
	stored, err := s.loadDegradeResults(ctx)
	if err != nil {
		return DegradeBoardResponse{}, err
	}
	credentials, err := s.degradeCredentials(ctx, schedule.ChannelID)
	if err != nil {
		return DegradeBoardResponse{}, err
	}
	running := s.degradeRunning()
	history := make([]DegradeResultResponse, 0)
	for i := range credentials {
		rows := stored[credentials[i].CredentialID]
		if len(rows) > 0 {
			latest := degradeResultResponse(rows[len(rows)-1], false)
			credentials[i].Latest = &latest
		}
		_, credentials[i].Running = running[credentials[i].CredentialID]
		for _, row := range rows {
			history = append(history, degradeResultResponse(row, false))
		}
	}
	sort.Slice(history, func(i, j int) bool { return history[i].StartedAtMS > history[j].StartedAtMS })
	if len(history) > 100 {
		history = history[:100]
	}
	return DegradeBoardResponse{Schedule: schedule, Credentials: credentials, History: history}, nil
}

func degradeResultResponse(row degradeStoredResult, detail bool) DegradeResultResponse {
	response := DegradeResultResponse{
		ID: row.ID, CredentialID: row.CredentialID, GroupID: row.GroupID, Model: row.Model,
		Status: row.Status, Degraded: row.Degraded, Error: row.Error, StartedAtMS: row.StartedAtMS,
		DurationMS: row.DurationMS, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens,
	}
	if row.Attribution != nil {
		response.Prediction = row.Attribution.Prediction
		response.Probability = row.Attribution.Probability
		response.Family = row.Attribution.FamilyPrediction
		response.FamilyProb = row.Attribution.FamilyProbability
		if detail {
			response.Attribution = row.Attribution
		}
	}
	return response
}

func (s *Service) degradeCredentials(ctx context.Context, channelID string) ([]DegradeCredentialResponse, error) {
	var groups []models.Group
	if err := s.db.WithContext(ctx).Where("channel_id = ? AND enabled = ?", channelID, true).Find(&groups).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	out := make([]DegradeCredentialResponse, 0)
	for _, group := range groups {
		page := 1
		for {
			listed, err := s.ListGroupCredentials(ctx, group.ID, CredentialCollectionQuery{Page: page, PageSize: 100})
			if err != nil {
				return nil, err
			}
			for _, item := range listed.Items {
				identity := strings.TrimSpace(item.Account.Email)
				if identity == "" {
					identity = item.Mask
				}
				plan := ""
				if item.Observation != nil && item.Observation.Snapshot != nil {
					plan = item.Observation.Snapshot.Plan.Name
				}
				out = append(out, DegradeCredentialResponse{
					CredentialID: item.CredentialID, GroupID: group.ID, GroupName: group.Name,
					Identity: identity, Plan: plan, Status: item.EffectiveStatus, Weight: item.Weight,
				})
			}
			if page >= listed.Pagination.TotalPages {
				break
			}
			page++
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GroupName != out[j].GroupName {
			return out[i].GroupName < out[j].GroupName
		}
		return out[i].Identity < out[j].Identity
	})
	return out, nil
}

func (s *Service) degradeRunning() map[uint]struct{} {
	state := s.degrade()
	state.mu.Lock()
	defer state.mu.Unlock()
	out := make(map[uint]struct{}, len(state.running))
	for id := range state.running {
		out[id] = struct{}{}
	}
	return out
}

// UpdateDegradeSchedule stores the interval and model the board repeats with.
func (s *Service) UpdateDegradeSchedule(ctx context.Context, request degradeScheduleRequest) (DegradeSchedule, error) {
	schedule, err := s.loadDegradeSchedule(ctx)
	if err != nil {
		return DegradeSchedule{}, err
	}
	if request.Enabled != nil {
		schedule.Enabled = *request.Enabled
	}
	if request.IntervalMS != nil {
		if *request.IntervalMS < degradeMinInterval.Milliseconds() || *request.IntervalMS > degradeMaxInterval.Milliseconds() {
			return DegradeSchedule{}, app_errors.ErrValidation
		}
		schedule.IntervalMS = *request.IntervalMS
	}
	if request.Model != nil {
		model := strings.TrimSpace(*request.Model)
		if model == "" || len(model) > 128 {
			return DegradeSchedule{}, app_errors.ErrValidation
		}
		schedule.Model = model
	}
	if request.ChannelID != nil && strings.TrimSpace(*request.ChannelID) != "" {
		schedule.ChannelID = strings.TrimSpace(*request.ChannelID)
	}
	schedule.NextRunAtMS = s.now().Add(time.Duration(schedule.IntervalMS) * time.Millisecond).UnixMilli()
	if err := s.saveDegradeSchedule(ctx, schedule); err != nil {
		return DegradeSchedule{}, err
	}
	return s.loadDegradeSchedule(ctx)
}

// StartDegradeRun tests the requested Codex credentials in the background.
func (s *Service) StartDegradeRun(ctx context.Context, request degradeRunRequest) (int, error) {
	schedule, err := s.loadDegradeSchedule(ctx)
	if err != nil {
		return 0, err
	}
	model := strings.TrimSpace(request.Model)
	if model == "" {
		model = schedule.Model
	}
	credentials, err := s.degradeCredentials(ctx, schedule.ChannelID)
	if err != nil {
		return 0, err
	}
	wanted := map[uint]struct{}{}
	if !request.All {
		for _, id := range request.CredentialIDs {
			wanted[id] = struct{}{}
		}
	}
	selected := make([]DegradeCredentialResponse, 0)
	state := s.degrade()
	state.mu.Lock()
	for _, credential := range credentials {
		if !request.All {
			if _, ok := wanted[credential.CredentialID]; !ok {
				continue
			}
		}
		if credential.Status != "available" {
			continue
		}
		if _, busy := state.running[credential.CredentialID]; busy {
			continue
		}
		state.running[credential.CredentialID] = struct{}{}
		selected = append(selected, credential)
	}
	state.mu.Unlock()
	for _, credential := range selected {
		go s.runDegradeCredential(credential, model)
	}
	return len(selected), nil
}

func (s *Service) runDegradeCredential(credential DegradeCredentialResponse, model string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logrus.WithFields(logrus.Fields{"credential_id": credential.CredentialID, "panic": fmt.Sprint(recovered)}).
				Error("degrade test panicked")
			s.appendDegradeResult(degradeStoredResult{
				ID: fmt.Sprintf("%d-panic", credential.CredentialID), CredentialID: credential.CredentialID,
				GroupID: credential.GroupID, Model: model, Status: "failed",
				Error: "测试过程中断", StartedAtMS: s.now().UnixMilli(), BankRevision: degrade.BankRevision(),
			})
		}
		state := s.degrade()
		state.mu.Lock()
		delete(state.running, credential.CredentialID)
		state.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(degrade.TraceChallenges)*degradeAttemptTimeout)
	defer cancel()
	started := s.now()
	result := degradeStoredResult{
		ID:           fmt.Sprintf("%d-%d", credential.CredentialID, started.UnixNano()),
		CredentialID: credential.CredentialID, GroupID: credential.GroupID, Model: model,
		StartedAtMS: started.UnixMilli(), BankRevision: degrade.BankRevision(), Status: "completed",
	}
	samples := degrade.Challenges(started)
	collected := make([]degrade.Sample, 0, len(samples))
	var input, output int64
	failed := 0
	for _, challenge := range samples {
		sample, usage, err := s.degradeProbe(ctx, credential, model, challenge)
		collected = append(collected, sample)
		input += usage.input
		output += usage.output
		if err != nil {
			failed++
		}
	}
	for i := range collected {
		collected[i].Text = ""
	}
	result.Samples = collected
	result.InputTokens, result.OutputTokens = input, output
	attribution, err := degrade.Analyze(collected)
	if err != nil {
		result.Status = "inconclusive"
		result.Error = err.Error()
		if failed == len(samples) && collected[len(collected)-1].Error != "" {
			result.Error = collected[len(collected)-1].Error
		}
	} else {
		result.Attribution = &attribution
		consistent := attribution.Consistent(model)
		result.Degraded = &consistent
		*result.Degraded = !consistent
		if attribution.Used < len(samples) {
			result.Status = "partial"
		}
	}
	result.DurationMS = s.now().Sub(started).Milliseconds()
	s.appendDegradeResult(result)
}

type degradeUsage struct{ input, output int64 }

func (s *Service) degradeProbe(ctx context.Context, credential DegradeCredentialResponse, model string, challenge degrade.Challenge) (degrade.Sample, degradeUsage, error) {
	sample := degrade.Sample{ExpectedCount: challenge.ExpectedCount}
	attemptCtx, cancel := context.WithTimeout(ctx, degradeAttemptTimeout)
	defer cancel()
	body, err := json.Marshal(map[string]any{
		"model": model,
		"input": []map[string]string{{"role": "user", "content": challenge.Prompt}},
	})
	if err != nil {
		sample.Error = "无法构造测试请求"
		return sample, degradeUsage{}, err
	}
	executed, err := s.executeDegrade(attemptCtx, credential, model, body)
	sample.Attempts = 1
	if err != nil {
		sample.Error = clipDegradeError(err.Error())
		return sample, degradeUsage{}, err
	}
	if executed.result.Error != nil || executed.result.StatusCode < 200 || executed.result.StatusCode >= 300 {
		message := fmt.Sprintf("上游返回 HTTP %d", executed.result.StatusCode)
		if executed.result.Error != nil && executed.result.Error.Summary != "" {
			message = executed.result.Error.Summary
		}
		sample.Error = clipDegradeError(message)
		return sample, degradeUsage{}, fmt.Errorf("%s", message)
	}
	text := degradeAnswerText(executed.result.Body)
	sample.Text = degrade.ClipText(text)
	sample.Parsed = len(degrade.ParseNumbers(sample.Text))
	sample.Accepted = sample.Parsed >= max(80, int(float64(challenge.ExpectedCount)*0.55))
	if !sample.Accepted {
		sample.Error = "有效数字不足"
	}
	var input, output int64
	if executed.result.Usage != nil {
		input = executed.result.Usage.Normalized.Tokens.UncachedInput + executed.result.Usage.Normalized.Tokens.CacheRead
		output = executed.result.Usage.Normalized.Tokens.Output
	}
	return sample, degradeUsage{input: input, output: output}, nil
}

type degradeExecution struct {
	result execution.AttemptResult
}

func (s *Service) executeDegrade(ctx context.Context, credential DegradeCredentialResponse, model string, body []byte) (degradeExecution, error) {
	if s.executor == nil || s.manager == nil {
		return degradeExecution{}, app_errors.ErrInternalServer
	}
	if s.manager == nil {
		return degradeExecution{}, app_errors.ErrInternalServer
	}
	snapshot := s.manager.Current()
	if snapshot == nil {
		return degradeExecution{}, app_errors.ErrInternalServer
	}
	group, ok := snapshot.Groups[credential.GroupID]
	if !ok {
		return degradeExecution{}, app_errors.ErrValidation
	}
	mode, supported := group.ResolvedTarget.ModeForModel(protocol.OpenAIResponses, execution.OperationResponsesCreate, model)
	if !supported {
		return degradeExecution{}, app_errors.ErrValidation
	}
	entries, err := s.registry.SnapshotGroupCredentialEntriesExact(credential.GroupID, []uint{credential.CredentialID})
	if err != nil || len(entries) != 1 {
		return degradeExecution{}, app_errors.ErrValidation
	}
	entry := entries[0]
	secret, err := s.encryption.Decrypt(entry.EncryptedValue)
	if err != nil {
		return degradeExecution{}, err
	}
	plaintext := []byte(secret)
	defer clear(plaintext)
	requestID, err := s.newExecutionID()
	if err != nil {
		return degradeExecution{}, err
	}
	attemptID, err := s.newExecutionID()
	if err != nil {
		return degradeExecution{}, err
	}
	spec := execution.NewAttemptSpec(execution.AttemptSpec{
		RequestID: requestID, AttemptID: attemptID, Sequence: 1,
		ChannelID: string(group.ChannelID), RouteMode: execution.RouteMode(mode),
		ClientProtocol: protocol.OpenAIResponses, Operation: execution.OperationResponsesCreate,
		ClientModel: model, UpstreamModel: model, Method: http.MethodPost, Path: "/v1/responses",
		Header: http.Header{"Content-Type": []string{"application/json"}}, Body: body,
		TargetConfig: group.ResolvedTarget.TargetConfig,
		Timeouts:     execution.AttemptTimeouts{Request: degradeAttemptTimeout, FirstByte: degradeAttemptTimeout},
		Credential:   execution.NewCredentialSnapshot(entry.ID, entry.Version, entry.IdentityGeneration, plaintext),
	})
	if err := spec.Validate(); err != nil {
		return degradeExecution{}, err
	}
	return degradeExecution{result: s.executor.Execute(ctx, spec)}, nil
}

func degradeAnswerText(body []byte) string {
	var payload struct {
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if json.Unmarshal(body, &payload) == nil {
		var b strings.Builder
		for _, item := range payload.Output {
			for _, content := range item.Content {
				b.WriteString(content.Text)
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	return string(body)
}

func clipDegradeError(message string) string {
	message = strings.TrimSpace(message)
	if len(message) > 300 {
		return message[:300]
	}
	return message
}

func (s *Service) appendDegradeResult(result degradeStoredResult) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.degradeWrite.Lock()
	defer s.degradeWrite.Unlock()
	stored, err := s.loadDegradeResults(ctx)
	if err != nil {
		logrus.WithError(err).WithField("credential_id", result.CredentialID).Error("degrade result was not loaded")
		return
	}
	rows := append(stored[result.CredentialID], result)
	if len(rows) > degradeHistory {
		rows = rows[len(rows)-degradeHistory:]
	}
	stored[result.CredentialID] = rows
	if err := s.saveDegradeResults(ctx, stored); err != nil {
		logrus.WithError(err).WithField("credential_id", result.CredentialID).Error("degrade result was not saved")
	}
}

// RunDegradeInspection repeats the saved schedule until the process stops.
func (s *Service) RunDegradeInspection(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.degradeTick(ctx)
		}
	}
}

func (s *Service) degradeTick(ctx context.Context) {
	schedule, err := s.loadDegradeSchedule(ctx)
	if err != nil || !schedule.Enabled {
		return
	}
	if s.now().UnixMilli() < schedule.NextRunAtMS {
		return
	}
	state := s.degrade()
	state.mu.Lock()
	if state.sweeping {
		state.mu.Unlock()
		return
	}
	state.sweeping = true
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		state.sweeping = false
		state.mu.Unlock()
	}()
	schedule.LastRunAtMS = s.now().UnixMilli()
	schedule.NextRunAtMS = s.now().Add(time.Duration(schedule.IntervalMS) * time.Millisecond).UnixMilli()
	if err := s.saveDegradeSchedule(ctx, schedule); err != nil {
		return
	}
	_, _ = s.StartDegradeRun(ctx, degradeRunRequest{Model: schedule.Model, All: true})
}
