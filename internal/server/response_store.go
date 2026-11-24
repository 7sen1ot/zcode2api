package server

import (
	"slices"
	"sync"
	"zcode2api/internal/server/shared"
)

const (
	maxStoredResponses = 100
	maxResponseBytes   = 64 << 20
)

type responseStore struct {
	mu      sync.Mutex
	entries map[string]responseEntry
	order   []string
	bytes   int
}

type responseEntry struct {
	Key             string
	Affinity        string
	InstructionMode string
	History         *responseHistory
	Tools           *responseToolSet
	ToolOptions     shared.M
	Response        shared.M
	Bytes           int
}

type responseToolSet struct {
	definitions []any
	bytes       int
	refs        int // Stored entries, guarded by responseStore.mu.
}

type responseHistory struct {
	parent     *responseHistory
	items      []any
	count      int
	bytes      int
	totalBytes int
	refs       int // Stored entries and child nodes, guarded by responseStore.mu.
}

func (e responseEntry) reuseTools(payload shared.M) *responseToolSet {
	// An explicit tools field, including an empty list, replaces the previous set.
	if _, explicit := payload["tools"]; explicit || e.Tools == nil {
		return nil
	}
	payload["tools"] = e.Tools.definitions
	for field, value := range e.ToolOptions {
		if _, explicit := payload[field]; !explicit {
			payload[field] = value
		}
	}
	return e.Tools
}

func (h *responseHistory) withInput(input []any) []any {
	items := make([]any, h.count+len(input))
	copy(items[h.count:], input)
	for node := h; node != nil; node = node.parent {
		copy(items[node.count-len(node.items):node.count], node.items)
	}
	return items
}

func (s *responseStore) lookupLocked(id, keyID string) (responseEntry, bool) {
	entry, ok := s.entries[id]
	if !ok || entry.Key != keyID {
		return responseEntry{}, false
	}
	return entry, true
}

func (s *responseStore) get(id, keyID string, touch bool) (responseEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.lookupLocked(id, keyID)
	if ok && touch {
		s.removeFromOrderLocked(id)
		s.order = append(s.order, id)
	}
	return entry, ok
}

func (s *responseStore) delete(id, keyID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.lookupLocked(id, keyID); !ok {
		return false
	}
	s.forgetLocked(id)
	return true
}

func (s *responseStore) removeFromOrderLocked(id string) {
	if i := slices.Index(s.order, id); i >= 0 {
		s.order = slices.Delete(s.order, i, i+1)
	}
}

func (s *responseStore) forgetLocked(id string) {
	entry, ok := s.entries[id]
	if !ok {
		return
	}
	delete(s.entries, id)
	s.removeFromOrderLocked(id)
	s.bytes -= entry.Bytes
	if entry.Tools != nil {
		entry.Tools.refs--
		if entry.Tools.refs == 0 {
			s.bytes -= entry.Tools.bytes
		}
	}
	for h := entry.History; h != nil; h = h.parent {
		h.refs--
		if h.refs > 0 {
			break
		}
		s.bytes -= h.bytes
	}
}

func (s *responseStore) remember(entry responseEntry, parent *responseHistory, input []any, assistant any) {
	items := make([]any, 0, len(input)+1)
	items = append(items, input...)
	if assistant != nil {
		items = append(items, assistant)
	}
	// Share history across continuations and branches, including when a parent
	// response is later evicted or explicitly deleted.
	itemBytes := len(shared.JSON(items))
	history := &responseHistory{parent: parent, items: items, count: len(items), bytes: itemBytes, totalBytes: itemBytes}
	if parent != nil {
		history.count += parent.count
		history.totalBytes += parent.totalBytes
	}
	entry.Bytes = len(shared.JSON(entry.Response)) + len(entry.Affinity) + len(entry.InstructionMode)
	toolBytes := 0
	if entry.Tools != nil {
		entry.Bytes += len(shared.JSON(entry.ToolOptions))
		toolBytes = entry.Tools.bytes
	}
	if entry.Bytes+history.totalBytes+toolBytes > maxResponseBytes {
		return
	}
	entry.History = history
	id := shared.Str(entry.Response["id"])

	s.mu.Lock()
	defer s.mu.Unlock()
	s.forgetLocked(id)
	if s.entries == nil {
		s.entries = make(map[string]responseEntry)
	}
	s.entries[id] = entry
	s.order = append(s.order, id)
	s.bytes += entry.Bytes
	if entry.Tools != nil {
		entry.Tools.refs++
		if entry.Tools.refs == 1 {
			s.bytes += toolBytes
		}
	}
	for h := history; h != nil; h = h.parent {
		h.refs++
		if h.refs > 1 {
			break
		}
		s.bytes += h.bytes
	}
	for len(s.entries) > maxStoredResponses || s.bytes > maxResponseBytes {
		s.forgetLocked(s.order[0])
	}
}
