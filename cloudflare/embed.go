package cloudflare

import _ "embed"

// Solver is the supplied implementation, embedded unchanged.
//
//go:embed solve.py
var Solver string
