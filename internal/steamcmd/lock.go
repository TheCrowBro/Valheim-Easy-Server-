package steamcmd

import (
	"context"
	"errors"
	"sync"
)

// Only one SteamCMD runs at a time in the whole app: every game shares the
// one in <data>/steamcmd, which updates itself and keeps a single package
// cache, so two at once would trip over each other. The lock covers all of
// an Install, including the first download of SteamCMD itself.
//
// An install or repair waits for its turn. A start's update doesn't: a
// friend waiting to play shouldn't sit behind another game's hour-long
// download, so it skips the update and the game starts with the build it
// already has (spec 2.3).

// ErrBusy is what errors.Is finds in the error an Install returns when it
// skipped because another download holds SteamCMD (see SkipIfBusy).
var ErrBusy = errors.New("SteamCMD is busy with another download")

// BusyError says which game's download is holding SteamCMD. Its text is
// made to fit the manager's warning: "Couldn't check for server updates
// (Satisfactory is downloading). Starting the version that's installed."
type BusyError struct {
	Name string // the game being downloaded
}

func (e *BusyError) Error() string { return e.Name + " is downloading" }

// Is makes errors.Is(err, ErrBusy) true.
func (e *BusyError) Is(target error) bool { return target == ErrBusy }

type skipKey struct{}

// SkipIfBusy marks ctx so an Install under it never waits for another
// game's SteamCMD download: it returns a *BusyError straight away instead.
// A start's update-on-start uses it; installs and repairs don't.
func SkipIfBusy(ctx context.Context) context.Context {
	return context.WithValue(ctx, skipKey{}, true)
}

func skipsIfBusy(ctx context.Context) bool {
	skip, _ := ctx.Value(skipKey{}).(bool)
	return skip
}

// steamLock is the app-wide SteamCMD lock. A sync.Mutex can't be waited
// for with a cancel, so free does that job: it is closed whenever the lock
// is let go, waking everyone who waits, and replaced by a new one.
var steamLock = struct {
	mu     sync.Mutex
	held   bool
	holder string
	free   chan struct{}
}{free: make(chan struct{})}

// lockSteam takes the SteamCMD lock for the game called name. When another
// game holds it, it either returns a *BusyError (wait false) or shows
// "Waiting for the <game> download to finish…" and waits for its turn or
// for ctx to end.
func lockSteam(ctx context.Context, name string, wait bool, progress func(Progress)) (unlock func(), err error) {
	shown := ""
	for {
		steamLock.mu.Lock()
		if !steamLock.held {
			steamLock.held = true
			steamLock.holder = name
			steamLock.mu.Unlock()
			return unlockSteam, nil
		}
		holder, free := steamLock.holder, steamLock.free
		steamLock.mu.Unlock()
		if !wait {
			return nil, &BusyError{Name: holder}
		}
		if holder != shown {
			// The holder can change while we wait (it finishes and a third
			// game gets in first), so the text follows it.
			progress(Progress{Step: "Waiting for the " + holder + " download to finish…", Percent: -1})
			shown = holder
		}
		select {
		case <-free:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func unlockSteam() {
	steamLock.mu.Lock()
	defer steamLock.mu.Unlock()
	steamLock.held = false
	steamLock.holder = ""
	close(steamLock.free)
	steamLock.free = make(chan struct{})
}
