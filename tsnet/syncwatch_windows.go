//go:build windows

package tsnet

import (
	"errors"
	"io/fs"
	"path/filepath"
	"sync"
	"unsafe"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sys/windows"
)

// SyncWatchDefault is the default of -sync-watch: true when watching is cheap.
const SyncWatchDefault = true

// The errors of a watches or open files limit (see isLimit).
var (
	errNoSpace      error = windows.ERROR_NOT_ENOUGH_QUOTA
	errTooManyFiles error = windows.ERROR_TOO_MANY_OPEN_FILES
)

// watcher watches the whole folder with one recursive ReadDirectoryChangesW on its root (the first directory
// added: the others are then already watched). fsnotify keeps a handle open on each watched directory, which
// prevents renaming or removing the directories containing them (fsnotify issue #356).
type watcher struct {
	Events  chan fsnotify.Event
	Errors  chan error
	quit    chan struct{}
	quitEv  windows.Handle // signaled with quit, to stop waiting for changes.
	done    chan struct{}
	added   bool
	running bool
	once    sync.Once
}

func newWatcher() (*watcher, error) {
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, err
	}
	return &watcher{
		Events: make(chan fsnotify.Event), Errors: make(chan error), quit: make(chan struct{}),
		quitEv: ev, done: make(chan struct{}),
	}, nil
}

// Add watches the tree of the first path added, the next ones are in it.
func (w *watcher) Add(path string) error {
	if w.added {
		return nil
	}
	w.added = true
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return &fs.PathError{Op: "watch", Path: path, Err: err}
	}
	ioEv, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(h)
		return err
	}
	w.running = true
	go w.run(path, h, ioEv)
	return nil
}

// Remove does nothing: removed directories are no longer notified.
func (w *watcher) Remove(string) error {
	return nil
}

func (w *watcher) Close() error {
	w.once.Do(func() {
		close(w.quit)
		_ = windows.SetEvent(w.quitEv)
		if w.running {
			<-w.done
		}
		_ = windows.CloseHandle(w.quitEv)
	})
	return nil
}

// run reads the changes till closed or an error (then closes the channels: notifications stopped).
func (w *watcher) run(root string, h, ioEv windows.Handle) {
	defer close(w.done)
	defer func() {
		_ = windows.CloseHandle(h)
		_ = windows.CloseHandle(ioEv)
		close(w.Events)
		close(w.Errors)
	}()
	const mask = windows.FILE_NOTIFY_CHANGE_FILE_NAME | windows.FILE_NOTIFY_CHANGE_DIR_NAME |
		windows.FILE_NOTIFY_CHANGE_ATTRIBUTES | windows.FILE_NOTIFY_CHANGE_SIZE |
		windows.FILE_NOTIFY_CHANGE_LAST_WRITE | windows.FILE_NOTIFY_CHANGE_CREATION
	const bufSize = 64 << 10 // the max for network shares.
	buf := make([]byte, bufSize)
	for {
		ov := windows.Overlapped{HEvent: ioEv}
		var n uint32
		err := windows.ReadDirectoryChanges(h, &buf[0], bufSize, true, mask, nil, &ov, 0)
		if err == nil {
			i, werr := windows.WaitForMultipleObjects([]windows.Handle{ioEv, w.quitEv}, false, windows.INFINITE)
			if werr != nil || i != windows.WAIT_OBJECT_0 { // closed.
				_ = windows.CancelIoEx(h, &ov)
				_ = windows.GetOverlappedResult(h, &ov, &n, true)
				return
			}
			err = windows.GetOverlappedResult(h, &ov, &n, false)
		}
		if errors.Is(err, windows.ERROR_NOTIFY_ENUM_DIR) || (err == nil && n == 0) { // changes lost.
			if !w.sendErr(fsnotify.ErrEventOverflow) {
				return
			}
			continue
		}
		if err != nil { // eg the root removed.
			w.sendErr(err)
			return
		}
		for off := uint32(0); ; {
			fi := (*windows.FileNotifyInformation)(unsafe.Pointer(&buf[off]))
			name := windows.UTF16ToString(unsafe.Slice(&fi.FileName, fi.FileNameLength/2))
			if !w.send(fsnotify.Event{Name: filepath.Join(root, name), Op: notifyOp(fi.Action)}) {
				return
			}
			if fi.NextEntryOffset == 0 {
				break
			}
			off += fi.NextEntryOffset
		}
	}
}

func notifyOp(action uint32) fsnotify.Op {
	switch action {
	case windows.FILE_ACTION_ADDED, windows.FILE_ACTION_RENAMED_NEW_NAME:
		return fsnotify.Create
	case windows.FILE_ACTION_REMOVED:
		return fsnotify.Remove
	case windows.FILE_ACTION_RENAMED_OLD_NAME:
		return fsnotify.Rename
	default:
		return fsnotify.Write
	}
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
