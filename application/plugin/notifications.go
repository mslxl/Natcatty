package plugin

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// Notification bounds. Notifications are visible host UI, not a bulk channel.
const (
	// MaxNotificationTitleBytes bounds the title.
	MaxNotificationTitleBytes = 200
	// MaxNotificationBodyBytes bounds the body.
	MaxNotificationBodyBytes = 4000
	// MaxNotificationActions bounds actionable buttons per notification.
	MaxNotificationActions = 4
	// MaxNotificationActionLabelBytes bounds one button label.
	MaxNotificationActionLabelBytes = 100
	// MaxTrackedNotifications bounds undismissed notifications per host.
	MaxTrackedNotifications = 256
)

var notificationActionIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// NotificationAction is one actionable notification button chosen by the host
// UI. The plugin receives the action ID as a notification.action event.
type NotificationAction struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Notification is one host notification posted by a plugin.
type Notification struct {
	ID         string
	PluginID   string
	PluginName string
	Title      string
	Body       string
	Actions    []NotificationAction
}

// NotificationSink is the consumer-owned port over the host notification UI.
// The app implements it with the native notification/dialog surface; tests
// record calls.
type NotificationSink interface {
	Post(ctx context.Context, notification Notification) error
}

// NotificationResult is the host's acknowledgement of one post. A duplicate
// post inside the deduplication window returns the original ID and
// Deduplicated true; no second notification is shown.
type NotificationResult struct {
	ID           string `json:"id"`
	Deduplicated bool   `json:"deduplicated,omitempty"`
}

// NotificationNotFoundError reports an action or dismissal for an unknown or
// expired notification.
type NotificationNotFoundError struct {
	ID string
}

func (e *NotificationNotFoundError) Error() string {
	return fmt.Sprintf("plugin notification %q does not exist or has expired", e.ID)
}

// NotificationActionError reports an action ID the notification did not
// declare.
type NotificationActionError struct {
	NotificationID string
	ActionID       string
}

func (e *NotificationActionError) Error() string {
	return fmt.Sprintf("plugin notification %q has no action %q", e.NotificationID, e.ActionID)
}

// MemoryNotificationSink records posted notifications. It is the deterministic
// sink for tests and a safe no-UI fallback for a composition without a desktop
// notification surface.
type MemoryNotificationSink struct {
	mu            sync.Mutex
	notifications []Notification
}

// Post implements NotificationSink.
func (s *MemoryNotificationSink) Post(_ context.Context, notification Notification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	notification.Actions = append([]NotificationAction(nil), notification.Actions...)
	s.notifications = append(s.notifications, notification)
	return nil
}

// Notifications returns a copy of everything posted.
func (s *MemoryNotificationSink) Notifications() []Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Notification(nil), s.notifications...)
}

// Len returns the posted count.
func (s *MemoryNotificationSink) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.notifications)
}

type notificationQuota struct {
	window time.Time
	count  int
	seen   map[string]notificationSeen
}

type notificationSeen struct {
	id string
	at time.Time
}

type pendingNotification struct {
	ref     PluginRef
	subject AuthoritySubject
	actions map[string]bool
	at      time.Time
}

// NotificationService applies per-plugin rate limiting and deduplication and
// tracks undismissed notifications so action results can be routed back to the
// plugin that posted them.
type NotificationService struct {
	now    func() time.Time
	sink   NotificationSink
	rate   int
	window time.Duration

	mu      sync.Mutex
	quotas  map[AuthoritySubject]*notificationQuota
	pending map[string]*pendingNotification
	order   []string
	nextID  uint64
}

func newNotificationService(host *HostServices) *NotificationService {
	return &NotificationService{
		now:     host.now,
		sink:    host.notificationsSink,
		rate:    host.limits.NotificationRate,
		window:  host.limits.NotificationWindow,
		quotas:  make(map[AuthoritySubject]*notificationQuota),
		pending: make(map[string]*pendingNotification),
	}
}

