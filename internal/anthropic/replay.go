package anthropic

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/turn"
)

const (
	defaultReplayMaxEntries = 10240
	defaultReplayMaxBytes   = 64 << 20
	replayEvictBatchSize    = 128
)

var claudeCodeSessionSuffix = regexp.MustCompile(`_session_([A-Za-z0-9-]+)$`)

type ReplayMatch struct {
	Applied   bool
	AccountID string
}

type replayRecord struct {
	AccountID string
	Blocks    Content
	ExpiresAt time.Time
	Size      int
}

type ReplayManager struct {
	ttl        time.Duration
	maxEntries int
	maxBytes   int
	bytes      int
	mu         sync.RWMutex
	records    map[string]replayRecord
	now        func() time.Time
}

func NewReplayManager(ttl time.Duration) *ReplayManager {
	return &ReplayManager{
		ttl:        ttl,
		maxEntries: defaultReplayMaxEntries,
		maxBytes:   defaultReplayMaxBytes,
		records:    make(map[string]replayRecord),
		now:        time.Now,
	}
}

func SessionID(headerValue string, metadata json.RawMessage) string {
	if sessionID := strings.TrimSpace(headerValue); sessionID != "" {
		return sessionID
	}
	if len(metadata) == 0 {
		return ""
	}

	var fields struct {
		SessionID string `json:"session_id"`
		UserID    string `json:"user_id"`
	}
	if json.Unmarshal(metadata, &fields) != nil {
		return ""
	}
	if sessionID := strings.TrimSpace(fields.SessionID); sessionID != "" {
		return sessionID
	}

	userID := strings.TrimSpace(fields.UserID)
	if matches := claudeCodeSessionSuffix.FindStringSubmatch(userID); len(matches) == 2 {
		return strings.TrimSpace(matches[1])
	}
	if strings.HasPrefix(userID, "{") {
		var embedded struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal([]byte(userID), &embedded) == nil {
			return strings.TrimSpace(embedded.SessionID)
		}
	}
	return ""
}

