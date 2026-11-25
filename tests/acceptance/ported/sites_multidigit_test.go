package ported

import (
	"fmt"
	"testing"
)

// A return of a non-zero exit code is an assertion site whatever its number of
// digits: run_finish.py's timeout verdict is `return 124, ...`.
func TestSitesSeeMultiDigitExitCodes(t *testing.T) {
	src := "def run():\n    if slow:\n        return 124, 0.0, \"timed out\"\n    return 10\n\n\ndef main():\n    code = run()\n"
	got := fmt.Sprint(Sites(Canonical([]byte(src))))
	if got != "[3 4 8]" {
		t.Fatalf("Sites = %s, want [3 4 8]: a return of 124 or 10 is a non-zero exit code, and run() holds it", got)
	}
}
