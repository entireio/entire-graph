package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func containerMapSearchResponse() sem.SearchResponse {
	response := sectionedSearchResponse()
	response.ContainerMap = &sem.SearchContainerMap{
		FilePath: "src/routing/mod.rs", FileLines: 353, Kind: "struct", Name: "Router",
		StartLine: 49, EndLine: 353, AnchorRank: 1,
		Fields: []string{"inner:Arc"},
		Members: []sem.SearchContainerMember{
			{Name: "merge", StartLine: 10, EndLine: 14, Params: "(Router)", Hit: true},
			{Name: "Ops", Kind: "enum", StartLine: 213, EndLine: 282,
				Flags: []string{"NESTED", "PRIVATE"}, Members: 12},
		},
		MembersOmitted: 3,
	}
	return response
}

// TestWriteTextSearchLeadsWithContainerMap pins placement: the map is the first thing after any
// confidence notice, because a reader who has already decided to open the file will not scroll
// back for the reason not to.
func TestWriteTextSearchLeadsWithContainerMap(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := writeTextSearch(&buf, containerMapSearchResponse()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "CONTAINER MAP src/routing/mod.rs [353 lines]") {
		t.Fatalf("the map must lead the payload:\n%s", out)
	}
	mapAt := strings.Index(out, "CONTAINER MAP")
	firstResultAt := strings.Index(out, "1. src/routing/mod.rs:10-14")
	if firstResultAt < 0 || mapAt > firstResultAt {
		t.Fatalf("map must precede the ranking:\n%s", out)
	}
	for _, want := range []string{
		"fields: inner:Arc",
		"* 10-14 merge(Router)   <- rank 1",
		"213-282 enum Ops  NESTED,PRIVATE  +12 members",
		"+3 more members",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
}

// TestWriteTextSearchWithoutContainerMap pins that a payload with no map is byte-identical to the
// pre-map renderer: the block is additive, never a reformat.
func TestWriteTextSearchWithoutContainerMap(t *testing.T) {
	t.Parallel()
	var withMap, without bytes.Buffer
	if err := writeTextSearch(&withMap, containerMapSearchResponse()); err != nil {
		t.Fatal(err)
	}
	if err := writeTextSearch(&without, sectionedSearchResponse()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(without.String(), "CONTAINER MAP") {
		t.Fatalf("a response with no map must print none:\n%s", without.String())
	}
	if !strings.HasSuffix(withMap.String(), without.String()) {
		t.Fatalf("the map must be a prefix-only addition\nwith:\n%s\nwithout:\n%s", withMap.String(), without.String())
	}
}

// TestAgentSearchContainerMapDegradation pins the agent format's ladder: the map rides along when
// the cap allows, degrades to names and ranges, then drops — and it never costs the caller the
// top-ranked location, nor a byte over the cap.
func TestAgentSearchContainerMapDegradation(t *testing.T) {
	t.Parallel()
	response := containerMapSearchResponse()
	unbounded := renderAgentSearch(t, response, 0)
	if !strings.Contains(unbounded, "CONTAINER MAP") {
		t.Fatalf("unbounded agent output must carry the map:\n%s", unbounded)
	}
	full := len(sem.RenderSearchContainerMap(response.ContainerMap, false))
	compact := len(sem.RenderSearchContainerMap(response.ContainerMap, true))
	if compact >= full {
		t.Fatalf("compact map (%d) must be smaller than the full one (%d)", compact, full)
	}
	for _, budget := range []int{len(unbounded), 900, 600, 400, 240, 120, 60, 20} {
		out := renderAgentSearch(t, response, budget)
		if len(out) > budget {
			t.Fatalf("budget %d exceeded: %d bytes\n%s", budget, len(out), out)
		}
		if len(out) == 0 {
			t.Fatalf("budget %d produced no output at all", budget)
		}
		if strings.Contains(out, "CONTAINER MAP") && !strings.Contains(out, "src/routing/mod.rs") {
			t.Fatalf("budget %d kept the map but lost the ranking:\n%s", budget, out)
		}
	}
	// The floor: at a budget that cannot hold the map AND a result, the result wins.
	tight := renderAgentSearch(t, response, 200)
	if strings.Contains(tight, "CONTAINER MAP") && !strings.Contains(tight, ":10") {
		t.Fatalf("a tight budget must spend on the ranking before the map:\n%s", tight)
	}
}

// TestNdjsonSearchEmitsContainerMapRecord pins the streaming shape: one labelled record, before
// the results, so a consumer reading the stream in order is oriented before it sees a hit.
func TestNdjsonSearchEmitsContainerMapRecord(t *testing.T) {
	t.Parallel()
	response := containerMapSearchResponse()
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	if err := encoder.Encode(map[string]any{"record_type": "search_header"}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(struct {
		RecordType string `json:"record_type"`
		*sem.SearchContainerMap
	}{RecordType: "search_container_map", SearchContainerMap: response.ContainerMap}); err != nil {
		t.Fatal(err)
	}
	var record struct {
		RecordType string `json:"record_type"`
		FileLines  int    `json:"file_lines"`
		Members    []struct {
			Name      string `json:"name"`
			StartLine int    `json:"start_line"`
			EndLine   int    `json:"end_line"`
		} `json:"members"`
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 records, got %d", len(lines))
	}
	if err := json.Unmarshal([]byte(lines[1]), &record); err != nil {
		t.Fatal(err)
	}
	if record.RecordType != "search_container_map" {
		t.Fatalf("record_type = %q", record.RecordType)
	}
	if record.FileLines != 353 {
		t.Fatalf("file_lines = %d, want 353", record.FileLines)
	}
	if len(record.Members) != 2 || record.Members[1].Name != "Ops" ||
		record.Members[1].StartLine != 213 || record.Members[1].EndLine != 282 {
		t.Fatalf("members did not round-trip: %+v", record.Members)
	}
}

func renderAgentSearch(t *testing.T, response sem.SearchResponse, budget int) string {
	t.Helper()
	var buf bytes.Buffer
	if err := writeAgentSearch(&buf, response, budget); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
