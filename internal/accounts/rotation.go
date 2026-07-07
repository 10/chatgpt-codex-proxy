package accounts

import (
	"sort"
	"strings"
	"time"
)

func (s *Service) selectStickyLocked(candidates []*Record, now time.Time) *Record {
	if s.stickyAccountID == "" {
		return nil
	}
	for _, candidate := range candidates {
		if candidate.ID == s.stickyAccountID && isEligible(candidate, now) {
			return candidate
		}
	}
	return nil
}

func selectRoundRobin(candidates []*Record, index *int) *Record {
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	selected := candidates[*index%len(candidates)]
	*index = *index + 1
	return selected
}

func selectLeastUsed(candidates []*Record, index *int) *Record {
	withQuota := make([]*Record, 0, len(candidates))
	for _, candidate := range candidates {
		if hasUsableQuota(candidate) {
			withQuota = append(withQuota, candidate)
			continue
		}
	}

	if len(withQuota) == 0 {
		return selectRoundRobin(candidates, index)
	}

	sort.Slice(withQuota, func(i, j int) bool {
		if cmp := compareLeastUsedQuota(withQuota[i], withQuota[j]); cmp != 0 {
			return cmp < 0
		}
		return withQuota[i].ID < withQuota[j].ID
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
	if aHasReset && bHasReset && !aReset.Equal(bReset) {
		if aReset.Before(bReset) {
			return -1
		}
		return 1
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

func hasUsableQuota(record *Record) bool {
	return record != nil && record.CachedQuota != nil && record.CachedQuota.RateLimit.UsedPercent != nil
}

func normalizeQuotaSnapshot(snapshot *QuotaSnapshot, now time.Time) bool {
	if snapshot == nil {
		return false
	}
	changed := false
	if normalizeRateLimitWindow(&snapshot.RateLimit, now) {
		changed = true
	}
	if normalizeRateLimitWindow(snapshot.SecondaryRateLimit, now) {
		changed = true
	}
	if normalizeRateLimitWindow(snapshot.CodeReviewRateLimit, now) {
		changed = true
	}
	return changed
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
	if windowAvailabilityBlocked(&snapshot.RateLimit, now) {
		return true
	}
	if windowLimitActive(&snapshot.RateLimit, now) {
		return true
	}
	return windowLimitActive(snapshot.SecondaryRateLimit, now)
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
