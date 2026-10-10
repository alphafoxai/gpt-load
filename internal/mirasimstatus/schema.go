package mirasimstatus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

func decodeData(payload []byte) (*Data, error) {
	var data Data
	if err := json.Unmarshal(payload, &data); err != nil {
		return nil, fmt.Errorf("Mirasim status: invalid schema 2 JSON")
	}
	// A missing/null scalar must not silently become zero and look healthy.
	// Unknown fields are allowed so additive upstream changes remain compatible.
	if err := requireShape(payload, reflect.TypeOf(data), "data"); err != nil {
		return nil, fmt.Errorf("Mirasim status: invalid schema 2: %w", err)
	}
	if err := data.validate(); err != nil {
		return nil, fmt.Errorf("Mirasim status: invalid schema 2: %w", err)
	}
	return &data, nil
}

var rawMessageType = reflect.TypeOf(json.RawMessage{})

func requireShape(raw json.RawMessage, typ reflect.Type, path string) error {
	if typ == rawMessageType {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
			return nil
		}
		return fmt.Errorf("%s cannot be null", path)
	}
	if typ.Kind() == reflect.Pointer {
		return requireShape(raw, typ.Elem(), path)
	}
	switch typ.Kind() {
	case reflect.Struct:
		// time.Time is already validated by its JSON unmarshaler.
		if typ.PkgPath() == "time" {
			return nil
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return fmt.Errorf("%s must be an object", path)
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Anonymous {
				if err := requireShape(raw, field.Type, path); err != nil {
					return err
				}
				continue
			}
			key := strings.Split(field.Tag.Get("json"), ",")[0]
			value, ok := object[key]
			if !ok {
				return fmt.Errorf("%s.%s is missing", path, key)
			}
			if err := requireShape(value, field.Type, path+"."+key); err != nil {
				return err
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return fmt.Errorf("%s must be an array", path)
		}
		for _, value := range values {
			if err := requireShape(value, typ.Elem(), path+"[]"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (data *Data) validate() error {
	if data.Schema != 2 {
		return fmt.Errorf("unsupported schema (expected 2)")
	}
	if data.GeneratedAt.IsZero() || data.DataThrough.IsZero() || data.CellsStart.IsZero() {
		return fmt.Errorf("source timestamps must be present")
	}
	if data.CellSeconds != 1800 {
		return fmt.Errorf("cellSeconds must be 1800")
	}
	if data.CellsStart.After(data.DataThrough) || data.DataThrough.After(data.GeneratedAt) {
		return fmt.Errorf("source timestamps are out of order")
	}
	if data.Thresholds.Warn < 0 || data.Thresholds.Good > 100 || data.Thresholds.Warn > data.Thresholds.Good || data.Thresholds.MinTurns < 1 {
		return fmt.Errorf("invalid thresholds")
	}
	if data.Notes == nil || len(data.Cohorts) == 0 {
		return fmt.Errorf("notes and nonempty cohorts are required")
	}
	seen := make(map[string]bool)
	for _, cohort := range data.Cohorts {
		if (cohort.ID != "free" && cohort.ID != "paid" && cohort.ID != "cloud") || seen[cohort.ID] {
			return fmt.Errorf("invalid or duplicate cohort id")
		}
		seen[cohort.ID] = true
		if cohort.State == "" || cohort.Agents == nil {
			return fmt.Errorf("cohort state and agents are required")
		}
		agents := make(map[string]bool)
		for _, agent := range cohort.Agents {
			if agent.ID == "" || agent.Name == "" || agents[agent.ID] || agent.Models == nil {
				return fmt.Errorf("invalid agent")
			}
			agents[agent.ID] = true
			if err := agent.Summary.validate(); err != nil {
				return err
			}
			for _, reason := range agent.Reasons {
				if reason.Class != "throttle" && reason.Class != "outage" && reason.Class != "capacity" {
					return fmt.Errorf("invalid reason class")
				}
				if reason.Share < 0 || reason.Share > 100 {
					return fmt.Errorf("invalid reason share")
				}
			}
			models := make(map[string]bool)
			for _, model := range agent.Models {
				if model.ID == "" || model.Name == "" || models[model.ID] {
					return fmt.Errorf("invalid model")
				}
				models[model.ID] = true
				if err := model.Metrics.validate(); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (m Metrics) validate() error {
	if m.Now.Status != "ok" && m.Now.Status != "degraded" && m.Now.Status != "down" {
		return fmt.Errorf("invalid current status")
	}
	if m.Now.Window != "15m" && m.Now.Window != "1h" {
		return fmt.Errorf("invalid current window")
	}
	for _, value := range []*float64{m.Now.Availability, m.Availability.H24, m.Availability.D7, m.SameModel} {
		if value != nil && (*value < 0 || *value > 100) {
			return fmt.Errorf("percentage out of range")
		}
	}
	for _, value := range []*float64{m.Latency.P50, m.Latency.P95} {
		if value != nil && *value < 0 {
			return fmt.Errorf("latency must be nonnegative seconds")
		}
	}
	if m.Intel != nil && (m.Intel.Full < 0 || m.Intel.Swapped < 0 || m.Intel.Mismatched < 0 || m.Intel.Cut < 0) {
		return fmt.Errorf("intel values must be nonnegative")
	}
	if m.Cells == nil || m.Merged == nil {
		return fmt.Errorf("cells and merged arrays are required")
	}
	for _, cell := range m.Cells {
		if cell != nil && (*cell < 0 || *cell > 1000) {
			return fmt.Errorf("cell must be null or 0..1000")
		}
	}
	return nil
}
