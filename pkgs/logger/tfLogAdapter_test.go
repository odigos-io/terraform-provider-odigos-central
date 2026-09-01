// Copyright (c) Odigos
// SPDX-License-Identifier: Apache-2.0

package logger

import "testing"

func TestArgsToMapPairsKeysAndValues(t *testing.T) {
	got := argsToMap("cluster", "prod", "count", 3)
	if got["cluster"] != "prod" {
		t.Errorf("cluster = %v, want prod", got["cluster"])
	}
	if got["count"] != 3 {
		t.Errorf("count = %v, want 3", got["count"])
	}
}

func TestArgsToMapSkipsNonStringKeys(t *testing.T) {
	got := argsToMap(1, "ignored", "kept", true)
	if _, ok := got["ignored"]; ok {
		t.Error("non-string key should not produce an entry")
	}
	if got["kept"] != true {
		t.Errorf("kept = %v, want true", got["kept"])
	}
}

func TestArgsToMapDropsTrailingUnpairedArg(t *testing.T) {
	got := argsToMap("only-key")
	if len(got) != 0 {
		t.Errorf("got %v, want an empty map for a dangling key", got)
	}
}
