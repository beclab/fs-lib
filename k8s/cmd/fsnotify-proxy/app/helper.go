package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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

type watcher struct {
	multicast.MsgWriter
	client         *multicast.Client
	podPathMap     map[string]string // { pathInNode: pathInPod }
	mu             sync.RWMutex
	Remove         func()
	CRName         string
	CRNamespace    string
	delayWriteMsgs map[string]time.Time
}

func NewWatcher(c *multicast.Client) *watcher {
	return &watcher{
		client:         c,
		podPathMap:     make(map[string]string),
		delayWriteMsgs: make(map[string]time.Time),
	}
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
			if func() bool {
				w.mu.Lock()
				defer w.mu.Unlock()
				_, ok := w.delayWriteMsgs[event.Name]
				w.delayWriteMsgs[event.Name] = time.Now()
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
	deley := time.NewTimer(time.Second)
	go func() {
		<-deley.C
		if t, ok := w.delayWriteMsgs[localEvent.Name]; ok && time.Since(t) < time.Second {
			w.send(localEvent, sendEvent)
			return
		}
		err := sendEvent(&localEvent)
		if err != nil {
			klog.Error("send write event error, ", err, ", ", localEvent.Name)
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		delete(w.delayWriteMsgs, localEvent.Name)
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
		return nil, fmt.Errorf("event key not found, %s", event.Key)
	}

	event.Name = strings.Replace(event.Name, event.Key, keyInPod, 1)

	return jfsnotify.PackageMsg(jfsnotify.MSG_EVENT, jfsnotify.PackEvent(event)), nil
}
