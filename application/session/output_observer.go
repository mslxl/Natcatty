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
	s.outputObserverMu.Lock()
	if replay := s.outputReplay.snapshot(); len(replay) > 0 {
		fn(replay)
	}
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

func (s *Session) publishOutput(data []byte) {
	if len(data) == 0 {
		return
	}
	s.outputObserverMu.Lock()
	s.outputReplay.append(data)
	listeners := make([]func([]byte), 0, len(s.outputObservers))
	for _, fn := range s.outputObservers {
		listeners = append(listeners, fn)
	}
	s.outputObserverMu.Unlock()
	for _, fn := range listeners {
		fn(data)
	}
}
