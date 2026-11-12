package pythonkernel

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestScopedStartLocksCountWaitersAndReleaseAllKeys(t *testing.T) {
	r := &Runtime{starts: map[string]*startLock{}}
	var wg sync.WaitGroup
	var inside atomic.Int32
	var overlaps atomic.Int32
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := r.lockStart("same")
			if inside.Add(1) != 1 {
				overlaps.Add(1)
			}
			time.Sleep(time.Microsecond)
			inside.Add(-1)
			unlock()
		}()
	}
	wg.Wait()
	if overlaps.Load() != 0 {
		t.Fatal("waiters used different locks")
	}
	for i := 0; i < 1000; i++ {
		unlock := r.lockStart(fmt.Sprint(i))
		unlock()
	}
	if len(r.starts) != 0 {
		t.Fatal("lock keys leaked", len(r.starts))
	}
}
