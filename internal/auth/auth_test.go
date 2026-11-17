package auth

import "testing"

func TestEqual(t *testing.T) {
	if !Equal("test-key", "test-key") {
		t.Fatal("equal keys did not match")
	}
	if Equal("test-key", "test-key-2") {
		t.Fatal("different keys matched")
	}
	if Equal("short", "a-much-longer-registration-key") {
		t.Fatal("different lengths matched")
	}
	if Equal("", "") {
		// Empty compares equal at this layer. Callers must reject empty keys
		// before accepting a connection.
	} else {
		t.Fatal("empty strings must compare equal")
	}
	if Equal("", "test-key") {
		t.Fatal("empty matched a real key")
	}
}