// NotificationPost posts one actionable notification. It requires the
// notifications permission, is rate-limited, deduplicated, and audited.
func (s *ServiceSession) NotificationPost(ctx context.Context, title, body string,
	actions []NotificationAction) (NotificationResult, error) {
	if err := s.check(ServiceNotifications, "post",
		domainplugin.PermissionNotifications, domainplugin.Scope{}); err != nil {
		return NotificationResult{}, err
	}
	if err := validateNotification(title, body, actions); err != nil {
		return NotificationResult{}, s.fail(ServiceNotifications, "post",
			domainplugin.PermissionNotifications, domainplugin.Scope{},
			ServiceCodeInvalid,
			serviceError(ServiceNotifications, "post", domainplugin.PermissionNotifications,
				domainplugin.Scope{}, err))
	}
	if s.host.notifications.sink == nil {
		return NotificationResult{}, s.fail(ServiceNotifications, "post",
			domainplugin.PermissionNotifications, domainplugin.Scope{}, ServiceCodeFault,
			serviceError(ServiceNotifications, "post", domainplugin.PermissionNotifications,
				domainplugin.Scope{}, ErrServiceUnavailable))
	}
	result, err := s.host.notifications.post(ctx, s, title, body, actions)
	if err != nil {
		return NotificationResult{}, s.fail(ServiceNotifications, "post",
			domainplugin.PermissionNotifications, domainplugin.Scope{},
			notificationResultCode(err),
			serviceError(ServiceNotifications, "post", domainplugin.PermissionNotifications,
				domainplugin.Scope{}, err))
	}
	resultCode := ServiceCodeOK
	if result.Deduplicated {
		resultCode = "duplicate"
	}
	s.record(ServiceNotifications, "post", domainplugin.PermissionNotifications,
		domainplugin.Scope{}, resultCode)
	return result, nil
}

// NotificationDismiss removes one pending notification without choosing an
// action. It only dismisses notifications owned by this plugin instance.
func (s *ServiceSession) NotificationDismiss(notificationID string) error {
	if err := s.check(ServiceNotifications, "dismiss",
		domainplugin.PermissionNotifications, domainplugin.Scope{}); err != nil {
		return err
	}
	if err := s.host.notifications.dismiss(s.subject, notificationID); err != nil {
		return s.fail(ServiceNotifications, "dismiss", domainplugin.PermissionNotifications,
			domainplugin.Scope{}, notificationResultCode(err),
			serviceError(ServiceNotifications, "dismiss", domainplugin.PermissionNotifications,
				domainplugin.Scope{}, err))
	}
	s.record(ServiceNotifications, "dismiss", domainplugin.PermissionNotifications,
		domainplugin.Scope{}, ServiceCodeOK)
	return nil
}

func (n *NotificationService) post(ctx context.Context, session *ServiceSession, title, body string,
	actions []NotificationAction) (NotificationResult, error) {
	now := n.now().UTC()
	key := notificationDedupKey(title, body, actions)
	n.mu.Lock()
	quota := n.quotas[session.subject]
	if quota == nil {
		quota = &notificationQuota{seen: make(map[string]notificationSeen)}
		n.quotas[session.subject] = quota
	}
	if quota.window.IsZero() || now.Sub(quota.window) >= n.window || now.Before(quota.window) {
		quota.window = now
		quota.count = 0
		for existing, seen := range quota.seen {
			if now.Sub(seen.at) >= n.window {
				delete(quota.seen, existing)
			}
		}
	}
	if seen, ok := quota.seen[key]; ok && now.Sub(seen.at) < n.window {
		n.mu.Unlock()
		return NotificationResult{ID: seen.id, Deduplicated: true}, nil
	}
	if quota.count >= n.rate {
		n.mu.Unlock()
		return NotificationResult{}, &LimitError{
			Resource: "notifications per " + n.window.String(), Limit: n.rate,
		}
	}
	n.nextID++
	id := fmt.Sprintf("notif-%d", n.nextID)
	declared := make(map[string]bool, len(actions))
	for _, action := range actions {
		declared[action.ID] = true
	}
	quota.count++
	quota.seen[key] = notificationSeen{id: id, at: now}
	n.pending[id] = &pendingNotification{
		ref: session.ref, subject: session.subject, actions: declared, at: now,
	}
	n.order = append(n.order, id)
	for len(n.order) > MaxTrackedNotifications {
		oldest := n.order[0]
		n.order = n.order[1:]
		delete(n.pending, oldest)
	}
	n.mu.Unlock()

	notification := Notification{
		ID:         id,
		PluginID:   session.ref.ID,
		PluginName: session.ref.Name,
		Title:      title,
		Body:       body,
		Actions:    append([]NotificationAction(nil), actions...),
	}
	if err := n.sink.Post(ctx, notification); err != nil {
		n.mu.Lock()
		delete(n.pending, id)
		delete(quota.seen, key)
		if quota.count > 0 {
			quota.count--
		}
		n.removeOrderLocked(id)
		n.mu.Unlock()
		return NotificationResult{}, &ServiceError{
			Service: ServiceNotifications, Method: "post", Code: ServiceCodeFault,
			Permission: domainplugin.PermissionNotifications, Message: err.Error(), Cause: err,
		}
	}
	return NotificationResult{ID: id}, nil
}

