package sem

import "testing"

// The envelope stamp is worth nothing unless the code path that builds a real
// response sets it: an unstamped response serializes as format_version 0, which
// is indistinguishable from a consumer's "field absent" default. Both
// content-bearing construction sites in SearchRepository must carry it.
func TestSearchRepositoryStampsFormatVersion(t *testing.T) {
	repo := cacheTestRepo(t)
	for _, name := range []string{"Alpha", "no-such-symbol-anywhere-in-this-repository"} {
		t.Run(name, func(t *testing.T) {
			response, err := SearchRepository(t.Context(), repo, "test", name, SearchOptions{
				Profile: ProfileFull, TopK: 5,
			})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if response.FormatVersion != SearchFormatVersion {
				t.Fatalf("format_version = %d, want %d (results=%d)",
					response.FormatVersion, SearchFormatVersion, len(response.Results))
			}
		})
	}
}
