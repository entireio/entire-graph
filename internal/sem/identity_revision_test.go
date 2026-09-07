package sem

import (
	"strings"
	"testing"
)

func TestParserIdentityRevisionsSnapshotAndCaches(t *testing.T) {
	header := leanHeader(sourceContext{}, "same-release", profileSpec{})
	if header.IdentityRevision != "js-ts-callable-scope-1" {
		t.Fatalf("identity=%q", header.IdentityRevision)
	}
	if !strings.HasSuffix(searchSnapshotCacheVersion, "-"+header.IdentityRevision) || !strings.HasSuffix(providerRecordsCacheVersion, "-"+header.IdentityRevision) {
		t.Fatal("parser identity does not invalidate cached snapshots")
	}
}
