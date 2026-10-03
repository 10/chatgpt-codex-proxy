package accounts

import (
	"cmp"
	"slices"
	"strings"
	"time"
)

func selectRoundRobin(candidates []*Record, index *int) *Record {
	slices.SortFunc(candidates, func(a, b *Record) int { return strings.Compare(a.ID, b.ID) })
	selected := candidates[*index%len(candidates)]
	*index = *index + 1
	return selected
}

func selectLeastUsed(candidates []*Record, index *int) *Record {
	withQuota := make([]*Record, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate != nil && candidate.CachedQuota != nil && candidate.CachedQuota.RateLimit.UsedPercent != nil {
			withQuota = append(withQuota, candidate)
		}
	}

	if len(withQuota) == 0 {
		return selectRoundRobin(candidates, index)
	}

	slices.SortFunc(withQuota, func(a, b *Record) int {
		return cmp.Or(compareLeastUsedQuota(a, b), strings.Compare(a.ID, b.ID))
	})

	tiedCount := 1
	for tiedCount < len(withQuota) && compareLeastUsedQuota(withQuota[0], withQuota[tiedCount]) == 0 {
		tiedCount++
	}
	selected := withQuota[*index%tiedCount]
	*index = *index + 1
	return selected
}

// compareLeastUsedQuota ranks accounts by the limit they are closest to
// hitting. Windows are ordered by usage rather than by the primary/secondary
// slot, because upstream reports a 7-day window as primary for some plans.
func compareLeastUsedQuota(a, b *Record) int {
	aWindows := windowsByUsage(a.CachedQuota)
	bWindows := windowsByUsage(b.CachedQuota)
	for i := range min(len(aWindows), len(bWindows)) {
		if order := cmp.Compare(*aWindows[i].UsedPercent, *bWindows[i].UsedPercent); order != 0 {
			return order
		}
	}
	if aWindows[0].ResetAt != nil && bWindows[0].ResetAt != nil {
		return aWindows[0].ResetAt.Compare(*bWindows[0].ResetAt)
	}
	return 0
}

func windowsByUsage(snapshot *QuotaSnapshot) []*RateLimitWindow {
	windows := make([]*RateLimitWindow, 0, 2)
	for _, window := range []*RateLimitWindow{&snapshot.RateLimit, snapshot.SecondaryRateLimit} {
		if window != nil && window.UsedPercent != nil {
			windows = append(windows, window)
		}
	}
	slices.SortStableFunc(windows, func(a, b *RateLimitWindow) int {
		return cmp.Compare(*b.UsedPercent, *a.UsedPercent)
	})
	return windows
}

func normalizeQuotaSnapshot(snapshot *QuotaSnapshot, now time.Time) bool {
	if snapshot == nil {
		return false
	}
	primaryChanged := normalizeRateLimitWindow(&snapshot.RateLimit, now)
	secondaryChanged := normalizeRateLimitWindow(snapshot.SecondaryRateLimit, now)
	codeReviewChanged := normalizeRateLimitWindow(snapshot.CodeReviewRateLimit, now)
	return primaryChanged || secondaryChanged || codeReviewChanged
}

func normalizeRateLimitWindow(window *RateLimitWindow, now time.Time) bool {
	if window == nil || window.ResetAt == nil || window.ResetAt.After(now) {
		return false
	}
	// A window that has reset starts again at zero usage.
	zero := 0.0
	window.Allowed = true
	window.LimitReached = false
	window.UsedPercent = &zero
	window.ResetAt = nil
	return true
}

func quotaBlocksGeneralRouting(snapshot *QuotaSnapshot, now time.Time) bool {
	if snapshot == nil {
		return false
	}
	return windowAvailabilityBlocked(&snapshot.RateLimit, now) ||
		windowLimitActive(&snapshot.RateLimit, now) ||
		windowLimitActive(snapshot.SecondaryRateLimit, now)
}

func windowAvailabilityBlocked(window *RateLimitWindow, now time.Time) bool {
	if window == nil || window.Allowed {
		return false
	}
	if window.ResetAt == nil {
		return true
	}
	return window.ResetAt.After(now)
}

func windowLimitActive(window *RateLimitWindow, now time.Time) bool {
	if window == nil || !window.LimitReached {
		return false
	}
	if window.ResetAt == nil {
		return true
	}
	return window.ResetAt.After(now)
}

func isEligible(record *Record, now time.Time) bool {
	if record == nil || record.Status != StatusActive {
		return false
	}
	if strings.TrimSpace(record.Token.AccessToken) == "" {
		return false
	}
	if record.CooldownUntil != nil && record.CooldownUntil.After(now) {
		return false
	}
	return !quotaBlocksGeneralRouting(record.CachedQuota, now)
}
