package qoder

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

var effortOrder = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

type modelSpec struct {
	Key           string
	Name          string
	Source        string
	Config        json.RawMessage
	Thinks        bool
	AlwaysThinks  bool
	Efforts       []string
	DefaultEffort string
	Windows       []int
	DefaultWindow int
	MaxInput      int
	Context       int
}

func fallbackModel(name string) modelSpec {
	config, _ := json.Marshal(map[string]string{"key": name})
	return modelSpec{Key: name, Config: config}
}

func parseListing(raw []byte) map[string]modelSpec {
	var listing struct {
		Chat []json.RawMessage `json:"chat"`
	}
	if json.Unmarshal(raw, &listing) != nil {
		return nil
	}
	out := map[string]modelSpec{}
	for _, item := range listing.Chat {
		spec := modelFromRaw(item)
		if spec.Key == "" || spec.Key == "auto" || spec.Key == "default" {
			continue
		}
		var enabled struct {
			Enable bool `json:"enable"`
		}
		if json.Unmarshal(item, &enabled) != nil || !enabled.Enable {
			continue
		}
		out[spec.Key] = spec
	}
	return out
}

func modelFromRaw(raw json.RawMessage) modelSpec {
	var header struct {
		Key            string `json:"key"`
		DisplayName    string `json:"display_name"`
		Source         string `json:"source"`
		IsReasoning    bool   `json:"is_reasoning"`
		MaxInputTokens int    `json:"max_input_tokens"`
	}
	_ = json.Unmarshal(raw, &header)
	windows, def := windowsOf(raw)
	spec := modelSpec{
		Key: header.Key, Name: strings.TrimSpace(header.DisplayName), Source: strings.TrimSpace(header.Source), Config: append(json.RawMessage(nil), raw...),
		Thinks: header.IsReasoning, Windows: windows, DefaultWindow: def, MaxInput: header.MaxInputTokens,
	}
	if len(windows) > 0 {
		spec.Context = windows[len(windows)-1]
	} else {
		spec.Context = header.MaxInputTokens
	}
	var thinking struct {
		Config *struct {
			Disabled json.RawMessage `json:"disabled"`
			Enabled  *struct {
				Efforts map[string]struct {
					IsDefault bool `json:"is_default"`
				} `json:"efforts"`
			} `json:"enabled"`
		} `json:"thinking_config"`
	}
	if json.Unmarshal(raw, &thinking) != nil || thinking.Config == nil {
		return spec
	}
	spec.Thinks = thinking.Config.Enabled != nil
	spec.AlwaysThinks = spec.Thinks && len(thinking.Config.Disabled) == 0
	spec.Efforts = nil
	if !spec.Thinks || thinking.Config.Enabled == nil {
		return spec
	}
	for name, effort := range thinking.Config.Enabled.Efforts {
		spec.Efforts = append(spec.Efforts, name)
		if effort.IsDefault {
			spec.DefaultEffort = name
		}
	}
	sort.Slice(spec.Efforts, func(i, j int) bool {
		if rank(spec.Efforts[i]) != rank(spec.Efforts[j]) {
			return rank(spec.Efforts[i]) < rank(spec.Efforts[j])
		}
		return spec.Efforts[i] < spec.Efforts[j]
	})
	return spec
}

func windowsOf(raw json.RawMessage) ([]int, int) {
	var decoded map[string]any
	if json.Unmarshal(raw, &decoded) != nil {
		return nil, 0
	}
	windows := []int{}
	def := 0
	if cc, ok := objectField(decoded, "context_config", "contextConfig"); ok {
		for _, entry := range cc {
			item, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			count := positiveInt(item["token_count"])
			if count == 0 {
				continue
			}
			if !containsInt(windows, count) {
				windows = append(windows, count)
			}
			if defaultFlag(item["is_default"]) {
				def = count
			}
		}
	}
	if len(windows) == 0 {
		for _, value := range arrayField(decoded, "available_context_windows", "availableContextWindows") {
			count := positiveInt(value)
			if count > 0 && !containsInt(windows, count) {
				windows = append(windows, count)
			}
		}
		def = positiveInt(firstField(decoded, "default_context_window", "defaultContextWindow"))
		if !containsInt(windows, def) {
			def = 0
		}
	}
	sort.Ints(windows)
	return windows, def
}

func objectField(decoded map[string]any, names ...string) (map[string]any, bool) {
	value := firstField(decoded, names...)
	item, ok := value.(map[string]any)
	return item, ok
}

func arrayField(decoded map[string]any, names ...string) []any {
	value, ok := firstField(decoded, names...).([]any)
	if !ok {
		return nil
	}
	return value
}

func firstField(decoded map[string]any, names ...string) any {
	for _, name := range names {
		if value, ok := decoded[name]; ok {
			return value
		}
	}
	return nil
}

func positiveInt(value any) int {
	switch typed := value.(type) {
	case float64:
		if typed > 0 && typed == float64(int(typed)) {
			return int(typed)
		}
	case json.Number:
		n, err := typed.Int64()
		if err == nil && n > 0 {
			return int(n)
		}
	case string:
		n, err := strconv.Atoi(typed)
		if err == nil && n > 0 {
			return n
		}
	}
	return 0
}

func defaultFlag(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case float64:
		return typed == 1
	case string:
		return typed == "1"
	default:
		return false
	}
}

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// modelChoices is what an operator picks. Qoder's list uses key for the
// request and display_name for the label; the label is the choice unless it
// is missing or already taken.
func modelChoices(listing map[string]modelSpec) []string {
	keys := make([]string, 0, len(listing))
	for key := range listing {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	seen := make(map[string]struct{}, len(keys))
	choices := make([]string, 0, len(keys))
	for _, key := range keys {
		label := strings.TrimSpace(listing[key].Name)
		if label == "" || label == "auto" || label == "default" {
			label = key
		}
		if _, taken := seen[label]; taken {
			label = key
		}
		if _, taken := seen[label]; taken {
			continue
		}
		seen[label] = struct{}{}
		choices = append(choices, label)
	}
	sort.Strings(choices)
	return choices
}

func specByName(listing map[string]modelSpec, name string) (modelSpec, bool) {
	if spec, ok := listing[name]; ok {
		return spec, true
	}
	var found modelSpec
	matched := false
	for _, spec := range listing {
		if spec.Name != name {
			continue
		}
		if !matched || spec.Key < found.Key {
			found = spec
			matched = true
		}
	}
	return found, matched
}

func rank(name string) int {
	for i, effort := range effortOrder {
		if effort == name {
			return i
		}
	}
	return len(effortOrder)
}

type listingEntry struct {
	at    time.Time
	byKey map[string]modelSpec
}
