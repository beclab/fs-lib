package app

import (
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bytetrade.io/web3os/fs-lib/jfsnotify"
	"bytetrade.io/web3os/fs-lib/k8s/pkg/multicast"
	"k8s.io/klog/v2"
)

type DebugRWMutex struct {
	mu sync.RWMutex
}

func (d *DebugRWMutex) Lock() {
	klog.Info("Mutex Lock")
	d.mu.Lock()
}

func (d *DebugRWMutex) Unlock() {
	klog.Info("Mutex Unlock")
	d.mu.Unlock()
}

func (d *DebugRWMutex) RLock() {
	klog.Info("Mutex RLock")
	d.mu.RLock()
}

func (d *DebugRWMutex) RUnlock() {
	klog.Info("Mutex RUnlock")
	d.mu.RUnlock()
}

// debounceWaiterGen: diagnostic ids only.
var debounceWaiterGen atomic.Uint64

type watcher struct {
	multicast.MsgWriter
	client         *multicast.Client
	podPathMap     map[string]string // { pathInNode: pathInPod }
	mu             sync.RWMutex
	Remove         func()
	CRName         string
	CRNamespace    string
	delayWriteMsgs map[string]time.Time
	// delayWriteWaiters is diagnostics-only (live send() goroutine count). Not used to decide start/refresh.
	delayWriteWaiters map[string]int
}

func NewWatcher(c *multicast.Client) *watcher {
	return &watcher{
		client:            c,
		podPathMap:        make(map[string]string),
		delayWriteMsgs:    make(map[string]time.Time),
		delayWriteWaiters: make(map[string]int),
	}
}

func (w *watcher) diagID() string {
	return fmt.Sprintf("cr=%s/%s", w.CRNamespace, w.CRName)
}

func (w *watcher) WriteMsg(msg string) error {
	events, err := w.parseMsg(msg)
	if err != nil {
		return err
	}

	sendEvent := func(event *jfsnotify.Event) error {
		klog.Info("translate msg to watcher, ", event)
		data, err := w.translateEventNameInCluster(event)
		if err != nil {
			return err
		}

		if data == nil {
			// event not in this pod
			return nil
		}

		klog.Info("send msg to watcher, ", event)
		err = w.client.SendBytes(data)
		if err != nil {
			return err
		}

		return nil
	}

	for _, event := range events {
		switch event.Op {
		case jfsnotify.Write:
			// Original control flow unchanged: only first insert starts timer.
			if func() bool {
				w.mu.Lock()
				defer w.mu.Unlock()
				_, ok := w.delayWriteMsgs[event.Name]
				waiters := w.delayWriteWaiters[event.Name] // diag only
				w.delayWriteMsgs[event.Name] = time.Now()
				if !ok {
					klog.Infof("WriteMsg branch=debounce_start %s name=%s waiters=%d",
						w.diagID(), event.Name, waiters)
				} else if waiters <= 0 {
					klog.Errorf("ORPHAN_REFRESH %s name=%s waiters=%d (map has key but no waiter goroutine)",
						w.diagID(), event.Name, waiters)
				} else {
					klog.Infof("WriteMsg branch=debounce_refresh %s name=%s waiters=%d",
						w.diagID(), event.Name, waiters)
				}
				return !ok
			}() {
				w.send(*event, sendEvent)
			}

		default:
			return sendEvent(event)
		}
	}

	return nil
}

