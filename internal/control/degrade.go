package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
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
	degradeAttemptTimeout  = 3 * time.Minute
	// degradeConcurrency is the reference probe's challenge fan-out: the three
	// challenges of one credential are asked in parallel, so a credential
	// finishes in the time of one challenge rather than three.
	degradeConcurrency = 3
	// degradeProbeAttempts mirrors the reference probe: an upstream that fails
	// before it answers is retried, an answer that is merely short is not.
	degradeProbeAttempts = 3
	degradeRetryBaseWait = 2 * time.Second
	// degradeRunTimeout bounds one credential: the challenges run in parallel,
	// so the budget only has to cover a single challenge using every attempt.
	degradeRunTimeout = degradeProbeAttempts*degradeAttemptTimeout + time.Minute
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
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && row.Value == "") {
		return stored, nil
	}
	if err != nil {
		return nil, err
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
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at_ms"}),
		}).Create(&row).Error
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
	ctx, cancel := context.WithTimeout(context.Background(), degradeRunTimeout)
	defer cancel()
	started := s.now()
	result := degradeStoredResult{
		ID:           fmt.Sprintf("%d-%d", credential.CredentialID, started.UnixNano()),
		CredentialID: credential.CredentialID, GroupID: credential.GroupID, Model: model,
		StartedAtMS: started.UnixMilli(), BankRevision: degrade.BankRevision(), Status: "completed",
	}
	samples := degrade.Challenges(started)
	collected, usage := s.degradeProbes(ctx, credential, model, samples)
	result.Samples = collected
	result.InputTokens, result.OutputTokens = usage.input, usage.output
	attribution, err := analyzeDegradeSamples(collected)
	if err != nil {
		// Report why nothing could be scored. The reason names the upstream or
		// the model, and must not be replaced by whichever sample happened to be
		// collected last: that masks a timeout as a short answer and vice versa.
		result.Status = "inconclusive"
		result.Error = degradeInconclusiveReason(collected, err)
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

// degradeProbes asks every challenge of one credential in parallel, matching the
// reference probe's fan-out. Samples are returned in challenge order, and each
// probe recovers its own panic: the board must never take the process down.
func (s *Service) degradeProbes(ctx context.Context, credential DegradeCredentialResponse, model string, samples []degrade.Challenge) ([]degrade.Sample, degradeUsage) {
	gate := make(chan struct{}, degradeConcurrency)
	collected := make([]degrade.Sample, len(samples))
	probes := make([]degradeUsage, len(samples))
	var mu sync.Mutex
	var wait sync.WaitGroup
	for index, challenge := range samples {
		wait.Add(1)
		go func() {
			defer wait.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					degradeLog(fmt.Errorf("panic: %v", recovered), credential.CredentialID, "degrade probe panicked")
					mu.Lock()
					collected[index] = degrade.Sample{ExpectedCount: challenge.ExpectedCount, Error: "测试过程中断"}
					mu.Unlock()
				}
			}()
			gate <- struct{}{}
			sample, usage, _ := s.degradeProbe(ctx, credential, model, challenge)
			<-gate
			mu.Lock()
			collected[index] = sample
			probes[index] = usage
			mu.Unlock()
		}()
	}
	wait.Wait()
	var total degradeUsage
	for _, usage := range probes {
		total.input += usage.input
		total.output += usage.output
	}
	return collected, total
}

// analyzeDegradeSamples scores the collected answers and then drops their
// bodies: Analyze reconstructs each number sequence from Sample.Text, so the
// text must survive until scoring finishes, while the board only keeps the
// verdict and diagnostics.
func analyzeDegradeSamples(collected []degrade.Sample) (degrade.Attribution, error) {
	attribution, err := degrade.Analyze(collected)
	for i := range collected {
		collected[i].Text = ""
	}
	return attribution, err
}

