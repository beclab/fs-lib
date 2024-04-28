package jfsnotify

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/smallnest/goframe"
	"k8s.io/klog/v2"
)

// to use the jfsnotify watcher in k8s cluster
// export POD_NAME and NAMESPACE in env

type Watcher struct {
	// Events sends the filesystem change events.
	//
	// fsnotify can send the following events; a "path" here can refer to a
	// file, directory, symbolic link, or special file like a FIFO.
	//
	//   fsnotify.Create    A new path was created; this may be followed by one
	//                      or more Write events if data also gets written to a
	//                      file.
	//
	//   fsnotify.Remove    A path was removed.
	//
	//   fsnotify.Rename    A path was renamed. A rename is always sent with the
	//                      old path as Event.Name, and a Create event will be
	//                      sent with the new name. Renames are only sent for
	//                      paths that are currently watched; e.g. moving an
	//                      unmonitored file into a monitored directory will
	//                      show up as just a Create. Similarly, renaming a file
	//                      to outside a monitored directory will show up as
	//                      only a Rename.
	//
	//   fsnotify.Write     A file or named pipe was written to. A Truncate will
	//                      also trigger a Write. A single "write action"
	//                      initiated by the user may show up as one or multiple
	//                      writes, depending on when the system syncs things to
	//                      disk. For example when compiling a large Go program
	//                      you may get hundreds of Write events, so you
	//                      probably want to wait until you've stopped receiving
	//                      them (see the dedup example in cmd/fsnotify).
	//                      Some systems may send Write event for directories
	//                      when the directory content changes.
	//
	//   fsnotify.Chmod     Attributes were changed. On Linux this is also sent
	//                      when a file is removed (or more accurately, when a
	//                      link to an inode is removed). On kqueue it's sent
	//                      and on kqueue when a file is truncated. On Windows
	//                      it's never sent.
	Events chan Event

	// Errors sends any errors.
	//
	// [ErrEventOverflow] is used to indicate there are too many events:
	//
	//  - inotify: there are too many queued events (fs.inotify.max_queued_events sysctl)
	//  - windows: The buffer size is too small; [WithBufferSize] can be used to increase it.
	//  - kqueue, fen: not used.
	Errors chan error

	name     string
	watches  []string
	fconn    goframe.FrameConn
	ctx      context.Context
	close    context.CancelFunc
	initTask chan func()
	mu       sync.RWMutex
	sendQ    chan []byte

	reconnect chan int
	writeMu   sync.Mutex
}

var (
	podName       = "default"
	podNamespace  = "default"
	serverAddr    = "fsnotify-svc.os-system:1506"
	containerName = "default"
)

func init() {
	getEnv := func(v *string, ns ...string) {
		for _, n := range ns {
			if e := os.Getenv(n); e != "" {
				*v = e
				return
			}
		}
	}

	getEnv(&podName, "POD_NAME", "HOSTNAME")
	getEnv(&podNamespace, "NAMESPACE")
	getEnv(&serverAddr, "NOTIFY_SERVER")
	getEnv(&containerName, "CONTAINER_NAME")

	klog.InitFlags(nil)
}

// NewWatcher creates a new Watcher.
func NewWatcher(name string) (*Watcher, error) {
	if strings.Contains(name, "/") {
		return nil, errors.New("bad watcher name, illegal character '/'")
	}

	w := &Watcher{
		Events:    make(chan Event),
		Errors:    make(chan error),
		initTask:  make(chan func(), 10),
		reconnect: make(chan int, 10),
		sendQ:     make(chan []byte, 255),

		name: fmt.Sprintf("%s/%s/%s", podName, containerName, name),
	}

	w.ctx, w.close = context.WithCancel(context.Background())

	go w.start()

	return w, nil
}

