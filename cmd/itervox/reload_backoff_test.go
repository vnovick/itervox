package main

import (
	"testing"
	"time"
)

func TestReloadBackoffSchedule(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 200 * time.Millisecond},
		{1, 400 * time.Millisecond},
		{2, 800 * time.Millisecond},
		{3, 1600 * time.Millisecond},
		{4, 3200 * time.Millisecond},
		{5, 6400 * time.Millisecond},
		{6, 12800 * time.Millisecond},
		{7, 25600 * time.Millisecond},
		{8, 30 * time.Second}, // capped
		{9, 30 * time.Second},
		{20, 30 * time.Second},
		{50, 30 * time.Second}, // saturated past shift-overflow guard
	}
	for _, tc := range cases {
		got := reloadBackoff(tc.attempt)
		if got != tc.want {
			t.Errorf("reloadBackoff(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestReloadBackoffNegativeAttemptClampsToBase(t *testing.T) {
	// Defensive: a negative attempt index should not panic or shift-overflow.
	got := reloadBackoff(-5)
	want := 200 * time.Millisecond
	if got != want {
		t.Errorf("reloadBackoff(-5) = %v, want %v", got, want)
	}
}

func TestReloadBackoffMonotonicallyIncreases(t *testing.T) {
	// Each step must be >= previous step until the cap is reached.
	prev := time.Duration(0)
	for i := range 30 {
		got := reloadBackoff(i)
		if got < prev {
			t.Errorf("reloadBackoff(%d)=%v decreased from previous %v", i, got, prev)
		}
		prev = got
	}
	if prev != reloadBackoffMax {
		t.Errorf("final attempt did not saturate at cap %v, got %v", reloadBackoffMax, prev)
	}
}
