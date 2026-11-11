package permanent_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/permanent"
)

var (
	errBase  = errors.New("base")
	errReset = errors.New("connection reset by peer")
)

// answers is an error type with a Permanent method that answers as told.
type answers struct {
	permanent bool
	inner     error
}

func (a *answers) Error() string { return "answers" }

func (a *answers) Permanent() bool { return a.permanent }

func (a *answers) Unwrap() error { return a.inner }

func TestMarkKeepsTheChain(t *testing.T) {
	t.Parallel()

	marked := permanent.Mark(errBase)

	if !errors.Is(marked, errBase) || !permanent.Is(marked) || marked.Error() != errBase.Error() {
		t.Errorf("Mark(%v) = %v, want a permanent error that wraps and reads as the original", errBase, marked)
	}

	if permanent.Mark(nil) != nil || permanent.Is(nil) || permanent.Is(errBase) {
		t.Error("nil stays nil, and an unmarked error is not permanent")
	}
}

// TestIsFollowsEveryBranch checks the marker is found through single wrapping,
// errors.Join, and a wrapper that answers false before the marker.
func TestIsFollowsEveryBranch(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		err  error
		want bool
	}{
		"wrapped with percent w": {fmt.Errorf("context: %w", permanent.Mark(errReset)), true},
		"joined, marker second":  {errors.Join(errBase, permanent.Mark(errReset)), true},
		"joined, marker first":   {errors.Join(permanent.Mark(errReset), errBase), true},
		"joined, no marker":      {errors.Join(errBase, errReset), false},
		"joined inside a wrap":   {fmt.Errorf("outer: %w", errors.Join(errBase, permanent.Mark(errReset))), true},
		"two percent w verbs":    {fmt.Errorf("%w and %w", errBase, permanent.Mark(errReset)), true},
		"false hides nothing":    {&answers{permanent: false, inner: permanent.Mark(errReset)}, true},
		"false in a join": {
			errors.Join(&answers{permanent: false}, permanent.Mark(errReset)), true,
		},
		"own marker true":    {&answers{permanent: true}, true},
		"own marker false":   {&answers{permanent: false}, false},
		"false then no mark": {&answers{permanent: false, inner: errBase}, false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := permanent.Is(tc.err); got != tc.want {
				t.Errorf("Is(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
