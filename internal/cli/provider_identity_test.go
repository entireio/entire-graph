package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestVersionAdvertisesParserIdentity(t *testing.T) {
	var out bytes.Buffer
	if err := Run(context.Background(), Options{Stdout: &out, Version: "same-release"}, []string{"version", "--json"}); err != nil {
		t.Fatal(err)
	}
	var info map[string]string
	if err := json.Unmarshal(out.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info["identity_revision"] != "js-ts-callable-scope-1" {
		t.Fatalf("identity=%v", info)
	}
}
