package agentsetup

import (
	"sort"
	"strings"
	"testing"
)

// memoriesFirstNormal and memoriesFirstStrict are the COR-2308 wording Brain's
// guides use, verbatim (entire-brain PR 624, now on Brain main). The two modes
// use different sentences for the same rule. Brain's compareGuideText compares
// the rendered guides BYTE FOR BYTE, so holding a substring such as "precedent
// section" is not enough: reflow a line break, swap the variants, or drop the
// history-search escalation and Graph drifts in silence, with the failure
// surfacing in Brain's CI against Brain's own change.
//
// Without the wording an agent treats the brief as if it had searched
// transcript history, or searches history by default instead of following
// precedent anchors and escalating only when memories miss.
const memoriesFirstNormal = "The brief's precedent section lists each surfaced memory's source sessions.\n" +
	"Follow those anchors when you need the detail behind a memory; the brief does\n" +
	"not search transcript history. Search transcripts only as an explicit\n" +
	"escalation when memories and their anchors miss:\n\n" +
	"    entire brain query --source history \"<question>\" --json\n"

const memoriesFirstStrict = "Memories are the default retrieval surface. The brief's precedent section lists\n" +
	"each surfaced memory's source sessions (session, transcript, commit,\n" +
	"checkpoint); follow those anchors when you need the detail behind a memory.\n" +
	"The brief does not search transcript history. Search transcripts only as an\n" +
	"explicit escalation, when memories and their anchors miss:\n\n" +
	"    entire brain query --source history \"<question>\" --json\n"

func TestBrainGuidesStateTheCOR2308MemoriesFirstWordingVerbatim(t *testing.T) {
	var checked []string
	for name, guide := range AllGuides() {
		if !strings.Contains(name, "brain") {
			if strings.Contains(guide, "precedent section") {
				t.Errorf("%s: a Graph-only guide mentions the brief's precedent section", name)
			}
			continue
		}
		checked = append(checked, name)
		recall := strings.Index(guide, `entire brain recall "<question>" --json`)
		if recall < 0 {
			t.Errorf("%s: a Brain-enabled guide no longer shows recall", name)
			continue
		}
		want := memoriesFirstNormal
		other := memoriesFirstStrict
		if strings.HasPrefix(name, "strict:") {
			want, other = memoriesFirstStrict, memoriesFirstNormal
		}
		got := strings.Index(guide, want)
		if got < 0 {
			t.Errorf("%s: guide does not state the COR-2308 memories-first wording verbatim; want:\n%s", name, want)
			continue
		}
		if got < recall {
			t.Errorf("%s: the memories-first wording sits above the recall command it follows", name)
		}
		if strings.Contains(guide, other) {
			t.Errorf("%s: guide mixes the normal and strict COR-2308 variants", name)
		}
	}
	sort.Strings(checked)
	if len(checked) != brainEnabledGuides {
		t.Errorf("checked %d Brain-enabled guides %v, want %d; AllGuides no longer renders what this test believes it does",
			len(checked), checked, brainEnabledGuides)
	}
}
