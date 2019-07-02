package sched

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	//sync "github.com/sasha-s/go-deadlock"
)

var Verbose = true

type notifier struct {
	deadline uint32
	async    bool
	callback func()
}

var timeoutWheel struct {
	secmin [60][60]struct {
		sync.Mutex
		counter uint64
		list    map[SchedKey]*notifier
	}
	maxasyncfires, maxsyncfires int
}

func init() {
	go func() {
		for t := range time.Tick(time.Second) {
			s, m, now := t.Second(), t.Minute(), t.Unix()

			repeat, acount, scount := false, 0, 0

			syncNotifiers := make([]*notifier, 0, 16)

		REPEAT:
			ts := &timeoutWheel.secmin[s][m]
			ts.Lock()
			for k, n := range ts.list {
				if int64(n.deadline) > now {
					continue
				}

				delete(ts.list, k)

				if n.async {
					acount++
					go n.callback()
				} else {
					scount++
					syncNotifiers = append(syncNotifiers, n)
				}
			}
			ts.Unlock()

			if !repeat {
				// Dial back 1 second to check if any objects which should time out at "this second"
				// are added to the "previous second" because of clock precision
				t = time.Unix(now-1, 0)
				s, m = t.Second(), t.Minute()
				repeat = true
				goto REPEAT
			}

			for _, n := range syncNotifiers {
				n.callback()
			}

			if acount > timeoutWheel.maxasyncfires {
				timeoutWheel.maxasyncfires = acount
			}
			if scount > timeoutWheel.maxsyncfires {
				timeoutWheel.maxsyncfires = scount
			}

			if Verbose {
				fmt.Print(time.Now().Format(time.StampMilli),
					" fires: async(", acount, " max:", timeoutWheel.maxasyncfires, ") sync(", scount, " max:", timeoutWheel.maxsyncfires, ")\n")
			}
		}
	}()
}

type SchedKey uint64

func Schedule(callback func(), deadline time.Time) SchedKey {
	return schedule(callback, true, deadline)
}

func ScheduleSync(callback func(), deadline time.Time) SchedKey {
	return schedule(callback, false, deadline)
}

func schedule(callback func(), async bool, deadline time.Time) SchedKey {
	if now, dead := time.Now().Unix(), deadline.Unix(); now > dead {
		// timed out already
		return 0
	} else if now == dead {
		if async {
			go callback()
		} else {
			callback()
		}
		return 0
	}

	s, m := deadline.Second(), deadline.Minute()
	ts := &timeoutWheel.secmin[s][m]
	ts.Lock()

	ts.counter++

	// sec (6bit) | min (6bit) | counter (52bit)
	// key will never be 0
	key := SchedKey(uint64(s+1)<<58 | uint64(m+1)<<52 | (ts.counter & 0xfffffffffffff))

	if ts.list == nil {
		ts.list = map[SchedKey]*notifier{}
	}

	ts.list[key] = &notifier{
		deadline: uint32(deadline.Unix()),
		callback: callback,
		async:    async,
	}

	ts.Unlock()
	return key
}

func (key SchedKey) Cancel() {
	s := int(key>>58) - 1
	m := int(key<<6>>58) - 1
	if s < 0 || s > 59 || m < 0 || m > 59 {
		return
	}
	ts := &timeoutWheel.secmin[s][m]
	ts.Lock()
	if ts.list != nil {
		//fmt.Println("+", ts.list, key)
		delete(ts.list, key)
		//fmt.Println("-", ts.list)
	}
	ts.Unlock()
}

func (key *SchedKey) Reschedule(callback func(), deadline time.Time) {
	key.reschedule(callback, true, deadline)
}

func (key *SchedKey) RescheduleSync(callback func(), deadline time.Time) {
	key.reschedule(callback, false, deadline)
}

func (key *SchedKey) reschedule(callback func(), async bool, deadline time.Time) {
RETRY:
	old := atomic.LoadUint64((*uint64)(key))
	SchedKey(old).Cancel()
	n := schedule(callback, async, deadline)
	if atomic.CompareAndSwapUint64((*uint64)(key), old, uint64(n)) {
		return
	}
	n.Cancel()
	goto RETRY
}
