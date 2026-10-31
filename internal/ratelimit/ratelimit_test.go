package ratelimit

import "testing"

func TestAllowUntilQuota(t *testing.T) {
	l := New()
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("k", 3); !ok {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if ok, remaining := l.Allow("k", 3); ok {
		t.Fatal("4th request must be denied")
	} else if remaining != 0 {
		t.Fatalf("remaining = %d", remaining)
	}
	// a different key is independent
	if ok, _ := l.Allow("other", 3); !ok {
		t.Fatal("independent key should be allowed")
	}
}

func TestReset(t *testing.T) {
	l := New()
	l.Allow("k", 1)
	if ok, _ := l.Allow("k", 1); ok {
		t.Fatal("second request must be denied")
	}
	l.Reset("k")
	if ok, _ := l.Allow("k", 1); !ok {
		t.Fatal("request after reset must be allowed")
	}
}

func TestGC(t *testing.T) {
	l := New()
	l.Allow("k", 10)
	if l.GC() != 0 {
		t.Fatal("fresh window must survive GC")
	}
}