func (w *watcher) send(localEvent jfsnotify.Event, sendEvent func(e *jfsnotify.Event) error) {
	name := localEvent.Name
	waiterID := debounceWaiterGen.Add(1)

	// Diagnostics: track live waiter count (does not affect start/refresh decision).
	w.mu.Lock()
	w.delayWriteWaiters[name]++
	waiters := w.delayWriteWaiters[name]
	w.mu.Unlock()
	klog.Infof("debounce_waiter_start %s name=%s waiter_id=%d waiters=%d",
		w.diagID(), name, waiterID, waiters)

	deley := time.NewTimer(time.Second)
	go func() {
		exitReason := "unknown"
		defer func() {
			// Log panic then re-panic so process behavior matches original (unrecovered panic).
			if r := recover(); r != nil {
				exitReason = fmt.Sprintf("panic:%v", r)
				klog.Errorf("debounce_waiter_panic %s name=%s waiter_id=%d recover=%v stack=%s",
					w.diagID(), name, waiterID, r, string(debug.Stack()))

				w.mu.Lock()
				w.delayWriteWaiters[name]--
				left := w.delayWriteWaiters[name]
				if left <= 0 {
					delete(w.delayWriteWaiters, name)
					left = 0
				}
				_, mapStill := w.delayWriteMsgs[name]
				w.mu.Unlock()
				klog.Infof("debounce_waiter_exit %s name=%s waiter_id=%d reason=%s waiters_left=%d map_still=%v",
					w.diagID(), name, waiterID, exitReason, left, mapStill)
				if left == 0 && mapStill {
					klog.Errorf("ORPHAN_CREATED %s name=%s waiter_id=%d reason=%s",
						w.diagID(), name, waiterID, exitReason)
				}
				panic(r)
			}

			w.mu.Lock()
			w.delayWriteWaiters[name]--
			left := w.delayWriteWaiters[name]
			if left <= 0 {
				delete(w.delayWriteWaiters, name)
				left = 0
			}
			_, mapStill := w.delayWriteMsgs[name]
			w.mu.Unlock()
			klog.Infof("debounce_waiter_exit %s name=%s waiter_id=%d reason=%s waiters_left=%d map_still=%v",
				w.diagID(), name, waiterID, exitReason, left, mapStill)
			if left == 0 && mapStill {
				klog.Errorf("ORPHAN_CREATED %s name=%s waiter_id=%d reason=%s",
					w.diagID(), name, waiterID, exitReason)
			}
		}()

		<-deley.C
		// Original: unlocked map read (intentionally preserved for fidelity).
		if t, ok := w.delayWriteMsgs[localEvent.Name]; ok && time.Since(t) < time.Second {
			exitReason = "reschedule"
			klog.Infof("debounce_waiter_wake %s name=%s waiter_id=%d map_ok=%v since=%v reschedule=true",
				w.diagID(), name, waiterID, ok, time.Since(t))
			klog.Infof("write_debounce_fire action=reschedule %s name=%s waiter_id=%d",
				w.diagID(), name, waiterID)
			w.send(localEvent, sendEvent)
			return
		}

		var sinceDbg time.Duration
		tDbg, okDbg := w.delayWriteMsgs[localEvent.Name]
		if okDbg {
			sinceDbg = time.Since(tDbg)
		}
		klog.Infof("debounce_waiter_wake %s name=%s waiter_id=%d map_ok=%v since=%v reschedule=false",
			w.diagID(), name, waiterID, okDbg, sinceDbg)
		klog.Infof("write_debounce_fire action=send %s name=%s waiter_id=%d",
			w.diagID(), name, waiterID)

		exitReason = "flush"
		klog.Infof("debounce_flush_enter %s name=%s waiter_id=%d", w.diagID(), name, waiterID)
		err := sendEvent(&localEvent)
		if err != nil {
			exitReason = "flush_err"
			klog.Error("send write event error, ", err, ", ", localEvent.Name)
		} else {
			exitReason = "flush_ok"
		}
		klog.Infof("debounce_flush_done %s name=%s waiter_id=%d err=%v", w.diagID(), name, waiterID, err)

		w.mu.Lock()
		defer w.mu.Unlock()
		delete(w.delayWriteMsgs, localEvent.Name)
		klog.Infof("debounce_map_delete %s name=%s waiter_id=%d", w.diagID(), name, waiterID)
	}()

}

func (w *watcher) Close() {
	w.Remove()
}

func (w *watcher) parseMsg(msg string) ([]*jfsnotify.Event, error) {
	var event []*jfsnotify.Event
	err := json.Unmarshal([]byte(msg), &event)
	if err != nil {
		klog.Error("json decode msg error, ", err)
		return nil, err
	}

	return event, nil
}

func (w *watcher) translateEventNameInCluster(event *jfsnotify.Event) ([]byte, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	keyInPod, ok := w.podPathMap[event.Key]
	if !ok {
		return nil, nil
	}

	event.Name = strings.Replace(event.Name, event.Key, keyInPod, 1)

	return jfsnotify.PackageMsg(jfsnotify.MSG_EVENT, jfsnotify.PackEvent(event)), nil
}
