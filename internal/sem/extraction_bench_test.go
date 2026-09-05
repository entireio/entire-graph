package sem

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Independent fixture from P1-A, scaled for phase characterization. It is not
// a substitute for the preregistered release corpus or an end-to-end gain test.
func BenchmarkExtractionPhases(b *testing.B) {
	repo := b.TempDir()
	for i := 0; i < 30; i++ {
		source := fmt.Sprintf("package fixture\nfunc Shared%d(v int) int { return v + 1 }\nfunc Caller%d() int { return Shared%d(2) }\n", i, i, i)
		if err := os.WriteFile(filepath.Join(repo, fmt.Sprintf("f%d.go", i)), []byte(source), 0600); err != nil {
			b.Fatal(err)
		}
	}
	for _, profile := range []Profile{ProfileSyntaxOnly, ProfileFast, ProfileFull} {
		b.Run(string(profile), func(b *testing.B) {
			phases := map[BuildPhase]time.Duration{}
			var serialization time.Duration
			var outputBytes int
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				current := map[BuildPhase]time.Duration{}
				err := StreamSnapshot(b.Context(), repo, "characterization", ProviderSnapshotOptions{Worktree: true, Profile: profile, Progress: func(event ProgressEvent) { current[event.Phase] = event.PhaseElapsed }}, func(record any) error {
					start := time.Now()
					encoded, err := json.Marshal(record)
					serialization += time.Since(start)
					outputBytes += len(encoded) + 1
					return err
				})
				if err != nil {
					b.Fatal(err)
				}
				for phase, elapsed := range current {
					phases[phase] += elapsed
				}
			}
			b.StopTimer()
			for phase, elapsed := range phases {
				b.ReportMetric(float64(elapsed.Nanoseconds())/float64(b.N), string(phase)+"-ns/op")
			}
			b.ReportMetric(float64(serialization.Nanoseconds())/float64(b.N), "serialization-ns/op")
			b.ReportMetric(float64(outputBytes)/float64(b.N), "output-bytes/op")
		})
	}
}
