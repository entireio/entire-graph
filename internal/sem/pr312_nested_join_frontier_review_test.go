package sem

import "testing"

func TestReview312NestedJoinFrontierBoundary(t *testing.T) {
	cases := []struct {
		name  string
		depth int
	}{
		{name: "depth-16-ordinary-retains-route", depth: 16},
		{name: "depth-17-ordinary-retains-route", depth: 17},
		{name: "depth-24-ordinary-retains-route", depth: 24},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			review312AssertJoinSummaryRoutes(t, review312NestedJoinSummarySource(test.depth, false), true)
		})
	}
	t.Run("depth-17-rewrite-still-omits-route", func(t *testing.T) {
		review312AssertJoinSummaryRoutes(t, review312NestedJoinSummarySource(17, true), false)
	})
}
