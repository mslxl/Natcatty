package connection

import "testing"

func TestAuthMethodCacheEvictsLeastRecentlyUsed(t *testing.T) {
	t.Parallel()

	cache := NewAuthMethodCache(2)
	cache.Put("a", CandidatePassword)
	cache.Put("b", CandidateAgent)

	if method, ok := cache.Get("a"); !ok || method != CandidatePassword {
		t.Fatalf("Get(a) = %q, %v", method, ok)
	}
	cache.Put("c", "publickey-user")

	if _, ok := cache.Get("b"); ok {
		t.Error("b was not evicted as the least recently used entry")
	}
	if _, ok := cache.Get("a"); !ok {
		t.Error("a was evicted even though it was recently read")
	}
	if method, ok := cache.Get("c"); !ok || method != "publickey-user" {
		t.Errorf("Get(c) = %q, %v", method, ok)
	}
	if cache.Len() != 2 {
		t.Errorf("Len = %d, want 2", cache.Len())
	}
}

func TestAuthMethodCacheDefaultLimitIs512(t *testing.T) {
	t.Parallel()

	cache := NewAuthMethodCache(0)
	for i := 0; i < MaxAuthMethodCacheEntries+10; i++ {
		cache.Put(string(rune('a'+i%26))+string(rune('a'+i/26)), CandidatePassword)
	}
	if cache.Len() != MaxAuthMethodCacheEntries {
		t.Fatalf("Len = %d, want %d", cache.Len(), MaxAuthMethodCacheEntries)
	}
}

func TestAuthMethodCacheDeleteClearsFailure(t *testing.T) {
	t.Parallel()

	cache := NewAuthMethodCache(4)
	cache.Put("endpoint", CandidatePassword)
	cache.Delete("endpoint")
	if _, ok := cache.Get("endpoint"); ok {
		t.Error("deleted method is still cached")
	}
}
