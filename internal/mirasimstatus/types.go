// Package mirasimstatus reads the fixed, unauthenticated Mirasim public status feed.
// It is independent of local credentials, routing, and health-check state.
package mirasimstatus

import (
	"context"
	"encoding/json"
	"time"
)

const SourceURL = "https://mirasim.ai/api/status"

// Reader is the control-plane view of the process-local public status cache.
// Upstream failures are represented in the snapshot, not as a healthy empty feed.
type Reader interface {
	Snapshot(context.Context) Snapshot
}

// Snapshot distinguishes the last check from the last successful fetch.
// Data and FetchedAt are explicitly null until the first successful fetch.
type Snapshot struct {
	SourceURL string     `json:"source_url"`
	CheckedAt time.Time  `json:"checked_at"`
	FetchedAt *time.Time `json:"fetched_at"`
	Stale     bool       `json:"stale"`
	Error     string     `json:"error,omitempty"`
	Data      *Data      `json:"data"`
}

// Data preserves schema 2's units: availability is a percentage, cells are
// thousandths (0..1000), and latency is measured in seconds. Null is no data.
type Data struct {
	Schema      int               `json:"schema"`
	GeneratedAt time.Time         `json:"generatedAt"`
	DataThrough time.Time         `json:"dataThrough"`
	CellSeconds int               `json:"cellSeconds"`
	CellsStart  time.Time         `json:"cellsStart"`
	Thresholds  Thresholds        `json:"thresholds"`
	Notes       []json.RawMessage `json:"notes"`
	Cohorts     []Cohort          `json:"cohorts"`
}

type Thresholds struct {
	Good     float64 `json:"good"`
	Warn     float64 `json:"warn"`
	MinTurns int     `json:"minTurns"`
}

type Cohort struct {
	ID     string  `json:"id"`
	State  string  `json:"state"`
	Agents []Agent `json:"agents"`
}

type Agent struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Summary Metrics  `json:"summary"`
	Reasons []Reason `json:"reasons"`
	Models  []Model  `json:"models"`
}

type Model struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Metrics
}

type Metrics struct {
	Now          Current           `json:"now"`
	Availability Availability      `json:"availability"`
	Latency      Latency           `json:"latency"`
	SameModel    *float64          `json:"sameModel"`
	Intel        *Intel            `json:"intel"`
	Cells        []*int            `json:"cells"`
	Merged       []json.RawMessage `json:"merged"`
}

type Current struct {
	Status       string   `json:"status"`
	Availability *float64 `json:"availability"`
	Window       string   `json:"window"`
}

type Availability struct {
	H24 *float64 `json:"h24"`
	D7  *float64 `json:"d7"`
}

type Latency struct {
	P50 *float64 `json:"p50"`
	P95 *float64 `json:"p95"`
}

type Intel struct {
	Full       float64 `json:"full"`
	Swapped    float64 `json:"swapped"`
	Mismatched float64 `json:"mismatched"`
	Cut        float64 `json:"cut"`
}

type Reason struct {
	Class string  `json:"class"`
	Share float64 `json:"share"`
}
