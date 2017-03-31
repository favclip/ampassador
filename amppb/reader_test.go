package amppb

import (
	"testing"

	"encoding/json"
)

func TestParseRules(t *testing.T) {
	rules, err := ParseRules("")
	if err != nil {
		t.Fatal(err)
	}

	if v := len(rules.Tags); v == 0 {
		t.Error("unexpected", v)
	}

	b, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}

	t.Log(string(b))
}
