package auth

import "testing"

func TestHashAndVerify(t *testing.T) {
	pw := "correct-horse-battery-staple-123!"
	hash, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := VerifyPassword(pw, hash); err != nil {
		t.Fatalf("verify good password: %v", err)
	}
	if err := VerifyPassword("wrong", hash); err == nil {
		t.Fatalf("verify wrong password should fail")
	}
}

func TestHashUniquePerCall(t *testing.T) {
	a, _ := HashPassword("x")
	b, _ := HashPassword("x")
	if a == b {
		t.Fatalf("expected unique salts per hash")
	}
}