func (n *NotificationService) dismiss(subject AuthoritySubject, id string) error {
	n.mu.Lock()
	pending, ok := n.pending[id]
	if !ok || pending.subject != subject {
		n.mu.Unlock()
		return &NotificationNotFoundError{ID: id}
	}
	delete(n.pending, id)
	n.removeOrderLocked(id)
	n.mu.Unlock()
	return nil
}

func (n *NotificationService) removeOrderLocked(id string) {
	for i, existing := range n.order {
		if existing == id {
			n.order = append(n.order[:i], n.order[i+1:]...)
			return
		}
	}
}

// NotificationAction records the user's choice and publishes the action result
// to the plugin that posted the notification. The host UI calls it.
func (h *HostServices) NotificationAction(notificationID, actionID string) error {
	if h == nil || h.notifications == nil {
		return ErrServiceUnavailable
	}
	n := h.notifications
	n.mu.Lock()
	pending, ok := n.pending[notificationID]
	if !ok {
		n.mu.Unlock()
		return &NotificationNotFoundError{ID: notificationID}
	}
	if !pending.actions[actionID] {
		n.mu.Unlock()
		return &NotificationActionError{
			NotificationID: notificationID, ActionID: actionID,
		}
	}
	delete(n.pending, notificationID)
	n.removeOrderLocked(notificationID)
	ref := pending.ref
	n.mu.Unlock()

	h.audit.Record(domainplugin.AuditEvent{
		Time:       h.now().UTC(),
		Event:      domainplugin.AuditService,
		PluginID:   ref.ID,
		Source:     ref.Source,
		Version:    ref.Version,
		Permission: domainplugin.PermissionNotifications,
		Reason:     domainplugin.ServiceAuditReason(ServiceNotifications, "action", ServiceCodeOK),
	})
	h.publishTo(SubjectForRef(ref), domainplugin.EventNotificationAction, map[string]string{
		"notification": notificationID,
		"action":       actionID,
	})
	return nil
}

func validateNotification(title, body string, actions []NotificationAction) error {
	if strings.TrimSpace(title) == "" {
		return &StorageKeyError{Problem: "a notification title is required"}
	}
	if !utf8.ValidString(title) || !utf8.ValidString(body) {
		return &StorageKeyError{Problem: "title and body must be valid UTF-8"}
	}
	if len(title) > MaxNotificationTitleBytes {
		return &StorageKeyError{Problem: fmt.Sprintf("title must be at most %d bytes",
			MaxNotificationTitleBytes)}
	}
	if len(body) > MaxNotificationBodyBytes {
		return &StorageKeyError{Problem: fmt.Sprintf("body must be at most %d bytes",
			MaxNotificationBodyBytes)}
	}
	if len(actions) > MaxNotificationActions {
		return &LimitError{Resource: "notification actions", Limit: MaxNotificationActions}
	}
	seen := make(map[string]bool, len(actions))
	for _, action := range actions {
		if !notificationActionIDPattern.MatchString(action.ID) {
			return &StorageKeyError{Key: action.ID,
				Problem: "action id must be lowercase letters, digits, dot, underscore, or hyphen, at most 32 characters"}
		}
		if seen[action.ID] {
			return &StorageKeyError{Key: action.ID, Problem: "duplicate action id"}
		}
		seen[action.ID] = true
		if strings.TrimSpace(action.Label) == "" {
			return &StorageKeyError{Key: action.ID, Problem: "an action label is required"}
		}
		if len(action.Label) > MaxNotificationActionLabelBytes {
			return &StorageKeyError{Key: action.ID,
				Problem: fmt.Sprintf("label must be at most %d bytes", MaxNotificationActionLabelBytes)}
		}
		if !utf8.ValidString(action.Label) {
			return &StorageKeyError{Key: action.ID, Problem: "label must be valid UTF-8"}
		}
	}
	return nil
}

func notificationDedupKey(title, body string, actions []NotificationAction) string {
	var key strings.Builder
	key.WriteString(title)
	key.WriteByte(0)
	key.WriteString(body)
	for _, action := range actions {
		key.WriteByte(0)
		key.WriteString(action.ID)
		key.WriteByte(0)
		key.WriteString(action.Label)
	}
	return key.String()
}

func notificationResultCode(err error) string {
	var service *ServiceError
	if errors.As(err, &service) {
		return service.Code
	}
	var limit *LimitError
	if errors.As(err, &limit) {
		return ServiceCodeLimit
	}
	var missing *NotificationNotFoundError
	if errors.As(err, &missing) {
		return ServiceCodeNotFound
	}
	var action *NotificationActionError
	if errors.As(err, &action) {
		return ServiceCodeInvalid
	}
	var invalid *StorageKeyError
	if errors.As(err, &invalid) {
		return ServiceCodeInvalid
	}
	return ServiceCodeFault
}