func (m *ReplayManager) Remember(sessionID, model, accountID string, accumulator *turn.Accumulator) bool {
	if m == nil || accumulator == nil {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	model = strings.TrimSpace(model)
	if sessionID == "" || model == "" {
		return false
	}

	blocks := replayBlocks(accumulator)
	key := replayKey(sessionID, model)
	if !hasToolUseBlock(blocks) {
		m.mu.Lock()
		m.deleteLocked(key)
		m.mu.Unlock()
		return false
	}

	size := replayContentSize(blocks) + len(accountID) + len(key)
	m.mu.Lock()
	m.deleteLocked(key)
	if size > m.maxBytes {
		m.mu.Unlock()
		return false
	}
	record := replayRecord{
		AccountID: strings.TrimSpace(accountID),
		Blocks:    cloneContent(blocks),
		ExpiresAt: m.currentTime().Add(m.ttl),
		Size:      size,
	}
	m.records[key] = record
	m.bytes += record.Size
	m.evictLocked()
	_, retained := m.records[key]
	m.mu.Unlock()
	return retained
}

func (m *ReplayManager) Apply(sessionID string, request MessagesRequest) (MessagesRequest, ReplayMatch) {
	if m == nil {
		return request, ReplayMatch{}
	}
	record, ok := m.get(sessionID, request.Model)
	if !ok {
		return request, ReplayMatch{}
	}

	cachedCalls := make(map[string]Block)
	for _, block := range record.Blocks {
		if block.Type == "tool_use" {
			cachedCalls[shortenCallID(block.ID)] = cloneBlock(block)
		}
	}
	if len(cachedCalls) == 0 {
		return request, ReplayMatch{}
	}

	targetCalls := matchingToolResultIDs(request.Messages, cachedCalls)
	if len(targetCalls) == 0 {
		return request, ReplayMatch{}
	}

	existingCalls := existingToolUses(request.Messages, targetCalls)
	existingSignatures := existingReasoningSignatures(request.Messages)
	replayed := make(Content, 0, len(record.Blocks))
	replaySignatures := make(map[string]bool)
	needsReplay := false
	for _, block := range record.Blocks {
		switch block.Type {
		case "thinking":
			signature := strings.TrimSpace(jsonutil.FirstNonEmpty(block.Signature, block.Data))
			if !IsValidCodexReasoningSignature(signature) {
				continue
			}
			replaySignatures[signature] = true
			needsReplay = needsReplay || !existingSignatures[signature]
			replayed = append(replayed, cloneBlock(block))
		case "tool_use":
			callID := shortenCallID(block.ID)
			if !targetCalls[callID] {
				continue
			}
			needsReplay = needsReplay || !existingCalls[callID]
			replayed = append(replayed, cloneBlock(block))
		}
	}
	if !needsReplay {
		return request, ReplayMatch{AccountID: record.AccountID}
	}

	request.Messages = cloneMessages(request.Messages)
	replaceAt := -1
	for messageIndex, message := range request.Messages {
		filtered := make(Content, 0, len(message.Content))
		for _, block := range message.Content {
			signature := strings.TrimSpace(jsonutil.FirstNonEmpty(block.Signature, block.Data))
			if (block.Type == "thinking" || block.Type == "redacted_thinking") && replaySignatures[signature] {
				if replaceAt < 0 {
					replaceAt = messageIndex
				}
				continue
			}
			if block.Type == "tool_use" && targetCalls[shortenCallID(block.ID)] {
				if replaceAt < 0 {
					replaceAt = messageIndex
				}
				continue
			}
			filtered = append(filtered, block)
		}
		request.Messages[messageIndex].Content = filtered
	}
	insertAt := firstToolResultMessage(request.Messages, targetCalls)
	if replaceAt < 0 {
		replaceAt = matchingReplayTextMessage(request.Messages, record.Blocks, insertAt)
	}
	if replaceAt >= 0 && replaceAt < insertAt {
		request.Messages[replaceAt].Content = orderedReplayContent(
			record.Blocks,
			targetCalls,
			request.Messages[replaceAt].Content,
		)
	} else {
		request.Messages = insertMessage(request.Messages, insertAt, Message{Role: "assistant", Content: replayed})
	}
	return request, ReplayMatch{Applied: true, AccountID: record.AccountID}
}

func (m *ReplayManager) Delete(sessionID, model string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.deleteLocked(replayKey(sessionID, model))
	m.mu.Unlock()
}

func (m *ReplayManager) Sweep() {
	if m == nil {
		return
	}
	now := m.currentTime()
	m.mu.Lock()
	for key, record := range m.records {
		if !record.ExpiresAt.After(now) {
			m.deleteLocked(key)
		}
	}
	m.mu.Unlock()
}

func WithoutThinking(request MessagesRequest) (MessagesRequest, bool) {
	request.Messages = cloneMessages(request.Messages)
	changed := false
	for index := range request.Messages {
		content := request.Messages[index].Content
		filtered := make(Content, 0, len(content))
		for _, block := range content {
			if block.Type == "thinking" || block.Type == "redacted_thinking" {
				changed = true
				continue
			}
			filtered = append(filtered, block)
		}
		request.Messages[index].Content = filtered
	}
	return request, changed
}

func (m *ReplayManager) get(sessionID, model string) (replayRecord, bool) {
	key := replayKey(sessionID, model)
	if key == "" {
		return replayRecord{}, false
	}
	m.mu.RLock()
	record, ok := m.records[key]
	m.mu.RUnlock()
	if !ok {
		return replayRecord{}, false
	}
	if !record.ExpiresAt.After(m.currentTime()) {
		m.mu.Lock()
		if current, exists := m.records[key]; exists && !current.ExpiresAt.After(m.currentTime()) {
			m.deleteLocked(key)
		}
		m.mu.Unlock()
		return replayRecord{}, false
	}
	record.Blocks = cloneContent(record.Blocks)
	return record, true
}

func (m *ReplayManager) currentTime() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

func replayKey(sessionID, model string) string {
	sessionID = strings.TrimSpace(sessionID)
	model = strings.TrimSpace(model)
	if sessionID == "" || model == "" {
		return ""
	}
	// Protected routes share one configured proxy principal. The Claude session
	// ID is the continuity boundary within that principal.
	return sessionID + "\x00" + model
}

func (m *ReplayManager) deleteLocked(key string) {
	if record, exists := m.records[key]; exists {
		m.bytes -= record.Size
		delete(m.records, key)
	}
}

func (m *ReplayManager) evictLocked() {
	if len(m.records) <= m.maxEntries && m.bytes <= m.maxBytes {
		return
	}

	type candidate struct {
		key       string
		expiresAt time.Time
	}
	candidates := make([]candidate, 0, len(m.records))
	for key, record := range m.records {
		candidates = append(candidates, candidate{key: key, expiresAt: record.ExpiresAt})
	}
	slices.SortFunc(candidates, func(left, right candidate) int {
		return left.expiresAt.Compare(right.expiresAt)
	})

	entryTarget := m.maxEntries
	if len(m.records) > m.maxEntries {
		batch := min(replayEvictBatchSize, max(1, m.maxEntries/8))
		entryTarget = max(0, m.maxEntries-batch)
	}
	byteTarget := m.maxBytes
	if m.bytes > m.maxBytes {
		byteTarget = max(0, m.maxBytes-m.maxBytes/16)
	}
	for _, candidate := range candidates {
		if len(m.records) <= entryTarget && m.bytes <= byteTarget {
			break
		}
		m.deleteLocked(candidate.key)
	}
}

func replayBlocks(accumulator *turn.Accumulator) Content {
	response := BuildMessage(accumulator)
	blocks := make(Content, 0, len(response.Content))
	for _, block := range response.Content {
		switch block.Type {
		case "text":
			blocks = append(blocks, Block{Type: "text", Text: block.Text})
		case "thinking":
			if !IsValidCodexReasoningSignature(block.Signature) {
				continue
			}
			blocks = append(blocks, Block{
				Type:      "thinking",
				Thinking:  block.Thinking,
				Signature: block.Signature,
			})
		case "tool_use":
			blocks = append(blocks, Block{
				Type:  "tool_use",
				ID:    block.ID,
				Name:  block.Name,
				Input: append(json.RawMessage(nil), block.Input...),
			})
		}
	}
	return blocks
}

func replayContentSize(blocks Content) int {
	size := 0
	for _, block := range blocks {
		size += len(block.Type) + len(block.Text) + len(block.Thinking) + len(block.Signature)
		size += len(block.ID) + len(block.Name) + len(block.Input)
	}
	return size
}

func orderedReplayContent(cached Content, targetCalls map[string]bool, existing Content) Content {
	used := make([]bool, len(existing))
	ordered := make(Content, 0, len(cached)+len(existing))
	for _, block := range cached {
		switch block.Type {
		case "thinking":
			signature := strings.TrimSpace(jsonutil.FirstNonEmpty(block.Signature, block.Data))
			if IsValidCodexReasoningSignature(signature) {
				ordered = append(ordered, cloneBlock(block))
			}
		case "tool_use":
			if targetCalls[shortenCallID(block.ID)] {
				ordered = append(ordered, cloneBlock(block))
			}
		case "text":
			for index, candidate := range existing {
				if !used[index] && candidate.Type == "text" && candidate.Text == block.Text {
					ordered = append(ordered, candidate)
					used[index] = true
					break
				}
			}
		}
	}
	for index, block := range existing {
		if !used[index] {
			ordered = append(ordered, block)
		}
	}
	return ordered
}

func matchingReplayTextMessage(messages []Message, cached Content, before int) int {
	texts := make(map[string]bool)
	for _, block := range cached {
		if block.Type == "text" {
			texts[block.Text] = true
		}
	}
	for messageIndex := min(before, len(messages)) - 1; messageIndex >= 0; messageIndex-- {
		if messages[messageIndex].Role != "assistant" {
			continue
		}
		for _, block := range messages[messageIndex].Content {
			if block.Type == "text" && texts[block.Text] {
				return messageIndex
			}
		}
	}
	return -1
}

func hasToolUseBlock(blocks Content) bool {
	for _, block := range blocks {
		if block.Type == "tool_use" && strings.TrimSpace(block.ID) != "" {
			return true
		}
	}
	return false
}

func matchingToolResultIDs(messages []Message, cached map[string]Block) map[string]bool {
	matches := make(map[string]bool)
	for _, message := range messages {
		for _, block := range message.Content {
			if block.Type != "tool_result" {
				continue
			}
			callID := shortenCallID(block.ToolUseID)
			if _, ok := cached[callID]; ok {
				matches[callID] = true
			}
		}
	}
	return matches
}

func existingToolUses(messages []Message, target map[string]bool) map[string]bool {
	existing := make(map[string]bool)
	for _, message := range messages {
		for _, block := range message.Content {
			if block.Type != "tool_use" {
				continue
			}
			callID := shortenCallID(block.ID)
			if !target[callID] {
				continue
			}
			existing[callID] = true
		}
	}
	return existing
}

func existingReasoningSignatures(messages []Message) map[string]bool {
	signatures := make(map[string]bool)
	for _, message := range messages {
		for _, block := range message.Content {
			if block.Type != "thinking" && block.Type != "redacted_thinking" {
				continue
			}
			signature := strings.TrimSpace(jsonutil.FirstNonEmpty(block.Signature, block.Data))
			if IsValidCodexReasoningSignature(signature) {
				signatures[signature] = true
			}
		}
	}
	return signatures
}

func firstToolResultMessage(messages []Message, target map[string]bool) int {
	for messageIndex, message := range messages {
		for _, block := range message.Content {
			if block.Type == "tool_result" && target[shortenCallID(block.ToolUseID)] {
				return messageIndex
			}
		}
	}
	return len(messages)
}

func insertMessage(messages []Message, index int, message Message) []Message {
	index = max(0, min(index, len(messages)))
	return slices.Insert(messages, index, message)
}

func cloneMessages(messages []Message) []Message {
	cloned := make([]Message, len(messages))
	for index, message := range messages {
		cloned[index] = message
		cloned[index].Content = cloneContent(message.Content)
	}
	return cloned
}

func cloneContent(content Content) Content {
	if content == nil {
		return nil
	}
	cloned := make(Content, len(content))
	for index, block := range content {
		cloned[index] = cloneBlock(block)
	}
	return cloned
}

func cloneBlock(block Block) Block {
	cloned := block
	cloned.Input = append(json.RawMessage(nil), block.Input...)
	cloned.Content = cloneContent(block.Content)
	if block.Source != nil {
		source := *block.Source
		cloned.Source = &source
	}
	return cloned
}
