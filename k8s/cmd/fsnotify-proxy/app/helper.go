package app

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"bytetrade.io/web3os/fs-lib/jfsnotify"
	"bytetrade.io/web3os/fs-lib/k8s/pkg/multicast"
	"k8s.io/klog/v2"
)

const writeDebounce = time.Second

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

// delayedWrite holds a resettable debounce timer and the latest WRITE snapshot.
type delayedWrite struct {
	timer *time.Timer
	event jfsnotify.Event
	gen   uint64 // bumped on each arm; stale AfterFunc callbacks ignore mismatch
}

type watcher struct {
	multicast.MsgWriter
	client         *multicast.Client
	podPathMap     map[string]string // { pathInNode: pathInPod }
	mu             sync.RWMutex
	Remove         func()
	CRName         string
	CRNamespace    string
	ChannelID      string // "ns/pod/container"
	PodNS          string
	PodName        string
	Container      string
	delayedWrites  map[string]*delayedWrite
}

func (w *watcher) clientCID() string {
	if w == nil || w.client == nil {
		return ""
	}
	return w.client.CID()
}

func (w *watcher) logTarget(prefix string, event *jfsnotify.Event) {
	klog.Infof("%s %v cid=%s channel=%s pod=%s/%s container=%s cr=%s/%s",
		prefix, event, w.clientCID(), w.ChannelID, w.PodNS, w.PodName, w.Container, w.CRNamespace, w.CRName)
}

func NewWatcher(c *multicast.Client) *watcher {
	return &watcher{
		client:        c,
		podPathMap:    make(map[string]string),
		delayedWrites: make(map[string]*delayedWrite),
	}
}

func (w *watcher) WriteMsg(msg string) error {
	events, err := w.parseMsg(msg)
	if err != nil {
		return err
	}

	sendEvent := func(event *jfsnotify.Event) error {
		w.logTarget("translate msg to watcher,", event)
		data, err := w.translateEventNameInCluster(event)
		if err != nil {
			return err
		}

		if data == nil {
			// event path not mapped for this watcher
			klog.Infof("unmap_skip event for watcher channel=%s cid=%s key=%s name=%s",
				w.ChannelID, w.clientCID(), event.Key, event.Name)
			return nil
		}

		w.logTarget("send msg to watcher,", event)
		err = w.client.SendBytes(data)
		if err != nil {
			return err
		}

		return nil
	}

	for _, event := range events {
		switch event.Op {
		case jfsnotify.Write:
			w.armWriteDebounce(*event, sendEvent)
		default:
			return sendEvent(event)
		}
	}

	return nil
}

// armWriteDebounce (W2): on every WRITE, store the latest event and (re)arm a 1s timer.
func (w *watcher) armWriteDebounce(event jfsnotify.Event, sendEvent func(*jfsnotify.Event) error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	name := event.Name
	d, exists := w.delayedWrites[name]
	if !exists {
		d = &delayedWrite{}
		w.delayedWrites[name] = d
	}
	if d.timer != nil {
		d.timer.Stop()
	}
	d.event = event
	d.gen++
	gen := d.gen
	d.timer = time.AfterFunc(writeDebounce, func() {
		w.flushWriteDebounce(name, gen, sendEvent)
	})

	if exists {
		klog.Infof("debounce_reset cid=%s channel=%s name=%s", w.clientCID(), w.ChannelID, name)
	} else {
		klog.Infof("debounce_arm cid=%s channel=%s name=%s", w.clientCID(), w.ChannelID, name)
	}
}

// flushWriteDebounce (W3): after quiet period, send the latest WRITE snapshot and clear state.
func (w *watcher) flushWriteDebounce(name string, gen uint64, sendEvent func(*jfsnotify.Event) error) {
	w.mu.Lock()
	d, ok := w.delayedWrites[name]
	if !ok || d.gen != gen {
		w.mu.Unlock()
		return
	}
	ev := d.event
	delete(w.delayedWrites, name)
	w.mu.Unlock()

	klog.Infof("debounce_flush cid=%s channel=%s name=%s", w.clientCID(), w.ChannelID, name)
	if err := sendEvent(&ev); err != nil {
		klog.Error("send write event error, ", err, ", ", name)
	}
}

func (w *watcher) Close() {
	w.mu.Lock()
	n := len(w.delayedWrites)
	for name, d := range w.delayedWrites {
		if d != nil && d.timer != nil {
			d.timer.Stop()
		}
		delete(w.delayedWrites, name)
	}
	w.mu.Unlock()
	if n > 0 {
		klog.Infof("debounce_cancel_all cid=%s channel=%s count=%d", w.clientCID(), w.ChannelID, n)
	}

	if w.Remove != nil {
		w.Remove()
	}
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
