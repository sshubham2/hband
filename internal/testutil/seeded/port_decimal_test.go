package main

import (
	"io"
	"testing"
)

// TestPortFlagReadsADecimalPort: -port names a decimal port. A leading zero or a
// base prefix must not turn it into a port other than the one the caller named.
func TestPortFlagReadsADecimalPort(t *testing.T) {
	if got, err := parsePort([]string{"-port", "010"}, io.Discard); err == nil && got != 10 {
		t.Errorf("parsePort(-port 010) = %d: the launcher would serve on port %d, not 10", got, got)
	}
	for _, v := range []string{"0x1F90", "0o17", "0b1", "1_000"} {
		if got, err := parsePort([]string{"-port", v}, io.Discard); err == nil {
			t.Errorf("parsePort(-port %s) = %d, want a refusal: not a decimal port number", v, got)
		}
	}
}
