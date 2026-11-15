package cart

import "testing"

func TestTotal(t *testing.T) {
	items := []Item{
		{Name: "coffee", Cents: 450, Quantity: 2},
		{Name: "bagel", Cents: 300, Quantity: 1},
	}
	if got, want := Total(items), 1200; got != want {
		t.Fatalf("Total = %d, want %d", got, want)
	}
}

func TestTotalEmpty(t *testing.T) {
	if got := Total(nil); got != 0 {
		t.Fatalf("Total(nil) = %d, want 0", got)
	}
}
