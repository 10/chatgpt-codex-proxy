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

func compareLeastUsedQuota(a, b *Record) int {
	aQuota := a.CachedQuota
	bQuota := b.CachedQuota

	aPrimary := primaryPercent(aQuota)
	bPrimary := primaryPercent(bQuota)
	switch {
	case aPrimary < bPrimary:
		return -1
	case aPrimary > bPrimary:
		return 1
	}

	aSecondary, aHasSecondary := secondaryPercent(aQuota)
	bSecondary, bHasSecondary := secondaryPercent(bQuota)
	if aHasSecondary && bHasSecondary {
		switch {
		case aSecondary < bSecondary:
			return -1
		case aSecondary > bSecondary:
			return 1
		}
	}

	aReset, aHasReset := primaryReset(aQuota)
	bReset, bHasReset := primaryReset(bQuota)
	if aHasReset && bHasReset {
		return aReset.Compare(bReset)
	}

	return 0
}

func primaryPercent(snapshot *QuotaSnapshot) float64 {
	if snapshot == nil || snapshot.RateLimit.UsedPercent == nil {
		return 0
	}
	return *snapshot.RateLimit.UsedPercent
}

func secondaryPercent(snapshot *QuotaSnapshot) (float64, bool) {
	if snapshot == nil || snapshot.SecondaryRateLimit == nil || snapshot.SecondaryRateLimit.UsedPercent == nil {
		return 0, false
	}
	return *snapshot.SecondaryRateLimit.UsedPercent, true
}

func primaryReset(snapshot *QuotaSnapshot) (time.Time, bool) {
	if snapshot == nil || snapshot.RateLimit.ResetAt == nil {
		return time.Time{}, false
	}
	return snapshot.RateLimit.ResetAt.UTC(), true
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
	window.Allowed = true
	window.LimitReached = false
	window.UsedPercent = nil
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
