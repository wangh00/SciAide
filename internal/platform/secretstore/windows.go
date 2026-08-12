//go:build windows

package secretstore

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

const (
	credentialTypeGeneric                       = 1
	credentialPersistLocalMachine               = 2
	errorNotFound                 syscall.Errno = 1168
	maxCredentialBlob                           = 2560
)

var (
	advapi32       = syscall.NewLazyDLL("advapi32.dll")
	procCredWrite  = advapi32.NewProc("CredWriteW")
	procCredRead   = advapi32.NewProc("CredReadW")
	procCredDelete = advapi32.NewProc("CredDeleteW")
	procCredFree   = advapi32.NewProc("CredFree")
)

type credential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        syscall.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

type Native struct{ prefix string }

func NewNative(appName string) *Native { return &Native{prefix: appName + ":"} }

func (s *Native) Put(ctx context.Context, ref string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(value) == 0 {
		return fmt.Errorf("secret must not be empty")
	}
	if len(value) > maxCredentialBlob {
		return fmt.Errorf("secret exceeds Windows Credential Manager limit")
	}
	target, err := syscall.UTF16PtrFromString(s.prefix + ref)
	if err != nil {
		return err
	}
	username, err := syscall.UTF16PtrFromString("SciAide")
	if err != nil {
		return err
	}
	copyValue := append([]byte(nil), value...)
	defer zero(copyValue)
	item := credential{Type: credentialTypeGeneric, TargetName: target, CredentialBlobSize: uint32(len(copyValue)), CredentialBlob: &copyValue[0], Persist: credentialPersistLocalMachine, UserName: username}
	result, _, callErr := procCredWrite.Call(uintptr(unsafe.Pointer(&item)), 0)
	if result == 0 {
		return fmt.Errorf("CredWriteW: %w", callErr)
	}
	return nil
}

func (s *Native) Get(ctx context.Context, ref string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := syscall.UTF16PtrFromString(s.prefix + ref)
	if err != nil {
		return nil, err
	}
	var item *credential
	result, _, callErr := procCredRead.Call(uintptr(unsafe.Pointer(target)), credentialTypeGeneric, 0, uintptr(unsafe.Pointer(&item)))
	if result == 0 {
		if errors.Is(callErr, errorNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("CredReadW: %w", callErr)
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(item)))
	if item.CredentialBlobSize == 0 || item.CredentialBlob == nil {
		return []byte{}, nil
	}
	value := unsafe.Slice(item.CredentialBlob, int(item.CredentialBlobSize))
	return append([]byte(nil), value...), nil
}

func (s *Native) Delete(ctx context.Context, ref string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := syscall.UTF16PtrFromString(s.prefix + ref)
	if err != nil {
		return err
	}
	result, _, callErr := procCredDelete.Call(uintptr(unsafe.Pointer(target)), credentialTypeGeneric, 0)
	if result == 0 && !errors.Is(callErr, errorNotFound) {
		return fmt.Errorf("CredDeleteW: %w", callErr)
	}
	return nil
}

func (s *Native) Configured(ctx context.Context, ref string) (bool, string, error) {
	value, err := s.Get(ctx, ref)
	if errors.Is(err, ErrNotFound) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	defer zero(value)
	return true, mask(value), nil
}
