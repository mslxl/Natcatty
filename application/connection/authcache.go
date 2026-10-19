package connection

import (
	"container/list"
	"sync"
)

// MaxAuthMethodCacheEntries bounds the successful-method cache, matching the
// original's 512-entry LRU.
const MaxAuthMethodCacheEntries = 512

// AuthMethodCache remembers which authentication candidate worked for an
// endpoint so automatic connections can try it first. The key is
// "username@hostname:port"; the value is a candidate ID, never a credential.
type AuthMethodCache struct {
	mu      sync.Mutex
	max     int
	order   *list.List
	entries map[string]*list.Element
}

type authCacheEntry struct {
	key    string
	method string
}

// NewAuthMethodCache returns an LRU cache with max entries. A non-positive max
// uses MaxAuthMethodCacheEntries.
func NewAuthMethodCache(max int) *AuthMethodCache {
	if max <= 0 {
		max = MaxAuthMethodCacheEntries
	}
	return &AuthMethodCache{
		max:     max,
		order:   list.New(),
		entries: make(map[string]*list.Element, max),
	}
}

// Get returns the cached method and marks it recently used.
func (c *AuthMethodCache) Get(key string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		return "", false
	}
	c.order.MoveToFront(element)
	return element.Value.(*authCacheEntry).method, true
}

// Put records a successful method and evicts the least recently used entry
// beyond the limit.
func (c *AuthMethodCache) Put(key, method string) {
	if c == nil || key == "" || method == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		element.Value.(*authCacheEntry).method = method
		c.order.MoveToFront(element)
		return
	}
	element := c.order.PushFront(&authCacheEntry{key: key, method: method})
	c.entries[key] = element
	for c.order.Len() > c.max {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*authCacheEntry).key)
	}
}

// Delete forgets an endpoint after a failed authentication.
func (c *AuthMethodCache) Delete(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		c.order.Remove(element)
		delete(c.entries, key)
	}
}

// Len reports the number of cached endpoints.
func (c *AuthMethodCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
