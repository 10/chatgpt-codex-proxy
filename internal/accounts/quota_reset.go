package accounts

import "time"

func PrimaryRateLimitReset(snapshot *QuotaSnapshot, now time.Time) *time.Time {
	if snapshot == nil {
		return nil
	}
	return activeWindowResetWindow(&snapshot.RateLimit, now)
}

func QuotaReset(snapshot *QuotaSnapshot, now time.Time) *time.Time {
	if snapshot == nil {
		return nil
	}
	if reset := activeWindowResetWindow(&snapshot.RateLimit, now); reset != nil {
		return reset
	}
	return activeWindowResetWindow(snapshot.SecondaryRateLimit, now)
}

func activeWindowResetWindow(window *RateLimitWindow, now time.Time) *time.Time {
	if window == nil || window.ResetAt == nil || !window.ResetAt.After(now) {
		return nil
	}
	if window.Allowed && !window.LimitReached {
		return nil
	}
	ts := window.ResetAt.UTC()
	return &ts
}
