//go:build !windows && !bindings

package main

func acquireApplicationInstance() (func(), bool, error) {
	return func() {}, true, nil
}
