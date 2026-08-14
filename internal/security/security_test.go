package security

import (
	"testing"
)

func TestPasswordHash(t *testing.T) {
	h := HashPassword("correct horse")
	if !VerifyPassword("correct horse", h) {
		t.Fatal("valid password rejected")
	}
	if VerifyPassword("wrong", h) {
		t.Fatal("invalid password accepted")
	}
}

func TestSession(t *testing.T) {
	s := NewSession("test-secret-key-32-bytes-minimum!!")
	tok := s.Create("admin", 60)
	claims, err := s.Verify(tok)
	if err != nil {
		t.Fatal(err)
	}
	if claims.User != "admin" {
		t.Fatalf("expected admin, got %s", claims.User)
	}
	if _, err := s.Verify(tok + "tampered"); err == nil {
		t.Fatal("tampered token accepted")
	}
}

func TestIPInList(t *testing.T) {
	cases := []struct {
		ip   string
		list []string
		want bool
	}{
		{"1.2.3.4", []string{"1.2.3.4"}, true},
		{"1.2.3.5", []string{"1.2.3.4"}, false},
		{"10.0.0.5", []string{"10.0.0.0/8"}, true},
		{"192.168.1.10", []string{"192.168.0.0/16"}, true},
		{"8.8.8.8", []string{"10.0.0.0/8", "192.168.0.0/16"}, false},
	}
	for _, c := range cases {
		if got := IPInList(c.ip, c.list); got != c.want {
			t.Errorf("IPInList(%q, %v) = %v, want %v", c.ip, c.list, got, c.want)
		}
	}
}

func TestRateLimiter(t *testing.T) {
	rl := NewRateLimiter(3)
	for i := 0; i < 3; i++ {
		ok, _ := rl.Check("k")
		if !ok {
			t.Fatalf("request %d should be allowed", i)
		}
	}
	ok, _ := rl.Check("k")
	if ok {
		t.Fatal("4th request should be blocked")
	}
	// Different key should be allowed
	ok, _ = rl.Check("other")
	if !ok {
		t.Fatal("different key should be allowed")
	}
}
