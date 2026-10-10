package lru

import "testing"

func TestCachePromotesAndEvictsLeastRecentlyUsed(t *testing.T) {
	cache := New[string, int](2)
	cache.Put("a", 1)
	cache.Put("b", 2)
	if value, ok := cache.Get("a"); !ok || value != 1 {
		t.Fatalf("Get(a) = %d, %v", value, ok)
	}
	cache.Put("c", 3)
	if _, ok := cache.Get("b"); ok {
		t.Fatal("least recently used entry was not evicted")
	}
	if cache.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", cache.Len())
	}
}

func TestCacheUpdatesAndRemovesInvalidEntries(t *testing.T) {
	cache := New[string, int](0)
	cache.Put("a", 1)
	cache.Put("a", 2)
	if value, ok := cache.GetIf("a", func(value int) bool { return value == 2 }); !ok || value != 2 {
		t.Fatalf("GetIf(a) = %d, %v", value, ok)
	}
	if _, ok := cache.GetIf("a", func(int) bool { return false }); ok {
		t.Fatal("invalid entry was returned")
	}
	if cache.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", cache.Len())
	}
}

func BenchmarkCacheParallel(b *testing.B) {
	cache := New[int, int](1024)
	for i := 0; i < 1024; i++ {
		cache.Put(i, i)
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		key := 0
		for pb.Next() {
			if _, ok := cache.Get(key & 1023); !ok {
				b.Fatal("cache miss")
			}
			key++
		}
	})
}