// degradeInconclusiveReason explains why nothing could be scored. The aggregate
// message has to separate an upstream that never answered from a model that
// answered too briefly: only the second one is evidence about the model, and
// reporting the first as "no numbers" hides the real fault.
func degradeInconclusiveReason(collected []degrade.Sample, err error) string {
	answered := 0
	reasons := make([]string, 0, len(collected))
	for _, sample := range collected {
		if sample.Parsed > 0 {
			answered++
			continue
		}
		if sample.Error == "" {
			continue
		}
		if !slices.Contains(reasons, sample.Error) {
			reasons = append(reasons, sample.Error)
		}
	}
	if answered > 0 {
		return "模型回答的有效数字不足"
	}
	if len(reasons) > 0 {
		return strings.Join(reasons, "；")
	}
	return err.Error()
}

// degradeProbe asks one challenge and retries only failures that happened
// before the model produced an answer: a transport error, a cancellation, or an
// upstream status that asks for another attempt. An answer that is complete but
// too short is a verdict about the model, so it is never retried.
func (s *Service) degradeProbe(ctx context.Context, credential DegradeCredentialResponse, model string, challenge degrade.Challenge) (degrade.Sample, degradeUsage, error) {
	sample := degrade.Sample{ExpectedCount: challenge.ExpectedCount}
	body, err := degrade.CodexTurn(model, challenge.Prompt, s.now())
	if err != nil {
		sample.Error = "无法构造测试请求"
		return sample, degradeUsage{}, err
	}
	var usage degradeUsage
	var lastErr error
	for attempt := 0; attempt < degradeProbeAttempts; attempt++ {
		if attempt > 0 {
			// The reference probe backs off 2s then 4s before redispatching.
			if err := s.degradeRetryWait(ctx, degradeRetryBaseWait<<(attempt-1)); err != nil {
				break
			}
		}
		attemptCtx, cancel := context.WithTimeout(ctx, degradeAttemptTimeout)
		executed, err := s.executeDegrade(attemptCtx, credential, model, body)
		cancel()
		sample.Attempts++
		if executed.result.Usage != nil {
			usage.input += executed.result.Usage.Normalized.Tokens.UncachedInput + executed.result.Usage.Normalized.Tokens.CacheRead
			usage.output += executed.result.Usage.Normalized.Tokens.Output
		}
		text := degradeProbeText(executed)
		if text != "" {
			// Keep whichever attempt produced the most usable answer: a retry
			// can fail halfway and deliver less than the attempt before it.
			clipped := degrade.ClipText(text)
			if parsed := len(degrade.ParseNumbers(clipped)); parsed >= sample.Parsed {
				sample.Text = clipped
				sample.Parsed = parsed
				sample.Accepted = parsed >= degrade.MinimumNumbers(challenge.ExpectedCount)
			}
		}
		if sample.Accepted {
			// The answer is already usable, so another attempt cannot improve the
			// verdict. This also covers a stream that broke after the model had
			// emitted the whole answer.
			sample.Error = ""
			return sample, usage, nil
		}
		if err == nil && executed.result.Error == nil &&
			executed.result.StatusCode >= 200 && executed.result.StatusCode < 300 {
			// The exchange itself succeeded, so it is not retried. Whether the
			// answer is usable is decided below and reported on the sample.
			lastErr = nil
			sample.Error = ""
			break
		}
		kind, message := degradeProbeFailure(executed, err)
		sample.Error = clipDegradeError(message)
		lastErr = fmt.Errorf("%s", message)
		if !degradeRetryable(kind) {
			break
		}
	}
	if sample.Text == "" {
		// Nothing usable arrived, so the caller must hear about the failure even
		// if every attempt merely produced an empty answer.
		if lastErr == nil {
			lastErr = fmt.Errorf("%s", "上游没有返回内容")
		}
		if sample.Error == "" {
			sample.Error = clipDegradeError(lastErr.Error())
		}
		return sample, usage, lastErr
	}
	// The upstream answered at least partially. Score what arrived, so a
	// truncated answer is reported as a short answer, not as a failed request.
	sample.Error = "有效数字不足"
	return sample, usage, nil
}