func (w *Watcher) start() {
	klog.Info("start watcher")
	w.initTask <- func() {
		if w.fconn != nil {
			klog.Info("send init clear signal")
			n := make([]byte, 255)
			copy(n, []byte(podNamespace+"/"+w.name))
			data := PackageMsg(MSG_CLEAR, n)
			err := w.syncWrite(data) // send clear first when startup
			if err != nil {
				klog.Error("send clear error, ", err)
				if isSocketError(err) {
					w.reconnect <- 1
				}
			}
		}
	}

	addWatch := func() {
		w.mu.RLock()
		defer w.mu.RUnlock()
		if w.fconn != nil && len(w.watches) > 0 {
			err := w.sendWatch(w.watches)
			if err != nil {
				klog.Error("send watches error, ", err)
			}
		}
	}
	for {
		select {
		case <-w.ctx.Done():
			w.clear()
			return
		default:
			// start
			klog.Info("reconnecting to ", serverAddr)
			ptype, addr := parseDialTarget(serverAddr)
			var (
				conn net.Conn
				err  error
			)
			if ptype == "ipc" {
				conn, err = NewIpcConn(addr)
			} else {
				conn, err = net.Dial(ptype, addr)
			}
			if err != nil {
				klog.Error("connect to ", serverAddr, " error, ", err)
				time.Sleep(time.Second)
			} else {
				klog.Info("success to connect ", serverAddr)

				encoderConfig := goframe.EncoderConfig{
					ByteOrder:                       binary.BigEndian,
					LengthFieldLength:               4,
					LengthAdjustment:                0,
					LengthIncludesLengthFieldLength: false,
				}

				decoderConfig := goframe.DecoderConfig{
					ByteOrder:           binary.BigEndian,
					LengthFieldOffset:   0,
					LengthFieldLength:   4,
					LengthAdjustment:    0,
					InitialBytesToStrip: 4,
				}

				w.fconn = goframe.NewLengthFieldBasedFrameConn(encoderConfig, decoderConfig, conn)
				w.initTask <- addWatch // re-send all watches when reconnected

				// read
				go func() {
					for {
						select {
						case <-w.ctx.Done():
							return
						default:
							event, err := w.fconn.ReadFrame()
							if err != nil {
								if isSocketError(err) {
									w.reconnect <- 1
									return
								}

								w.Errors <- err
							} else {
								mt, md, err := UnpackMsg(event)
								if err != nil {
									klog.Error("unpack msg error, ", err, ", ", string(event))
								} else if mt != MSG_EVENT {
									klog.Error("unexpected msg type, ", mt)
								} else {
									if evs, err := UnpackEvent(md); err != nil {
										klog.Error("unpack event error, ", err, ", ", string(md))
									} else {
										for _, ev := range evs {
											w.Events <- *ev
										}
									}
								}
							}
						}
					}
				}()

				// start send routine.
				go func() {
					for {
						if w.fconn == nil {
							time.Sleep(time.Second)
							continue
						}

						select {
						case <-w.ctx.Done():
							return
						case d := <-w.sendQ:
							if d != nil {
								err := w.syncWrite(d)

								if err != nil {
									if isSocketError(err) {
										w.reconnect <- 1
									}

									w.Errors <- err
								}
							}
						}
					}
				}()

				// batch task
				func() {
					for {
						select {
						case t := <-w.initTask:
							t()
						default:
							return
						}
					}
				}()

				<-w.reconnect
				// clear all signal before starting to reconnect
				func() {
					for {
						select {
						case <-w.reconnect:
						default:
							if w.fconn != nil {
								klog.Info("close the connection, try to reconnect")
								w.fconn.Close()
								w.fconn = nil
							}
							return
						}
					}
				}()
			} // end connected
		}
	}
}

func (w *Watcher) sendWatch(watches []string) error {
	return w.send(MSG_WATCH, watches)
}

func (w *Watcher) sendUnwatch(watches []string) error {
	return w.send(MSG_UNWATCH, watches)
}

