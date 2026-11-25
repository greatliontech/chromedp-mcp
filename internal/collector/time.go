package collector

import (
	"math"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/runtime"
)

// timeFromEpochSeconds converts a CDP TimeSinceEpoch (UTC seconds since the
// Unix epoch) to a UTC time.Time. Zero, NaN, and Inf map to the zero time.
func timeFromEpochSeconds(ts cdp.TimeSinceEpoch) time.Time {
	v := float64(ts)
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return time.Time{}
	}
	sec, frac := math.Modf(v)
	return time.Unix(int64(sec), int64(math.Round(frac*1e9))).UTC()
}

// timeFromRuntimeTimestamp converts a CDP Runtime.Timestamp (milliseconds
// since the Unix epoch) to a UTC time.Time. Zero, NaN, and Inf map to the
// zero time.
func timeFromRuntimeTimestamp(ts runtime.Timestamp) time.Time {
	v := float64(ts)
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return time.Time{}
	}
	sec, frac := math.Modf(v / 1000.0)
	return time.Unix(int64(sec), int64(math.Round(frac*1e9))).UTC()
}

// endTimeFromMonotonic derives a wall-clock end time from a start wall-clock
// time and two CDP monotonic timestamps (seconds). Falls back to time.Now()
// when the monotonic endpoints are missing or out of order.
func endTimeFromMonotonic(start time.Time, startMono, endMono cdp.MonotonicTime) time.Time {
	if startMono > 0 && endMono >= startMono {
		delta := float64(endMono - startMono)
		if !math.IsNaN(delta) && !math.IsInf(delta, 0) {
			return start.Add(time.Duration(math.Round(delta * float64(time.Second))))
		}
	}
	return time.Now()
}