// degradeProbeText reads the answer from the stream, falling back to the body of
// a non-streaming response.
func degradeProbeText(executed degradeExecution) string {
	if executed.text != "" {
		return executed.text
	}
	return degradeAnswerText(executed.result.Body)
}

// degradeFailureLabels names the failures the board can explain itself. The
// upstream summaries are English diagnostics meant for logs, so the board keeps
// its own wording for the causes it understands and falls back to the summary
// only when the cause is provider specific.
var degradeFailureLabels = map[execution.ErrorKind]string{
	execution.ErrorKindTimeout:   "上游响应超时",
	execution.ErrorKindCanceled:  "测试已取消",
	execution.ErrorKindTransport: "上游连接中断",
}

// degradeProbeFailure names what went wrong and decides whether another attempt
// can help. Retrying is only useful when the failure happened before the model
// answered: an expired attempt context, a cancelled request, a transport break,
// a rate limit, or an upstream fault.
func degradeProbeFailure(executed degradeExecution, err error) (execution.ErrorKind, string) {
	result := executed.result
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return execution.ErrorKindTimeout, degradeFailureLabels[execution.ErrorKindTimeout]
		}
		if errors.Is(err, context.Canceled) {
			return execution.ErrorKindCanceled, degradeFailureLabels[execution.ErrorKindCanceled]
		}
		return execution.ErrorKindInternal, clipDegradeError(err.Error())
	}
	if result.Error == nil {
		return degradeStatusFailure(result.StatusCode, "")
	}
	kind := result.Error.Kind
	if label, known := degradeFailureLabels[kind]; known {
		// A timeout or a dropped connection is the same fault whether or not the
		// upstream had started answering, so it always gets the board's wording.
		return kind, label
	}
	if kind == execution.ErrorKindHTTP || kind == execution.ErrorKindProvider {
		// The status decides whether another attempt can help, so that a rejected
		// request is not retried while an upstream fault is.
		return degradeStatusFailure(result.StatusCode, result.Error.Summary)
	}
	message := result.Error.Summary
	if message == "" {
		message = fmt.Sprintf("上游返回 HTTP %d", result.StatusCode)
	}
	return kind, message
}

// degradeStatusFailure classifies a completed exchange from its status code,
// preferring a provider supplied summary when there is one.
func degradeStatusFailure(status int, summary string) (execution.ErrorKind, string) {
	if status >= 200 && status < 300 {
		return execution.ErrorKindInternal, summary
	}
	if status == 0 {
		// No status at all means the request never reached the upstream, so
		// saying "HTTP 0" would describe a fault the user cannot act on.
		if summary != "" {
			return execution.ErrorKindTransport, summary
		}
		return execution.ErrorKindTransport, degradeFailureLabels[execution.ErrorKindTransport]
	}
	message := fmt.Sprintf("上游返回 HTTP %d", status)
	if summary != "" {
		message = summary
	}
	if status >= 500 || status == http.StatusTooManyRequests {
		// The reference probe retries these, plus a request that produced no
		// status. Other 4xx responses describe the request itself.
		return execution.ErrorKindHTTP, message
	}
	return execution.ErrorKindInvalidRequest, message
}

func degradeRetryable(kind execution.ErrorKind) bool {
	switch kind {
	case execution.ErrorKindTransport, execution.ErrorKindTimeout, execution.ErrorKindCanceled, execution.ErrorKindHTTP:
		return true
	default:
		return false
	}
}

