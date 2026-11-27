package session

import "sync"

// SubscribeOutput installs a listener for decoded bytes produced by the
// Session's transport. The listener sees the same ordered stream that the
// terminal receives, but it does not consume or alter that stream. The
// returned function removes the listener and is safe to call more than once.
//
// Listeners run on the Session read loop. They should do bounded, in-memory
// work and return promptly; a slow listener backpressures the terminal in the
// same way a slow terminal consumer does.
func (s *Session) SubscribeOutput(fn func([]byte)) func() {
	if fn == nil {
		return func() {}
	}
	s.outputObserverMu.Lock()
	if s.outputObservers == nil {
		s.outputObservers = make(map[uint64]func([]byte))
	}
	s.nextOutputID++
	id := s.nextOutputID
	s.outputObservers[id] = fn
	s.outputObserverMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.outputObserverMu.Lock()
			delete(s.outputObservers, id)
			s.outputObserverMu.Unlock()
		})
	}
}

// SubscribeOutputReplay installs a decoded-stream listener and synchronously
// replays the Session's bounded command-mark history before allowing new output
// to reach it. The replay and the subscription are ordered under one lock, so
// a Lens surface cannot see future bytes before the historical OSC 133 markers
// that describe them.
func (s *Session) SubscribeOutputReplay(fn func([]byte)) func() {
	if fn == nil {
		return func() {}
	}
	s.outputDeliverMu.Lock()
	s.outputObserverMu.Lock()
	replay := s.outputReplay.snapshot()
	s.outputObserverMu.Unlock()
	if len(replay) > 0 {
		fn(replay)
	}
	s.outputObserverMu.Lock()
	if s.outputObservers == nil {
		s.outputObservers = make(map[uint64]func([]byte))
	}
	s.nextOutputID++
	id := s.nextOutputID
	s.outputObservers[id] = fn
	s.outputObserverMu.Unlock()
	s.outputDeliverMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.outputObserverMu.Lock()
			delete(s.outputObservers, id)
			s.outputObserverMu.Unlock()
		})
	}
}

// SubscribeOutputFilterReplay is SubscribeOutputReplay for a consumer that may
// strip captured binary payload bytes from the stream before it continues on to
// the terminal tap, highlighter, replay buffer, and visible terminal.
func (s *Session) SubscribeOutputFilterReplay(fn func([]byte) []byte) func() {
	if fn == nil {
		return func() {}
	}
	s.outputDeliverMu.Lock()
	s.outputObserverMu.Lock()
	replay := s.outputReplay.snapshot()
	s.outputObserverMu.Unlock()
	if len(replay) > 0 {
		fn(replay)
	}
	s.outputObserverMu.Lock()
	if s.outputFilters == nil {
		s.outputFilters = make(map[uint64]func([]byte) []byte)
	}
	s.nextOutputID++
	id := s.nextOutputID
	s.outputFilters[id] = fn
	s.outputObserverMu.Unlock()
	s.outputDeliverMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.outputObserverMu.Lock()
			delete(s.outputFilters, id)
			s.outputObserverMu.Unlock()
		})
	}
}

func (s *Session) publishOutput(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	s.outputDeliverMu.Lock()
	defer s.outputDeliverMu.Unlock()
	s.outputObserverMu.Lock()
	listeners := make([]func([]byte), 0, len(s.outputObservers))
	for _, fn := range s.outputObservers {
		listeners = append(listeners, fn)
	}
	filters := make([]func([]byte) []byte, 0, len(s.outputFilters))
	for _, fn := range s.outputFilters {
		filters = append(filters, fn)
	}
	s.outputObserverMu.Unlock()
	for _, fn := range listeners {
		fn(data)
	}
	filtered := data
	for _, fn := range filters {
		filtered = fn(filtered)
	}
	s.outputObserverMu.Lock()
	s.outputReplay.append(filtered)
	s.outputObserverMu.Unlock()
	return filtered
}
