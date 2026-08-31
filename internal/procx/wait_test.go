package procx

import (
	"errors"
	"testing"
	"time"
)

func tableWith(pids map[int]string) *Table {
	t := &Table{byPID: map[int]Proc{}, children: map[int][]int{}}
	for pid, st := range pids {
		t.byPID[pid] = Proc{PID: pid, StartTime: st}
	}
	return t
}

func TestWaitGone(t *testing.T) {
	calls := 0
	scan := func() (*Table, error) {
		calls++
		if calls < 3 {
			return tableWith(map[int]string{42: "7"}), nil
		}
		return tableWith(map[int]string{}), nil
	}
	var slept []time.Duration
	sleep := func(d time.Duration) { slept = append(slept, d) }
	if err := WaitGone(scan, 42, "7", time.Second, 100*time.Millisecond, sleep); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || len(slept) != 2 || slept[0] != 100*time.Millisecond {
		t.Fatalf("calls=%d slept=%v", calls, slept)
	}
}

func TestWaitGoneTimesOut(t *testing.T) {
	scan := func() (*Table, error) { return tableWith(map[int]string{42: "7"}), nil }
	var total time.Duration
	err := WaitGone(scan, 42, "7", 500*time.Millisecond, 100*time.Millisecond, func(d time.Duration) { total += d })
	if err == nil || !errors.Is(err, ErrTimeout) || total < 500*time.Millisecond {
		t.Fatalf("err=%v total=%v", err, total)
	}
}

func TestWaitGoneReusedPidIsGone(t *testing.T) {
	scan := func() (*Table, error) { return tableWith(map[int]string{42: "8"}), nil }
	if err := WaitGone(scan, 42, "7", time.Second, 100*time.Millisecond, func(time.Duration) {}); err != nil {
		t.Fatalf("a pid with a different start time is gone: %v", err)
	}
}

func TestWaitGoneScanError(t *testing.T) {
	scan := func() (*Table, error) { return nil, errors.New("boom") }
	if err := WaitGone(scan, 42, "7", time.Second, 100*time.Millisecond, func(time.Duration) {}); err == nil {
		t.Fatal("scan error must propagate")
	}
}
