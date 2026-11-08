package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// `version --json` must advertise the SAME parser identity the provider stamps
// into snapshots, because that is how a consumer decides whether its persisted
// symbol IDs are still valid without building a snapshot first.
//
// This compares against sem.IdentityRevision rather than a literal so the
// revision has one home. The literal itself is pinned in
// sem.TestParserIdentityRevisionsSnapshotAndCaches, together with the cache
// namespaces it must propagate into; duplicating it here only created a second
// place to forget.
func TestVersionAdvertisesParserIdentity(t *testing.T) {
	var out bytes.Buffer
	if err := Run(context.Background(), Options{Stdout: &out, Version: "same-release"}, []string{"version", "--json"}); err != nil {
		t.Fatal(err)
	}
	var info map[string]string
	if err := json.Unmarshal(out.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	// Guard against the vacuous pass: an absent field unmarshals to "", which
	// would match an empty constant.
	if sem.IdentityRevision == "" {
		t.Fatal("sem.IdentityRevision is empty; snapshots would advertise no parser identity")
	}
	if info["identity_revision"] != sem.IdentityRevision {
		t.Fatalf("version --json advertises identity_revision=%q, want %q; full output = %v",
			info["identity_revision"], sem.IdentityRevision, info)
	}
}
