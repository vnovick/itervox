package orchestrator

import (
	"testing"

	"github.com/vnovick/itervox/internal/domain"
)

func sptr(s string) *string { return &s }

func TestSortForDispatchWithOutdegree_FlagOffMatchesPlainSort(t *testing.T) {
	a := domain.Issue{Identifier: "ENG-1"}
	b := domain.Issue{Identifier: "ENG-2"}
	off := SortForDispatchWithOutdegree([]domain.Issue{a, b}, false)
	if off[0].Identifier != "ENG-1" || off[1].Identifier != "ENG-2" {
		t.Errorf("flag-off should match SortForDispatch order; got %v %v", off[0].Identifier, off[1].Identifier)
	}
}

func TestSortForDispatchWithOutdegree_HighOutdegreeWins(t *testing.T) {
	// ENG-1 is blocked by nobody. ENG-2 blocks ENG-3 and ENG-4.
	// With prefer_high_outdegree=true, ENG-2 should sort before ENG-1.
	foundation := domain.Issue{Identifier: "ENG-FOUNDATION"}
	leaf1 := domain.Issue{Identifier: "ENG-LEAF1", BlockedBy: []domain.BlockerRef{{Identifier: sptr("ENG-FOUNDATION")}}}
	leaf2 := domain.Issue{Identifier: "ENG-LEAF2", BlockedBy: []domain.BlockerRef{{Identifier: sptr("ENG-FOUNDATION")}}}
	standalone := domain.Issue{Identifier: "ENG-STANDALONE"}

	got := SortForDispatchWithOutdegree([]domain.Issue{standalone, foundation, leaf1, leaf2}, true)
	if got[0].Identifier != "ENG-FOUNDATION" {
		t.Errorf("foundation (outdegree 2) should sort first; got %v", got[0].Identifier)
	}
}

func TestComputeBlockerOutdegree_CountsCorrectly(t *testing.T) {
	a := domain.Issue{Identifier: "ENG-A"}
	b := domain.Issue{Identifier: "ENG-B", BlockedBy: []domain.BlockerRef{{Identifier: sptr("ENG-A")}}}
	c := domain.Issue{Identifier: "ENG-C", BlockedBy: []domain.BlockerRef{{Identifier: sptr("ENG-A")}}}
	d := domain.Issue{Identifier: "ENG-D", BlockedBy: []domain.BlockerRef{{Identifier: sptr("ENG-B")}}}

	got := computeBlockerOutdegree([]domain.Issue{a, b, c, d})
	if got["ENG-A"] != 2 {
		t.Errorf("ENG-A outdegree = %d; want 2", got["ENG-A"])
	}
	if got["ENG-B"] != 1 {
		t.Errorf("ENG-B outdegree = %d; want 1", got["ENG-B"])
	}
	if got["ENG-D"] != 0 {
		t.Errorf("ENG-D outdegree = %d; want 0", got["ENG-D"])
	}
}
