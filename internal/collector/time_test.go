package collector

import (
	"math"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/runtime"
)

func TestTimeFromEpochSeconds(t *testing.T) {
	tests := []struct {
		name string
		in   cdp.TimeSinceEpoch
		want time.Time
	}{
		{"zero", 0, time.Time{}},
		{"nan", cdp.TimeSinceEpoch(math.NaN()), time.Time{}},
		{"pos_inf", cdp.TimeSinceEpoch(math.Inf(1)), time.Time{}},
		{"neg_inf", cdp.TimeSinceEpoch(math.Inf(-1)), time.Time{}},
		{"whole_seconds", cdp.TimeSinceEpoch(1700000000), time.Unix(1700000000, 0).UTC()},
		{"fractional_seconds", cdp.TimeSinceEpoch(1700000000.25), time.Unix(1700000000, 250_000_000).UTC()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := timeFromEpochSeconds(tc.in)
			if !got.Equal(tc.want) {
				t.Errorf("timeFromEpochSeconds(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestTimeFromRuntimeTimestamp(t *testing.T) {
	tests := []struct {
		name string
		in   runtime.Timestamp
		want time.Time
	}{
		{"zero", 0, time.Time{}},
		{"nan", runtime.Timestamp(math.NaN()), time.Time{}},
		{"inf", runtime.Timestamp(math.Inf(1)), time.Time{}},
		// runtime.Timestamp is in milliseconds since Unix epoch.
		{"milliseconds", runtime.Timestamp(1700000000125), time.Unix(1700000000, 125_000_000).UTC()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := timeFromRuntimeTimestamp(tc.in)
			if !got.Equal(tc.want) {
				t.Errorf("timeFromRuntimeTimestamp(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestEndTimeFromMonotonic(t *testing.T) {
	start := time.Unix(1700000000, 0).UTC()

	// Valid positive delta advances start by the monotonic difference.
	got := endTimeFromMonotonic(start, cdp.MonotonicTime(100.0), cdp.MonotonicTime(100.25))
	want := start.Add(250 * time.Millisecond)
	if !got.Equal(want) {
		t.Errorf("valid delta: got %v, want %v", got, want)
	}

	// Zero delta returns start unchanged.
	got = endTimeFromMonotonic(start, cdp.MonotonicTime(100.0), cdp.MonotonicTime(100.0))
	if !got.Equal(start) {
		t.Errorf("zero delta: got %v, want %v", got, start)
	}

	// Missing startMono, out-of-order endMono, or non-finite timestamps fall
	// back to wall-clock time.Now().
	before := time.Now()
	cases := map[string]time.Time{
		"missing_start": endTimeFromMonotonic(start, 0, cdp.MonotonicTime(10.0)),
		"inverted":      endTimeFromMonotonic(start, cdp.MonotonicTime(20.0), cdp.MonotonicTime(10.0)),
		"nan_end":       endTimeFromMonotonic(start, cdp.MonotonicTime(10.0), cdp.MonotonicTime(math.NaN())),
		"inf_end":       endTimeFromMonotonic(start, cdp.MonotonicTime(10.0), cdp.MonotonicTime(math.Inf(1))),
		"inf_start":     endTimeFromMonotonic(start, cdp.MonotonicTime(math.Inf(1)), cdp.MonotonicTime(math.Inf(1))),
	}
	after := time.Now()
	for label, ts := range cases {
		if ts.Before(before) || ts.After(after) {
			t.Errorf("%s: got %v, want within [%v, %v]", label, ts, before, after)
		}
	}
}
