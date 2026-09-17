//go:build bindings

package main

// Binding generation must not contend with a running desktop process.
func acquireApplicationInstance() (func(), bool, error) {
	return func() {}, true, nil
}
