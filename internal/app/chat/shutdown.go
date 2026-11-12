package chat

import "errors"

// ErrApplicationClosing is a host lifecycle signal, not a user cancellation.
// It is carried by the runner context; never infer it from a model error text.
var ErrApplicationClosing = errors.New("application is closing")
