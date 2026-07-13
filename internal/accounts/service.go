package accounts

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultRateLimitFallback = 60 * time.Second
	DefaultQuotaFallback     = 5 * time.Minute
)

type RotationStrategy string

const (
	RotationLeastUsed  RotationStrategy = "least_used"
	RotationRoundRobin RotationStrategy = "round_robin"
	RotationSticky     RotationStrategy = "sticky"
)

type Service struct {
	mu               sync.RWMutex
	store            Store
	records          map[string]*Record
	rotationStrategy RotationStrategy
	roundRobinIndex  int
	stickyAccountID  string
}

var accountIDSequence uint64

func NewService(accountsStore Store, defaultStrategy RotationStrategy) (*Service, error) {
	state, err := accountsStore.Load()
	if err != nil {
		return nil, err
	}

	svc := &Service{
		store:            accountsStore,
		records:          make(map[string]*Record),
		rotationStrategy: cmp.Or(state.RotationStrategy, defaultStrategy),
	}

	now := time.Now().UTC()
	needsPersist := false
	for _, stored := range state.Records {
		record := cloneRecord(stored)
		if svc.normalizeLoadedRecord(&record, now) {
			needsPersist = true
		}
		svc.records[record.ID] = &record
	}
	if needsPersist {
		if err := svc.persistLocked(); err != nil {
			return nil, err
		}
	}

	return svc, nil
}

func (s *Service) List() ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.refreshAllLocked(time.Now().UTC()); err != nil {
		return nil, err
	}

	items := make([]Record, 0, len(s.records))
	for _, record := range s.records {
		items = append(items, cloneRecord(record))
	}
	slices.SortFunc(items, func(a, b Record) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	return items, nil
}

func (s *Service) Get(id string) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.refreshAllLocked(time.Now().UTC()); err != nil {
		return Record{}, false, err
	}

	record, ok := s.records[id]
	if !ok {
		return Record{}, false, nil
	}
	return cloneRecord(record), true, nil
}

func (s *Service) EligibleNow(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if err := s.refreshAllLocked(now); err != nil {
		return false, err
	}

	record, ok := s.records[id]
	return ok && isEligible(record, now), nil
}

func (s *Service) UpsertFromToken(accountID string, token OAuthToken) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	metadata := metadataFromToken(token)
	for _, existing := range s.records {
		if sameAccount(existing, accountID, metadata.UserID) {
			existing.Token = token
			existing.Email = metadata.Email
			existing.PlanType = metadata.PlanType
			existing.UserID = metadata.UserID
			existing.Status = StatusActive
			existing.LastError = ""
			existing.CooldownUntil = nil
			existing.UpdatedAt = time.Now().UTC()
			if err := s.persistLocked(); err != nil {
				return Record{}, err
			}
			return cloneRecord(existing), nil
		}
	}

	now := time.Now().UTC()
	record := &Record{
		ID:        "acct_" + nextAccountID(),
		AccountID: accountID,
		UserID:    metadata.UserID,
		Email:     metadata.Email,
		PlanType:  metadata.PlanType,
		Status:    StatusActive,
		Token:     token,
		Cookies:   map[string]string{},
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.records[record.ID] = record
	if err := s.persistLocked(); err != nil {
		return Record{}, err
	}
	return cloneRecord(record), nil
}

func sameAccount(existing *Record, accountID, userID string) bool {
	if existing == nil || existing.AccountID != accountID {
		return false
	}
	existingUserID := strings.TrimSpace(existing.UserID)
	newUserID := strings.TrimSpace(userID)
	if existingUserID == "" || newUserID == "" {
		return true
	}
	return existingUserID == newUserID
}

func (s *Service) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.records[id]; !ok {
		return fmt.Errorf("account not found")
	}
	delete(s.records, id)
	if s.stickyAccountID == id {
		s.stickyAccountID = ""
	}
	return s.persistLocked()
}

func (s *Service) Patch(id string, label *string, status *Status) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.records[id]
	if !ok {
		return Record{}, fmt.Errorf("account not found")
	}
	if label != nil {
		record.Label = strings.TrimSpace(*label)
	}
	if status != nil {
		record.Status = *status
		switch *status {
		case StatusActive:
			record.CooldownUntil = nil
			record.LastError = ""
			record.CachedQuota = nil
		case StatusDisabled:
			record.CooldownUntil = nil
			record.LastError = ""
		}
	}
	record.UpdatedAt = time.Now().UTC()
	if err := s.persistLocked(); err != nil {
		return Record{}, err
	}
	return cloneRecord(record), nil
}

func (s *Service) ObserveQuota(id string, quota *QuotaSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.records[id]
	if !ok {
		return fmt.Errorf("account not found")
	}

	now := time.Now().UTC()
	if quota == nil {
		record.CachedQuota = nil
		record.UpdatedAt = now
		return s.persistLocked()
	}

	cloned := cloneQuotaSnapshot(quota)
	normalizeQuotaSnapshot(&cloned, now)
	record.CachedQuota = &cloned
	if plan := strings.TrimSpace(cloned.PlanType); plan != "" && plan != "unknown" {
		record.PlanType = plan
	}
	if !quotaBlocksGeneralRouting(record.CachedQuota, now) {
		record.CooldownUntil = nil
	}
	record.UpdatedAt = now
	return s.persistLocked()
}

