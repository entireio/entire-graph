package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGraphHealthConsumerCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, status, trust string
		files, flagged      int
	}{
		{"healthy", "ok", "trusted", 20, 0}, {"below", "ok", "trusted", 21, 1}, {"boundary", "degraded", "partial", 20, 1}, {"unsafe", "unsafe", "low", 3, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(os.Getenv("GRAPH_HEALTH_FIXTURES"), tc.name+".ndjson"))
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			res, err := scanSemanticStream(bytes.NewReader(data), &out, semanticStreamScanConfig{})
			if err != nil {
				t.Fatal(err)
			}
			if res.summary == nil {
				t.Fatal("summary lost")
			}
			header := res.header
			mergeSemanticSummary(&header, res.summary)
			if header.SchemaVersion != "1.3" {
				t.Fatalf("schema=%s", header.SchemaVersion)
			}
			if err := validateSemanticSchema(header.SchemaVersion); err != nil {
				t.Fatal(err)
			}
			level := completenessLevelFromStats(header.Stats)
			if level != tc.status || trustForCompleteness(level) != tc.trust {
				t.Fatalf("status/trust=%s/%s", level, trustForCompleteness(level))
			}
			var comp struct {
				Health struct {
					SourceFiles  int    `json:"source_files"`
					FlaggedFiles int    `json:"flagged_files"`
					Status       string `json:"status"`
				} `json:"health"`
			}
			if err := json.Unmarshal(header.Completeness, &comp); err != nil {
				t.Fatal(err)
			}
			if comp.Health.SourceFiles != tc.files || comp.Health.FlaggedFiles != tc.flagged || comp.Health.Status != tc.status {
				t.Fatalf("health lost: %+v", comp)
			}
			if len(header.PartialFailures) != tc.flagged {
				t.Fatalf("diagnostics=%d", len(header.PartialFailures))
			}
			if tc.flagged > 0 && (header.PartialFailures[0].Code != "E_PARSE_ERROR" || header.PartialFailures[0].Detail == "") {
				t.Fatal("diagnostic code/detail lost")
			}
			var again bytes.Buffer
			replay, err := scanSemanticStream(bytes.NewReader(out.Bytes()), &again, semanticStreamScanConfig{})
			if err != nil {
				t.Fatal(err)
			}
			if replay.summary == nil || !bytes.Equal(replay.summary.Completeness, header.Completeness) {
				t.Fatal("persisted health lost")
			}
			t.Logf("schema 1.3 accepted; %s/%s; %d/%d flagged; diagnostics and health retained", level, tc.trust, tc.flagged, tc.files)
		})
	}
	if validateSemanticSchema("2.0") == nil {
		t.Fatal("unsupported major accepted")
	}
}
