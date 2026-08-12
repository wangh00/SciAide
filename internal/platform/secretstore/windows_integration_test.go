//go:build windows

package secretstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestNativeSecretStoreRoundTrip(t *testing.T) {
	if os.Getenv("SCIAIDE_TEST_NATIVE_SECRETS") != "1" {
		t.Skip("set SCIAIDE_TEST_NATIVE_SECRETS=1 to test Windows Credential Manager")
	}
	ctx := context.Background()
	store := NewNative("SciAide-Test")
	ref := fmt.Sprintf("roundtrip/%d", time.Now().UnixNano())
	defer store.Delete(ctx, ref)
	if err := store.Put(ctx, ref, []byte("native-test-secret")); err != nil {
		t.Fatal(err)
	}
	value, err := store.Get(ctx, ref)
	if err != nil || string(value) != "native-test-secret" {
		t.Fatalf("Get() = (%q, %v)", value, err)
	}
	if err := store.Delete(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() after delete error = %v", err)
	}
}
