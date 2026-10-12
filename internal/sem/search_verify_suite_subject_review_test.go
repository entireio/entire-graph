package sem

import "testing"

func TestSuiteSubjectCannotBeCapturedByUnrelatedManifest(t *testing.T) {
	for _, kind := range []string{"Cargo", "Maven", "Gradle", "Node", "Composer", "Ruby", "CMake"} {
		if searchVerifySuiteApplies(searchVerifySubject{sourcePath: "service.py"}, kind) {
			t.Errorf("Python source selected %s", kind)
		}
	}
	if !searchVerifySuiteApplies(searchVerifySubject{sourcePath: "service.py"}, "Pytest") {
		t.Fatal("Python not recognized")
	}
	if !searchVerifySuiteApplies(searchVerifySubject{sourcePath: "native.c", testPath: "wrapper.test.ts"}, "Node") {
		t.Fatal("covering test not recognized")
	}
}
