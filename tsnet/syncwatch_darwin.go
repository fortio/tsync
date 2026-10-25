//go:build cgo

package tsnet

/*
#cgo LDFLAGS: -framework CoreServices
#include <CoreServices/CoreServices.h>
#include <dispatch/dispatch.h>
#include <stdlib.h>

extern void tsyncFSEvents(uintptr_t h, size_t n, char **paths, FSEventStreamEventFlags *flags);

static void tsyncCallback(ConstFSEventStreamRef s, void *info, size_t n, void *paths,
	const FSEventStreamEventFlags flags[], const FSEventStreamEventId ids[]) {
	tsyncFSEvents((uintptr_t)info, n, (char **)paths, (FSEventStreamEventFlags *)flags);
}

static FSEventStreamRef tsyncWatch(const char *path, uintptr_t h, dispatch_queue_t *q) {
	CFStringRef p = CFStringCreateWithCString(NULL, path, kCFStringEncodingUTF8);
	if (p == NULL) {
		return NULL;
	}
	CFArrayRef paths = CFArrayCreate(NULL, (const void **)&p, 1, &kCFTypeArrayCallBacks);
	CFRelease(p);
	FSEventStreamContext ctx = {.info = (void *)h};
	FSEventStreamRef s = FSEventStreamCreate(NULL, tsyncCallback, &ctx, paths, kFSEventStreamEventIdSinceNow, 0.05,
		kFSEventStreamCreateFlagFileEvents | kFSEventStreamCreateFlagNoDefer | kFSEventStreamCreateFlagWatchRoot);
	CFRelease(paths);
	if (s == NULL) {
		return NULL;
	}
	*q = dispatch_queue_create("org.fortio.tsync.watch", DISPATCH_QUEUE_SERIAL);
	FSEventStreamSetDispatchQueue(s, *q);
	if (!FSEventStreamStart(s)) {
		FSEventStreamInvalidate(s);
		FSEventStreamRelease(s);
		dispatch_release(*q);
		return NULL;
	}
	return s;
}

static void tsyncNoop(void *ctx) {
}

static void tsyncUnwatch(FSEventStreamRef s, dispatch_queue_t q) {
	FSEventStreamStop(s);
	FSEventStreamInvalidate(s);
	dispatch_sync_f(q, NULL, tsyncNoop); // the callbacks in flight are done.
	FSEventStreamRelease(s);
	dispatch_release(q);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"runtime/cgo"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/fsnotify/fsnotify"
)

// SyncWatchDefault is the default of -sync-watch: true when watching is cheap (FSEvents here).
const SyncWatchDefault = true

// The errors of a watches or open files limit (see isLimit).
var (
	errNoSpace      error = syscall.ENOSPC
	errTooManyFiles error = syscall.EMFILE
)

// watcher watches the whole folder with one recursive FSEvents stream on its root (the first directory added:
// the others are then already watched), rather than fsnotify's kqueue which needs a file descriptor per file
// (built without cgo, see syncwatch_unix.go).
type watcher struct {
	Events  chan fsnotify.Event
	Errors  chan error
	quit    chan struct{}
	root    string // as given.
	real    string // as notified (symlinks resolved, eg /var is /private/var).
	added   bool
	stopped bool // after an error that stops the notifications (channels closed, only used by the callbacks).
	stream  C.FSEventStreamRef
	queue   C.dispatch_queue_t
	handle  cgo.Handle
	once    sync.Once
}

func newWatcher() (*watcher, error) {
	return &watcher{Events: make(chan fsnotify.Event), Errors: make(chan error), quit: make(chan struct{})}, nil
}

// Add watches the tree of the first path added, the next ones are in it.
func (w *watcher) Add(path string) error {
	if w.added {
		return nil
	}
	w.added = true
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	rp, err := C.realpath(cpath, nil)
	if rp == nil {
		return &fs.PathError{Op: "watch", Path: path, Err: err}
	}
	w.root, w.real = path, C.GoString(rp)
	C.free(unsafe.Pointer(rp))
	w.handle = cgo.NewHandle(w)
	w.stream = C.tsyncWatch(cpath, C.uintptr_t(w.handle), &w.queue) //nolint:gocritic // in the cgo generated code.
	if w.stream == nil {
		w.handle.Delete()
		return &fs.PathError{Op: "watch", Path: path, Err: errors.New("can't start the FSEvents stream")}
	}
	return nil
}

// Remove does nothing: removed directories are no longer notified.
func (w *watcher) Remove(string) error {
	return nil
}

func (w *watcher) Close() error {
	w.once.Do(func() {
		close(w.quit)
		if w.stream != nil {
			C.tsyncUnwatch(w.stream, w.queue)
			w.handle.Delete()
		}
	})
	return nil
}

//export tsyncFSEvents
func tsyncFSEvents(h C.uintptr_t, n C.size_t, paths **C.char, flags *C.FSEventStreamEventFlags) {
	w := cgo.Handle(h).Value().(*watcher)
	if w.stopped {
		return
	}
	for i := range int(n) {
		p := filepath.Clean(C.GoString(unsafe.Slice(paths, n)[i]))
		f := unsafe.Slice(flags, n)[i]
		switch {
		case f&C.kFSEventStreamEventFlagRootChanged != 0: // moved or removed.
			w.sendErr(fmt.Errorf("%s moved or removed", w.root))
			w.stopped = true
			close(w.Events)
			close(w.Errors)
			return
		case f&(C.kFSEventStreamEventFlagMustScanSubDirs|C.kFSEventStreamEventFlagUserDropped|
			C.kFSEventStreamEventFlagKernelDropped|C.kFSEventStreamEventFlagMount|
			C.kFSEventStreamEventFlagUnmount) != 0:
			if !w.sendErr(fsnotify.ErrEventOverflow) {
				return
			}
			continue
		}
		name, ok := w.rel(p)
		if !ok {
			continue
		}
		if !w.send(fsnotify.Event{Name: name, Op: eventOp(f)}) {
			return
		}
	}
}

// rel returns the path notified as under the root given (rather than the real one).
func (w *watcher) rel(p string) (string, bool) {
	if p == w.real {
		return w.root, true
	}
	if rest, ok := strings.CutPrefix(p, w.real+"/"); ok {
		return filepath.Join(w.root, rest), true
	}
	return "", false
}

// eventOp returns the operations of the (coalesced) flags.
func eventOp(f C.FSEventStreamEventFlags) fsnotify.Op {
	var op fsnotify.Op
	if f&C.kFSEventStreamEventFlagItemCreated != 0 {
		op |= fsnotify.Create
	}
	if f&C.kFSEventStreamEventFlagItemRemoved != 0 {
		op |= fsnotify.Remove
	}
	if f&C.kFSEventStreamEventFlagItemRenamed != 0 {
		op |= fsnotify.Rename
	}
	if f&(C.kFSEventStreamEventFlagItemModified|C.kFSEventStreamEventFlagItemInodeMetaMod) != 0 {
		op |= fsnotify.Write
	}
	if op == 0 {
		op = fsnotify.Chmod // eg owner or extended attributes.
	}
	return op
}

// send returns false once closed.
func (w *watcher) send(ev fsnotify.Event) bool {
	select {
	case w.Events <- ev:
		return true
	case <-w.quit:
		return false
	}
}

func (w *watcher) sendErr(err error) bool {
	select {
	case w.Errors <- err:
		return true
	case <-w.quit:
		return false
	}
}