func sleepDegradeRetry(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type degradeExecution struct {
	result execution.AttemptResult
	text   string
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
	var events []byte
	result := s.executor.ExecuteStream(ctx, spec, func(event execution.StreamEvent) error {
		if event.Kind == execution.StreamEventData {
			events = append(events, event.Data...)
		}
		return nil
	})
	return degradeExecution{result: degradeStreamResult(result), text: degradeStreamText(events)}, nil
}

func degradeStreamResult(result execution.StreamResult) execution.AttemptResult {
	return execution.AttemptResult{
		DispatchState: result.DispatchState, ResponseStarted: result.ResponseStarted,
		UpstreamProtocol: result.UpstreamProtocol, AppliedReasoning: result.AppliedReasoning,
		StatusCode: result.StatusCode, Header: result.Header, Model: result.Model,
		UpstreamRequestID: result.UpstreamRequestID, Usage: result.Usage, Error: result.Error,
	}
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
		degradeLog(err, result.CredentialID, "degrade result was not loaded")
		return
	}
	rows := append(stored[result.CredentialID], result)
	if len(rows) > degradeHistory {
		rows = rows[len(rows)-degradeHistory:]
	}
	stored[result.CredentialID] = rows
	if err := s.saveDegradeResults(ctx, stored); err != nil {
		degradeLog(err, result.CredentialID, "degrade result was not saved")
	}
}

func degradeLog(err error, credentialID uint, message string) {
	entry := logrus.WithField("credential_id", credentialID)
	// A database helper can hand back a typed nil (*APIError)(nil). Comparing
	// that with != nil is true, and calling Error on it panics. The panic
	// handler logs through here too, so that second panic used to take the
	// whole process down.
	if text, ok := degradeErrorText(err); ok {
		entry = entry.WithField("error", text)
	}
	entry.Error(message)
}

func degradeErrorText(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	value := reflect.ValueOf(err)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return "", false
		}
	}
	return err.Error(), true
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

type degradeOutputItem struct {
	Type    string `json:"type"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// degradeStreamText reads the answer out of a Responses event stream.
//
// Codex streams the answer as response.output_text.delta events and only closes
// the message item when the turn ends. A stream cut mid-answer — upstream
// timeout, reset connection, cancellation — therefore carries every number the
// model already emitted in deltas and none in a finished item, so the deltas
// must be accumulated as a fallback. A finished item still wins: it is the only
// place a refusal or a tool call is distinguished from an answer.
func degradeStreamText(events []byte) string {
	var items []degradeOutputItem
	var completed []byte
	var deltas strings.Builder
	for _, line := range bytes.Split(events, []byte("\n")) {
		data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:"))
		if !ok {
			continue
		}
		var event struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
			Item     json.RawMessage `json:"item"`
			Delta    string          `json:"delta"`
		}
		if json.Unmarshal(bytes.TrimSpace(data), &event) != nil {
			continue
		}
		switch event.Type {
		case "response.output_item.done":
			var item degradeOutputItem
			if json.Unmarshal(event.Item, &item) == nil {
				items = append(items, item)
			}
		case "response.output_text.delta":
			// Reasoning summaries carry their own delta events; their numbers
			// are not the model's answer and must not be scored.
			deltas.WriteString(event.Delta)
		case "response.completed", "response.incomplete", "response.failed":
			completed = append([]byte(nil), event.Response...)
		}
	}
	if text := degradeOutputText(completed); text != "" {
		return text
	}
	if text := degradeItemText(items); text != "" {
		return text
	}
	return deltas.String()
}

func degradeOutputText(body []byte) string {
	var payload struct {
		Output []degradeOutputItem `json:"output"`
	}
	if json.Unmarshal(body, &payload) != nil || len(payload.Output) == 0 {
		return ""
	}
	return degradeItemText(payload.Output)
}

func degradeItemText(items []degradeOutputItem) string {
	var text strings.Builder
	for _, item := range items {
		if item.Type != "" && item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			if part.Type == "" || part.Type == "output_text" || part.Type == "text" {
				text.WriteString(part.Text)
			}
		}
	}
	return text.String()
}
