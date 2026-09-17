package localexec

import "testing"

func TestLiveProcessKeepsBoundedOutputTailAndCounters(t *testing.T) {
	process := newLiveProcess("call", 42)
	value := make([]byte, LiveOutputBytes+17)
	for index := range value {
		value[index] = byte('a' + index%26)
	}
	if _, err := process.writer(true).Write(value); err != nil {
		t.Fatal(err)
	}
	if _, err := process.writer(false).Write([]byte("warning")); err != nil {
		t.Fatal(err)
	}
	snapshot := process.snapshot()
	if snapshot.CallID != "call" || snapshot.PID != 42 || snapshot.StdoutBytes != int64(len(value)) || snapshot.StderrBytes != int64(len("warning")) {
		t.Fatalf("live counters = %#v", snapshot)
	}
	if len([]byte(snapshot.StdoutTail)) > LiveOutputBytes || snapshot.StdoutTail == string(value) || snapshot.StderrTail != "warning" {
		t.Fatalf("live tails = stdout %d bytes, stderr %q", len([]byte(snapshot.StdoutTail)), snapshot.StderrTail)
	}
}
