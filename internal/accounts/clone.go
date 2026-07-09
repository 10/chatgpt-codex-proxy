package accounts

import (
	"maps"
	"time"
)

func cloneRecord(record *Record) Record {
	cloned := *record
	cloned.Cookies = maps.Clone(record.Cookies)
	if cloned.Cookies == nil {
		cloned.Cookies = map[string]string{}
	}
	cloned.CooldownUntil = cloneTime(record.CooldownUntil)
	if record.CachedQuota != nil {
		quota := cloneQuotaSnapshot(record.CachedQuota)
		cloned.CachedQuota = &quota
	}
	return cloned
}

func cloneQuotaSnapshot(snapshot *QuotaSnapshot) QuotaSnapshot {
	cloned := *snapshot
	cloned.RateLimit = cloneRateLimitWindow(&snapshot.RateLimit)
	cloned.SecondaryRateLimit = cloneRateLimitWindowPtr(snapshot.SecondaryRateLimit)
	cloned.CodeReviewRateLimit = cloneRateLimitWindowPtr(snapshot.CodeReviewRateLimit)
	if snapshot.Credits != nil {
		credits := *snapshot.Credits
		if credits.Balance != nil {
			value := *credits.Balance
			credits.Balance = &value
		}
		cloned.Credits = &credits
	}
	return cloned
}

func cloneRateLimitWindowPtr(window *RateLimitWindow) *RateLimitWindow {
	if window == nil {
		return nil
	}
	cloned := cloneRateLimitWindow(window)
	return &cloned
}

func cloneRateLimitWindow(window *RateLimitWindow) RateLimitWindow {
	cloned := *window
	if window.UsedPercent != nil {
		value := *window.UsedPercent
		cloned.UsedPercent = &value
	}
	if window.ResetAt != nil {
		ts := window.ResetAt.UTC()
		cloned.ResetAt = &ts
	}
	if window.LimitWindowSeconds != nil {
		value := *window.LimitWindowSeconds
		cloned.LimitWindowSeconds = &value
	}
	return cloned
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	ts := value.UTC()
	return &ts
}
