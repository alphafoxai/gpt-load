package mirasimstatus

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixture(t *testing.T, now time.Time) string {
	t.Helper()
	value := 99.5
	cell := 995
	metrics := Metrics{
		Now:          Current{Status: "ok", Availability: &value, Window: "15m"},
		Availability: Availability{H24: &value}, Latency: Latency{P50: &value},
		SameModel: &value, Intel: &Intel{Full: 99, Swapped: 1},
		Cells: []*int{&cell, nil}, Merged: []json.RawMessage{},
	}
	data := Data{
		Schema: 2, GeneratedAt: now, DataThrough: now.Add(-time.Minute),
		CellSeconds: 1800, CellsStart: now.Add(-24 * time.Hour),
		Thresholds: Thresholds{Good: 99, Warn: 95, MinTurns: 20},
		Notes:      []json.RawMessage{},
		Cohorts: []Cohort{{ID: "free", State: "ok", Agents: []Agent{{
			ID: "claude-code", Name: "Claude", Summary: metrics,
			Reasons: []Reason{{Class: "throttle", Share: 77.9}, {Class: "outage", Share: 22}, {Class: "capacity", Share: .1}},
			Models:  []Model{{ID: "claude-model", Name: "Claude model", Metrics: metrics}},
		}}}},
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func responseWith(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestClientSchemaValidation(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	good := fixture(t, now)
	tests := []struct{ name, body, wantError string }{
		{"valid", good, ""},
		{"wrong version", strings.Replace(good, `"schema":2`, `"schema":1`, 1), "unsupported schema"},
		{"missing document", `{}`, "missing"},
		{"null document", `null`, "cannot be null"},
		{"invalid JSON", `not json`, "invalid schema"},
		{"trailing JSON", good + `{}`, "invalid schema"},
		{"bad date", strings.Replace(good, now.Format(time.RFC3339Nano), "yesterday", 1), "invalid schema"},
		{"missing status", strings.Replace(good, `"status":"ok",`, "", 1), "missing"},
		{"null status", strings.Replace(good, `"status":"ok"`, `"status":null`, 1), "cannot be null"},
		{"unknown status", strings.Replace(good, `"status":"ok"`, `"status":"healthy"`, 1), "invalid current status"},
		{"invalid window", strings.Replace(good, `"window":"15m"`, `"window":"24h"`, 1), "invalid current window"},
		{"availability out of range", strings.Replace(good, `"availability":99.5`, `"availability":100.1`, 1), "percentage out of range"},
		{"negative latency", strings.Replace(good, `"p50":99.5`, `"p50":-1`, 1), "latency"},
		{"cell out of range", strings.Replace(good, `[995,null]`, `[1001,null]`, 1), "cell must"},
		{"cell not integer", strings.Replace(good, `[995,null]`, `[99.5,null]`, 1), "invalid schema"},
		{"missing models", strings.Replace(good, `"models":`, `"notModels":`, 1), "models is missing"},
		{"no cohorts", strings.Replace(good, `"cohorts":`, `"notCohorts":`, 1), "cohorts is missing"},
		{"bad interval", strings.Replace(good, `"cellSeconds":1800`, `"cellSeconds":60`, 1), "cellSeconds"},
		{"bad reason", strings.Replace(good, `"class":"throttle"`, `"class":"unknown"`, 1), "reason class"},
		{"reason percentage out of range", strings.Replace(good, `"share":77.9`, `"share":100.1`, 1), "reason share"},
		{"null metrics", strings.Replace(good, `"now":{`, `"now":null,"ignoredNow":{`, 1), "cannot be null"},
		{"unknown extra field", strings.Replace(good, `"schema":2`, `"futureField":true,"schema":2`, 1), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return responseWith(200, tt.body), nil })})
			got := reader.Snapshot(t.Context())
			if tt.wantError != "" {
				if !strings.Contains(got.Error, tt.wantError) || got.Data != nil || !got.Stale || got.FetchedAt != nil {
					t.Fatalf("snapshot = %+v, expected %q and stale null data", got, tt.wantError)
				}
				return
			}
			if got.Stale || got.Error != "" || got.Data == nil || got.FetchedAt == nil {
				t.Fatalf("snapshot = %+v", got)
			}
			m := got.Data.Cohorts[0].Agents[0].Summary
			if *m.Cells[0] != 995 || m.Cells[1] != nil || *m.Now.Availability != 99.5 || m.Availability.D7 != nil || *m.Latency.P50 != 99.5 {
				t.Fatalf("metrics units or nulls changed: %+v", m)
			}
		})
	}
}

func TestClientRejectsHTTPFailuresAndOversize(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		response *http.Response
		want     string
	}{
		{"non200", responseWith(503, "private upstream detail"), "HTTP 503"},
		{"oversize streamed", responseWith(200, strings.Repeat(" ", int(maxResponseBytes)+1)), "exceeds 1 MiB"},
		{"oversize declared", &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, ContentLength: maxResponseBytes + 1, Body: io.NopCloser(strings.NewReader(""))}, "exceeds 1 MiB"},
		{"wrong content type", &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader("{}"))}, "content type"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reader := New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return tt.response, nil })})
			got := reader.Snapshot(t.Context())
			if !got.Stale || got.Data != nil || !strings.Contains(got.Error, tt.want) || strings.Contains(got.Error, "private") {
				t.Fatalf("snapshot = %+v", got)
			}
		})
	}
}

func TestClientEnforcesFixedURLNoCredentialsOrRedirects(t *testing.T) {
	t.Parallel()
	var forbiddenCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forbiddenCalls.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	for _, destination := range []string{target.URL + "/capture", "https://mirasim.ai/other", "https://evil.invalid/", "http://127.0.0.1/"} {
		t.Run(destination, func(t *testing.T) {
			var calls atomic.Int32
			jar, _ := cookiejar.New(nil)
			u, _ := url.Parse(SourceURL)
			jar.SetCookies(u, []*http.Cookie{{Name: "secret", Value: "caller-cookie"}})
			original := &http.Client{Jar: jar, Timeout: time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { t.Error("injected redirect callback used"); return nil }, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.String() != SourceURL || r.Host != "mirasim.ai" || r.Body != nil {
					t.Errorf("unexpected outbound request: %+v", r)
				}
				for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie", "X-Api-Key"} {
					if r.Header.Get(name) != "" {
						t.Errorf("credential forwarded: %s", name)
					}
				}
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) > requestTimeout {
					t.Errorf("request lacks eight second cap")
				}
				resp := responseWith(302, "")
				resp.Header.Set("Location", destination)
				return resp, nil
			})}
			reader := New(original)
			got := reader.Snapshot(t.Context())
			if !strings.Contains(got.Error, "HTTP 302") || !got.Stale || calls.Load() != 1 || forbiddenCalls.Load() != 0 {
				t.Fatalf("redirect escaped policy: %+v calls=%d", got, calls.Load())
			}
			if original.Jar == nil || original.Timeout != time.Minute {
				t.Fatal("constructor mutated injected client")
			}
		})
	}
}

func TestClientTimeoutAndCancellation(t *testing.T) {
	t.Parallel()
	reader := New(&http.Client{Timeout: 10 * time.Millisecond, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})})
	got := reader.Snapshot(t.Context())
	if !got.Stale || got.Data != nil || !strings.Contains(got.Error, "timed out") {
		t.Fatalf("timeout = %+v", got)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got = reader.Snapshot(ctx)
	if !got.Stale || !strings.Contains(got.Error, "canceled") {
		t.Fatalf("cancellation = %+v", got)
	}
}
