package sem

import (
	"strings"
	"testing"
)

func TestParserIdentityRevisionsSnapshotAndCaches(t *testing.T) {
	header := leanHeader(sourceContext{}, "same-release", profileSpec{})
	if header.IdentityRevision != "js-ts-callable-scope-2" {
		t.Fatalf("identity=%q", header.IdentityRevision)
	}
	if !strings.HasSuffix(searchSnapshotCacheVersion, "-"+header.IdentityRevision) || !strings.HasSuffix(providerRecordsCacheVersion, "-"+header.IdentityRevision) {
		t.Fatal("parser identity does not invalidate cached snapshots")
	}
}

func TestDefaultExportIdentityCorrectionIsRevisioned(t *testing.T) {
	entities := javascriptDefaultExportEntities("helper.js", "export default classifier => classifier()\n")
	symbols := entitySymbols("local/example", "helper.js", "JavaScript", entities)
	if len(symbols) != 1 || symbols[0].ID != "local/example:JavaScript:helper.js:function:helper" {
		t.Fatalf("corrected default export symbols = %+v", symbols)
	}
	if IdentityRevision == "js-ts-callable-scope-1" {
		t.Fatal("default export identity correction must invalidate the previous parser revision")
	}
}