func (s *Service) UpdateAuth(id, accountID string, token OAuthToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.records[id]
	if !ok {
		return fmt.Errorf("account not found")
	}

	metadata := metadataFromToken(token)
	if trimmed := strings.TrimSpace(accountID); trimmed != "" {
		record.AccountID = trimmed
	}
	record.Token = token
	if metadata.Email != "" {
		record.Email = metadata.Email
	}
	if metadata.PlanType != "" {
		record.PlanType = metadata.PlanType
	}
	if metadata.UserID != "" {
		record.UserID = metadata.UserID
	}
	if record.Status != StatusDisabled && record.Status != StatusBanned {
		record.Status = StatusActive
	}
	record.LastError = ""
	record.UpdatedAt = time.Now().UTC()
	return s.persistLocked()
}

func (s *Service) MarkError(id string, status Status, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.records[id]
	if !ok {
		return fmt.Errorf("account not found")
	}
	record.Status = status
	record.LastError = strings.TrimSpace(message)
	if status != StatusActive {
		record.CooldownUntil = nil
	}
	record.UpdatedAt = time.Now().UTC()
	return s.persistLocked()
}

func (s *Service) SetCooldown(id string, until *time.Time, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.records[id]
	if !ok {
		return fmt.Errorf("account not found")
	}
	if until != nil {
		value := until.UTC()
		until = &value
	}
	record.CooldownUntil = until
	record.LastError = strings.TrimSpace(message)
	record.UpdatedAt = time.Now().UTC()
	return s.persistLocked()
}

func (s *Service) NoteSuccess(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.records[id]; !ok {
		return
	}
	s.stickyAccountID = id
}

func (s *Service) Acquire(preferredID string) (Record, error) {
	return s.AcquireMatching(preferredID, nil)
}

func (s *Service) AcquireMatching(preferredID string, allow func(Record) bool) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if err := s.refreshAllLocked(now); err != nil {
		return Record{}, err
	}

	if preferredID != "" {
		if record, ok := s.records[preferredID]; ok && isEligible(record, now) {
			candidate := cloneRecord(record)
			if allow == nil || allow(candidate) {
				return candidate, nil
			}
		}
	}

	candidates := make([]*Record, 0, len(s.records))
	for _, record := range s.records {
		if isEligible(record, now) {
			candidate := cloneRecord(record)
			if allow != nil && !allow(candidate) {
				continue
			}
			recordCopy := candidate
			candidates = append(candidates, &recordCopy)
		}
	}
	if len(candidates) == 0 {
		return Record{}, fmt.Errorf("no active accounts")
	}

	switch s.rotationStrategy {
	case RotationRoundRobin:
		record := selectRoundRobin(candidates, &s.roundRobinIndex)
		return cloneRecord(record), nil
	case RotationSticky:
		if s.stickyAccountID != "" {
			for _, candidate := range candidates {
				if candidate.ID == s.stickyAccountID {
					return cloneRecord(candidate), nil
				}
			}
		}
	}
	record := selectLeastUsed(candidates, &s.roundRobinIndex)
	return cloneRecord(record), nil
}

func (s *Service) RotationStrategy() RotationStrategy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rotationStrategy
}

func (s *Service) SetRotationStrategy(strategy RotationStrategy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rotationStrategy = strategy
	return s.persistLocked()
}

func (s *Service) persistLocked() error {
	records := make([]*Record, 0, len(s.records))
	for _, record := range s.records {
		cloned := cloneRecord(record)
		records = append(records, &cloned)
	}
	return s.store.Save(State{
		Records:          records,
		RotationStrategy: s.rotationStrategy,
	})
}

func (s *Service) normalizeLoadedRecord(record *Record, now time.Time) bool {
	changed := false
	metadata := metadataFromToken(record.Token)
	if record.Status == "" {
		record.Status = StatusActive
		changed = true
	}
	switch record.Status {
	case StatusActive, StatusDisabled, StatusExpired, StatusBanned:
	default:
		record.Status = StatusActive
		changed = true
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
		changed = true
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = record.CreatedAt
		changed = true
	}
	if record.Cookies == nil {
		record.Cookies = map[string]string{}
		changed = true
	}
	if record.UserID == "" && strings.TrimSpace(metadata.UserID) != "" {
		record.UserID = strings.TrimSpace(metadata.UserID)
		changed = true
	}
	if record.Email == "" && strings.TrimSpace(metadata.Email) != "" {
		record.Email = strings.TrimSpace(metadata.Email)
		changed = true
	}
	if record.PlanType == "" && strings.TrimSpace(metadata.PlanType) != "" {
		record.PlanType = strings.TrimSpace(metadata.PlanType)
		changed = true
	}
	if record.CachedQuota != nil && normalizeQuotaSnapshot(record.CachedQuota, now) {
		changed = true
	}
	if clearExpiredCooldownLocked(record, now) {
		changed = true
	}
	return changed
}

func (s *Service) refreshAllLocked(now time.Time) error {
	changed := false
	for _, record := range s.records {
		recordChanged := false
		if record.CachedQuota != nil && normalizeQuotaSnapshot(record.CachedQuota, now) {
			recordChanged = true
		}
		if clearExpiredCooldownLocked(record, now) {
			recordChanged = true
		}
		if recordChanged {
			record.UpdatedAt = now
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.persistLocked()
}

func clearExpiredCooldownLocked(record *Record, now time.Time) bool {
	if record == nil || record.CooldownUntil == nil {
		return false
	}
	if record.CooldownUntil.After(now) {
		return false
	}
	record.CooldownUntil = nil
	return true
}

func nextAccountID() string {
	return fmt.Sprintf("%d_%08x", time.Now().UTC().UnixNano(), atomic.AddUint64(&accountIDSequence, 1))
}
