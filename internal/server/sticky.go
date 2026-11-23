package server

import "container/list"

const maxStickyAccounts = 8192

type stickyEntry struct {
	affinity string
	account  string
	at       int64
}

// stickyAccounts is guarded by App.mu. Only a successful inference refreshes
// the timestamp and eviction order.
type stickyAccounts struct {
	entries map[string]*list.Element
	order   list.List
}

func (s *stickyAccounts) account(affinity string, minutes float64, now int64) string {
	if e := s.entries[affinity]; e != nil {
		entry := e.Value.(*stickyEntry)
		if minutes > 0 && float64(now-entry.at) <= minutes*60000 {
			return entry.account
		}
	}
	return ""
}

func (s *stickyAccounts) remember(affinity, account string, now int64) {
	if e := s.entries[affinity]; e != nil {
		entry := e.Value.(*stickyEntry)
		entry.account, entry.at = account, now
		s.order.MoveToFront(e)
		return
	}
	if s.entries == nil {
		s.entries = make(map[string]*list.Element)
	}
	if s.order.Len() == maxStickyAccounts {
		oldest := s.order.Back()
		delete(s.entries, oldest.Value.(*stickyEntry).affinity)
		s.order.Remove(oldest)
	}
	s.entries[affinity] = s.order.PushFront(&stickyEntry{affinity: affinity, account: account, at: now})
}
