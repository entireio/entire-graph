package sem

import "testing"

func TestPytestConfigRequiresRealSectionForNarrowVerification(t *testing.T) {
	for _, tc := range []struct {
		file, content string
		want          bool
	}{{"setup.cfg", "[metadata]\ndescription=pytest helper", false}, {"tox.ini", "# [pytest]", false}, {"pytest.ini", "[pytest]", true}, {"pyproject.toml", "[tool.pytest.ini_options]", true}, {"pyproject.toml", "[tool.pytest]", true}} {
		if got := searchVerifyIsPytestConfig(tc.file, tc.content); got != tc.want {
			t.Errorf("%s: %v", tc.file, got)
		}
	}
}
