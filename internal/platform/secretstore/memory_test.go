package secretstore

import (
	"context"
	"errors"
	"testing"
)

func TestMemorySecretStoreContract(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	if err := store.Put(ctx, "profile", []byte("secret-value")); err != nil {
		t.Fatal(err)
	}
	configured, masked, err := store.Configured(ctx, "profile")
	if err != nil || !configured || masked != "••••alue" {
		t.Fatalf("Configured() = (%v, %q, %v)", configured, masked, err)
	}
	value, err := store.Get(ctx, "profile")
	if err != nil || string(value) != "secret-value" {
		t.Fatalf("Get() = (%q, %v)", value, err)
	}
	if err := store.Delete(ctx, "profile"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "profile"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() after delete error = %v", err)
	}
}
