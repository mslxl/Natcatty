package state

// InvalidateLists marks every listed Vault family stale after a change made
// off the UI thread, such as the sync service applying a downloaded cloud
// copy. It is safe to call from any goroutine: it sets a guarded flag and asks
// for a redraw, leaving the lists and any open editor form untouched. The UI
// thread applies the reload with FlushLists before the next frame, the same
// pattern InvalidateKnownHosts and FlushKnownHosts use.
func (s *State) InvalidateLists() {
	s.staleMu.Lock()
	s.stale = true
	s.staleMu.Unlock()
	s.redraw()
}

// FlushLists reloads the Vault lists when InvalidateLists marked them stale.
// The view calls it on the UI thread before rendering. An open editor keeps
// its unsaved buffer: load replaces only the listed records, so a save after
// the refresh still reports the normal conflict (for example, the Host no
// longer exists) and leaves the form open for the user to decide.
func (s *State) FlushLists() {
	s.staleMu.Lock()
	stale := s.stale
	s.stale = false
	s.staleMu.Unlock()
	if !stale {
		return
	}
	_ = s.load()
}
