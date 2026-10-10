package control

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"gpt-load/internal/mirasimstatus"
)

type recordingMirasimReader struct {
	result mirasimstatus.Snapshot
	calls  int
	ctx    context.Context
}

func (r *recordingMirasimReader) Snapshot(ctx context.Context) mirasimstatus.Snapshot {
	r.calls++
	r.ctx = ctx
	return r.result
}

func TestMirasimStatusHandlerReturnsExplicitUnavailableSnapshot(t *testing.T) {
	t.Parallel()
	initControlI18n(t)
	for _, reader := range []*recordingMirasimReader{nil, {result: mirasimstatus.Snapshot{
		SourceURL: mirasimstatus.SourceURL, CheckedAt: time.Now().UTC(), Stale: true,
		Error: "Mirasim status: public source returned HTTP 503",
	}}} {
		server := &Server{}
		if reader != nil {
			server.mirasimStatus = reader
		}
		engine := gin.New()
		engine.GET("/status", server.handleMirasimStatus)
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/status", nil))
		if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
		}
		var envelope struct {
			Data mirasimstatus.Snapshot `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		got := envelope.Data
		if !got.Stale || got.Data != nil || got.FetchedAt != nil || got.Error == "" || got.CheckedAt.IsZero() || got.SourceURL != mirasimstatus.SourceURL {
			t.Fatalf("unavailable snapshot = %+v", got)
		}
		if !strings.Contains(recorder.Body.String(), `"data":null`) || !strings.Contains(recorder.Body.String(), `"fetched_at":null`) {
			t.Fatalf("null contract = %s", recorder.Body.String())
		}
	}
}

func TestMirasimStatusHandlerRejectsAllQueryParametersBeforeFetch(t *testing.T) {
	t.Parallel()
	initControlI18n(t)
	reader := &recordingMirasimReader{}
	server := &Server{mirasimStatus: reader}
	engine := gin.New()
	engine.GET("/status", server.handleMirasimStatus)
	for _, path := range []string{"/status?", "/status?force=true", "/status?url=https://evil.invalid", "/status?proxy=http://127.0.0.1", "/status?token=secret"} {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s", path, recorder.Code, recorder.Body.String())
		}
	}
	if reader.calls != 0 {
		t.Fatalf("rejected requests fetched %d times", reader.calls)
	}
}

type mirasimRoundTripFunc func(*http.Request) (*http.Response, error)

func (f mirasimRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMirasimStatusHandlerNeverForwardsCallerAuthentication(t *testing.T) {
	t.Parallel()
	initControlI18n(t)
	calls := 0
	reader := mirasimstatus.New(&http.Client{Transport: mirasimRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.String() != mirasimstatus.SourceURL || request.Method != http.MethodGet {
			t.Errorf("unexpected request: %+v", request)
		}
		for _, key := range []string{"Authorization", "Proxy-Authorization", "X-Api-Key", "Cookie"} {
			if request.Header.Get(key) != "" {
				t.Errorf("caller %s was forwarded", key)
			}
		}
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("upstream unavailable"))}, nil
	})})
	server := &Server{mirasimStatus: reader}
	engine := gin.New()
	engine.GET("/status", server.handleMirasimStatus)
	request := httptest.NewRequest(http.MethodGet, "/status", nil)
	request.Header.Set("Authorization", "Bearer private-management-key")
	request.Header.Set("Proxy-Authorization", "Basic secret")
	request.Header.Set("X-Api-Key", "private-api-key")
	request.Header.Set("Cookie", "session=private-session")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != 200 || calls != 1 || !strings.Contains(recorder.Body.String(), `"stale":true`) {
		t.Fatalf("response = %d %s, calls=%d", recorder.Code, recorder.Body.String(), calls)
	}
}

func TestMirasimStatusHandlerPassesContextAndCachedData(t *testing.T) {
	t.Parallel()
	initControlI18n(t)
	now := time.Now().UTC()
	reader := &recordingMirasimReader{result: mirasimstatus.Snapshot{
		SourceURL: mirasimstatus.SourceURL, CheckedAt: now, FetchedAt: &now,
		Stale: true, Error: "Mirasim status: public source returned HTTP 503",
		Data: &mirasimstatus.Data{Schema: 2},
	}}
	server := &Server{mirasimStatus: reader}
	engine := gin.New()
	engine.GET("/status", server.handleMirasimStatus)
	request := httptest.NewRequest(http.MethodGet, "/status", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != 200 || reader.calls != 1 || reader.ctx != request.Context() {
		t.Fatalf("request not passed to reader")
	}
	var envelope struct {
		Data mirasimstatus.Snapshot `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Data.Stale || envelope.Data.Data == nil || envelope.Data.Data.Schema != 2 || envelope.Data.Error == "" {
		t.Fatalf("cached error data lost: %+v", envelope.Data)
	}
}
