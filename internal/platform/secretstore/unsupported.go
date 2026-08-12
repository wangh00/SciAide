//go:build !windows

package secretstore

import (
	"context"
	"fmt"
)

type Native struct{}

func NewNative(string) *Native { return &Native{} }
func (*Native) Put(context.Context, string, []byte) error {
	return fmt.Errorf("native SecretStore is not implemented on this platform")
}
func (*Native) Get(context.Context, string) ([]byte, error) {
	return nil, fmt.Errorf("native SecretStore is not implemented on this platform")
}
func (*Native) Delete(context.Context, string) error {
	return fmt.Errorf("native SecretStore is not implemented on this platform")
}
func (*Native) Configured(context.Context, string) (bool, string, error) {
	return false, "", fmt.Errorf("native SecretStore is not implemented on this platform")
}
