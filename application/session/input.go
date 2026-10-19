package session

import (
	"context"
	"errors"
	"time"

	"github.com/mslxl/natcatty/domain/model"
)

// ErrInputQueueFull reports a Session whose bounded input queue is full. A
// broadcast reports it as one target's failure instead of blocking every other
// Session.
var ErrInputQueueFull = errors.New("session: input queue is full")

// DeliveryStatus is the outcome of sending input to one Session.
type DeliveryStatus string

const (
	// DeliveryDelivered means the bytes entered the Session's transport queue.
	DeliveryDelivered DeliveryStatus = "delivered"
	// DeliveryFailed means the Session ended or its input queue was full.
	DeliveryFailed DeliveryStatus = "failed"
	// DeliveryGated means the Session's file-transfer gate consumed the input,
	// so the transport never saw it.
	DeliveryGated DeliveryStatus = "gated"
	// DeliveryMissing means the target Session is no longer registered.
	DeliveryMissing DeliveryStatus = "missing"
)

// Delivery is one target's result from a multi-Session send.
type Delivery struct {
	SessionID model.SessionID
	Status    DeliveryStatus
	Err       error
}

// SendResult reports one input sent to zero or more Sessions, in target order.
// A broadcast with no targets is a valid empty result.
type SendResult struct {
	Deliveries []Delivery
	// Bytes is the number of input bytes queued for each target. A
	// multi-chunk send sums all chunks.
	Bytes int
}

// DeliveredCount counts targets that accepted the input.
func (r SendResult) DeliveredCount() int {
	count := 0
	for _, delivery := range r.Deliveries {
		if delivery.Status == DeliveryDelivered {
			count++
		}
	}
	return count
}

// FailedCount counts targets that ended, were missing, or were gated. The UI
// shows these as the partial-failure part of a broadcast.
func (r SendResult) FailedCount() int {
	count := 0
	for _, delivery := range r.Deliveries {
		if delivery.Status != DeliveryDelivered {
			count++
		}
	}
	return count
}

// Complete reports whether every target accepted the input.
func (r SendResult) Complete() bool {
	return r.FailedCount() == 0
}

// WithInputHook observes input that passed the Session's tap and is about to
// reach its own transport. The shell uses it to fan typed input out to the
// selected broadcast Sessions. It runs on the caller's goroutine and must not
// block.
func WithInputHook(fn func(model.SessionID, []byte)) Option {
	return func(s *Service) { s.onInput = fn }
}

// SendInput sends data to exactly targets, preserving their order and
// deduplicating repeated IDs. Each target is queued independently and
// without blocking, so one saturated Session cannot stall the others. The
// returned result records every target, including a missing or gated one.
func (s *Service) SendInput(targets []model.SessionID, data []byte) SendResult {
	result := SendResult{Bytes: len(data)}
	if len(targets) == 0 {
		return result
	}

	s.mu.Lock()
	sessions := make([]*Session, len(targets))
	for index, id := range targets {
		sessions[index] = s.sessions[id]
	}
	s.mu.Unlock()

	seen := make(map[model.SessionID]bool, len(targets))
	for index, id := range targets {
		if seen[id] {
			continue
		}
		seen[id] = true
		delivery := Delivery{SessionID: id}
		switch {
		case sessions[index] == nil:
			delivery.Status = DeliveryMissing
			delivery.Err = ErrClosed
		default:
			delivery.Status, delivery.Err = sessions[index].deliver(data)
		}
		result.Deliveries = append(result.Deliveries, delivery)
	}
	return result
}

// SendChunks sends each chunk to every target in order, waiting delay between
// chunks. A target is delivered only when every chunk was delivered; a gated
// chunk marks it gated and a failed chunk marks it failed. SendChunks is for
// callers off the UI thread, such as line-by-line composed input.
func (s *Service) SendChunks(targets []model.SessionID, chunks [][]byte, delay time.Duration) SendResult {
	return s.SendChunksUntil(context.Background(), targets, chunks, delay)
}

// SendChunksUntil is SendChunks with a cancellation context. Cancellation
// stops before the next chunk, so an in-flight composed send stops immediately
// and reports only the chunks it already attempted.
func (s *Service) SendChunksUntil(ctx context.Context, targets []model.SessionID, chunks [][]byte, delay time.Duration) SendResult {
	var total SendResult
	for index, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return total
		}
		if index > 0 && delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return total
			case <-timer.C:
			}
		}
		chunkResult := s.SendInput(targets, chunk)
		if index == 0 {
			total = chunkResult
			continue
		}
		total.Bytes += chunkResult.Bytes
		for position := range total.Deliveries {
			status := chunkResult.Deliveries[position].Status
			switch {
			case status == DeliveryFailed && total.Deliveries[position].Status != DeliveryFailed:
				total.Deliveries[position].Status = DeliveryFailed
				total.Deliveries[position].Err = chunkResult.Deliveries[position].Err
			case status == DeliveryGated && total.Deliveries[position].Status == DeliveryDelivered:
				total.Deliveries[position].Status = DeliveryGated
				total.Deliveries[position].Err = chunkResult.Deliveries[position].Err
			case status == DeliveryMissing && total.Deliveries[position].Status == DeliveryDelivered:
				total.Deliveries[position].Status = DeliveryMissing
				total.Deliveries[position].Err = chunkResult.Deliveries[position].Err
			}
		}
	}
	return total
}

// deliver queues data for this Session's transport without going through the
// terminal duplex, so broadcast input never triggers the typed-input hook that
// started it. The tap still observes the bytes for command tracking and the
// transfer gate.
func (s *Session) deliver(data []byte) (DeliveryStatus, error) {
	select {
	case <-s.done:
		return DeliveryFailed, ErrClosed
	case <-s.ctx.Done():
		return DeliveryFailed, ErrClosed
	default:
	}
	forwarded, _, gated := s.handleInput(data, false)
	if gated {
		return DeliveryGated, nil
	}
	if len(forwarded) == 0 {
		return DeliveryDelivered, nil
	}
	copied := append([]byte(nil), forwarded...)
	select {
	case s.writeCh <- copied:
		return DeliveryDelivered, nil
	case <-s.done:
		return DeliveryFailed, ErrClosed
	default:
		return DeliveryFailed, ErrInputQueueFull
	}
}
