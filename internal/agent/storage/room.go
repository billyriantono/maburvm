package storage

import "sync"

// roomHeld is free space promised to backups running now, by file system
// (mount point): each one's room check saw the same free space. A backup-API
// copy (big) is as large as the VM's disks; an overlay export needs one
// compressed disk.
// ponytail: a promise is held in full until the backup ends, though what it
// already wrote is also gone from the free space; it errs on refusing.
var roomHeld = struct {
	sync.Mutex
	big, small map[string]int64
}{big: map[string]int64{}, small: map[string]int64{}}

// HoldRoom promises need bytes of fs, whose free space is avail, when what is
// left after the promises already made still covers need plus reserve. A big
// copy must fit beside every promise; an overlay export only beside the big
// ones', as before they existed. release gives the promise back; calling it
// again does nothing.
func HoldRoom(fs string, avail, need, reserve int64, big bool) (release func(), ok bool) {
	roomHeld.Lock()
	defer roomHeld.Unlock()
	promised := roomHeld.big[fs]
	if big {
		promised += roomHeld.small[fs]
	}
	if avail-promised < need+reserve {
		return nil, false
	}
	held := roomHeld.small
	if big {
		held = roomHeld.big
	}
	held[fs] += need
	var once sync.Once
	return func() {
		once.Do(func() {
			roomHeld.Lock()
			defer roomHeld.Unlock()
			if held[fs] -= need; held[fs] <= 0 {
				delete(held, fs)
			}
		})
	}, true
}

// HeldRoom is the room promised on fs now.
func HeldRoom(fs string) int64 {
	roomHeld.Lock()
	defer roomHeld.Unlock()
	return roomHeld.big[fs] + roomHeld.small[fs]
}
