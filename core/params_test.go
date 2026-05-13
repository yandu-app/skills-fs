package core

import "testing"

func TestParamSet(t *testing.T) {
	var params ParamSet
	params.set("itemId", "p1")
	params.set("attId", "a9")
	if params.Len() != 2 {
		t.Fatalf("len = %d, want 2", params.Len())
	}
	if got, ok := params.Get("itemId"); !ok || got != "p1" {
		t.Fatalf("itemId = %q, %v", got, ok)
	}
	if _, ok := params.Get("missing"); ok {
		t.Fatal("missing key should not exist")
	}
	seen := map[string]string{}
	params.Each(func(k, v string) {
		seen[k] = v
	})
	if seen["attId"] != "a9" {
		t.Fatalf("Each did not visit attId: %#v", seen)
	}
	asMap := params.ToMap()
	if asMap["itemId"] != "p1" || asMap["attId"] != "a9" {
		t.Fatalf("ToMap mismatch: %#v", asMap)
	}
	params.Reset()
	if params.Len() != 0 || params.ToMap() != nil {
		t.Fatalf("reset failed: len=%d map=%#v", params.Len(), params.ToMap())
	}
}

func TestParamSetInlineLimit(t *testing.T) {
	var params ParamSet
	for i := 0; i < inlineParams+2; i++ {
		params.set("k", "v")
	}
	if params.Len() != inlineParams {
		t.Fatalf("len = %d, want %d", params.Len(), inlineParams)
	}
}
