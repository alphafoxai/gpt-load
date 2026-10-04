package qoder

import "testing"

func TestModelChoicesUseDisplayNames(t *testing.T) {
	listing := map[string]modelSpec{
		"qfmodel": {Key: "qfmodel", Name: "Qwen3.8-Flash"},
		"qmodel":  {Key: "qmodel", Name: "Qwen3.8-Max"},
		"plain":   {Key: "plain"},
	}
	got := modelChoices(listing)
	want := []string{"Qwen3.8-Flash", "Qwen3.8-Max", "plain"}
	if len(got) != len(want) {
		t.Fatalf("choices = %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("choices = %#v", got)
		}
	}
	spec, ok := specByName(listing, "Qwen3.8-Flash")
	if !ok || spec.Key != "qfmodel" {
		t.Fatalf("spec = %#v ok=%v", spec, ok)
	}
	if _, ok := specByName(listing, "qfmodel"); !ok {
		t.Fatal("key lookup missed")
	}
}

func TestModelChoicesKeepKeyWhenDisplayNameCollides(t *testing.T) {
	listing := map[string]modelSpec{
		"a": {Key: "a", Name: "Same"},
		"b": {Key: "b", Name: "Same"},
	}
	got := modelChoices(listing)
	if len(got) != 2 || got[0] != "Same" || got[1] != "b" {
		t.Fatalf("choices = %#v", got)
	}
	spec, ok := specByName(listing, "Same")
	if !ok || spec.Key != "a" {
		t.Fatalf("spec = %#v ok=%v", spec, ok)
	}
}