func (w *Watcher) send(watchType int, watches []string) error {
	dataSize := (len(watches) + 1) * 255
	data := make([]byte, dataSize)
	offset := 0

	// first 255 byte, filled with watcher name
	copy(data, []byte(podNamespace+"/"+w.name))
	offset += 255

	for _, s := range watches {
		d := ([]byte)(unsafe.Slice(&data[offset], 255))
		copy(d, []byte(s))
		offset += 255
	}

	pack := PackageMsg(watchType, data)
	w.sendQ <- pack
	return nil
}

// Close removes all watches and closes the events channel.
func (w *Watcher) Close() error {
	w.close()
	return nil
}

func (w *Watcher) clear() {
	klog.Info("watcher closed, clear all")
	close(w.Errors)
	close(w.Errors)
	close(w.initTask)
	close(w.reconnect)
	close(w.sendQ)
}

// Add starts monitoring the path for changes.
//
// A path can only be watched once; watching it more than once is a no-op and will
// not return an error. Paths that do not yet exist on the filesystem cannot
// watched.
//
// A watch will be automatically removed if the watched path is deleted or
// renamed. The exception is the Windows backend, which doesn't remove the
// watcher on renames.
//
// Notifications on network filesystems (NFS, SMB, FUSE, etc.) or special
// filesystems (/proc, /sys, etc.) generally don't work.
//
// Returns [ErrClosed] if [Watcher.Close] was called.
//
// See [AddWith] for a version that allows adding options.
//
// # Watching directories
//
// All files in a directory are monitored, including new files that are created
// after the watcher is started. By default subdirectories are not watched (i.e.
// it's non-recursive), but if the path ends with "/..." all files and
// subdirectories are watched too.
//
// # Watching files
//
// Watching individual files (rather than directories) is generally not
// recommended as many tools update files atomically. Instead of "just" writing
// to the file a temporary file will be written to first, and if successful the
// temporary file is moved to to destination removing the original, or some
// variant thereof. The watcher on the original file is now lost, as it no
// longer exists.
//
// Instead, watch the parent directory and use Event.Name to filter out files
// you're not interested in. There is an example of this in [cmd/fsnotify/file.go].
func (w *Watcher) Add(name string) error { return w.AddWith(name) }

// AddWith is like [Add], but allows adding options. When using Add() the
// defaults described below are used.
//
// Possible options are:
//
//   - [WithBufferSize] sets the buffer size for the Windows backend; no-op on
//     other platforms. The default is 64K (65536 bytes).
func (w *Watcher) AddWith(name string, opts ...addOpt) error {
	err := w.sendWatch([]string{name})
	if err != nil {
		return err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	w.watches = append(w.watches, name)

	return nil
}

// Remove stops monitoring the path for changes.
//
// If the path was added as a recursive watch (e.g. as "/tmp/dir/...") then the
// entire recursive watch will be removed. You can use either "/tmp/dir" or
// "/tmp/dir/..." (they behave identically).
//
// You cannot remove individual files or subdirectories from recursive watches;
// e.g. Add("/tmp/path/...") and then Remove("/tmp/path/sub") will fail.
//
// For other watches directories are removed non-recursively. For example, if
// you added "/tmp/dir" and "/tmp/dir/subdir" then you will need to remove both.
//
// Removing a path that has not yet been added returns [ErrNonExistentWatch].
//
// Returns nil if [Watcher.Close] was called.
func (w *Watcher) Remove(name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	err := w.sendUnwatch([]string{name})
	if err != nil {
		return err
	}

	var newWatches []string
	for _, s := range w.watches {
		if s != name {
			newWatches = append(newWatches, s)
		}
	}

	w.watches = newWatches

	return nil
}

// WatchList returns all paths added with [Add] (and are not yet removed).
//
// Returns nil if [Watcher.Close] was called.
func (w *Watcher) WatchList() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()

	return w.watches
}

func (w *Watcher) syncWrite(d []byte) error {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()

	if w.fconn != nil {
		return w.fconn.WriteFrame(d)
	}

	return ErrClosed
}
