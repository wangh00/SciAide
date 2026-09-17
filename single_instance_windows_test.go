//go:build windows && !bindings

package main

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestProcessMutexRejectsSecondOwnerAndReleasesCleanly(t *testing.T) {
	name := fmt.Sprintf(`Local\SciAide.Test.%d.%d`, os.Getpid(), time.Now().UnixNano())
	releaseFirst, primary, err := acquireProcessMutex(name)
	if err != nil || !primary {
		t.Fatalf("first acquire = primary %v, error %v", primary, err)
	}
	defer releaseFirst()

	releaseSecond, primary, err := acquireProcessMutex(name)
	if err != nil || primary {
		t.Fatalf("second acquire = primary %v, error %v", primary, err)
	}
	releaseSecond()

	releaseFirst()
	releaseThird, primary, err := acquireProcessMutex(name)
	if err != nil || !primary {
		t.Fatalf("acquire after release = primary %v, error %v", primary, err)
	}
	releaseThird()
}
