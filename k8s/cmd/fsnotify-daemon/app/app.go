package app

import (
	"context"
	"encoding/json"
	"strings"

	"bytetrade.io/web3os/fs-lib/jfsnotify"
	sysv1alpha1 "bytetrade.io/web3os/fs-lib/k8s/pkg/apis/sys/v1alpha1"
	fswatchers "bytetrade.io/web3os/fs-lib/k8s/pkg/fswatchers/v1alpha1"
	listers "bytetrade.io/web3os/fs-lib/k8s/pkg/generated/listers/sys/v1alpha1"
	jfsipc "bytetrade.io/web3os/fs-lib/k8s/pkg/ipc"
	"bytetrade.io/web3os/fs-lib/k8s/pkg/producer"
	ipc "github.com/james-barrow/golang-ipc"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
)

type App struct {
	ipcClient     *jfsipc.JFSClient
	producer      producer.Interface
	watcherLister listers.FSWatcherLister
}

func New(ctx context.Context, lister listers.FSWatcherLister, stopCh <-chan struct{}) (*App, error) {
	var app App
	app.watcherLister = lister

	producer, err := producer.NewRedisClient(ctx, stopCh)
	if err != nil {
		return nil, err
	}

	app.producer = producer

	jfs := jfsipc.NewJFSClient(ctx, "JuiceFS-IPC",
		app.readEvent,
		app.addWatchers,
	)

	app.ipcClient = jfs

	return &app, nil
}

func (a *App) readEvent(msg *ipc.Message) {
	klog.Info("received event, ", msg.MsgType, string(msg.Data))

	if msg.MsgType != jfsnotify.MSG_EVENT {
		klog.Error("unsupported msg type, ", msg.MsgType)
		return
	}

	if a.producer == nil {
		klog.Error("producer invalid")
		return
	}

	event, err := jfsnotify.UnpackEvent(msg.Data)
	if err != nil {
		klog.Error("unpack event error, ", err)
		return
	}

	data, err := json.Marshal(event)
	if err != nil {
		klog.Error("marshal event error, ", err)
		return
	}

	klog.Info("publish event, ", string(data))
	a.producer.Pub(producer.ChannelName, string(data))
}

func (a *App) addWatchers(client *jfsipc.JFSClient) {
	klog.Info("reconnect callback")
	// clear
	err := client.Write(&ipc.Message{MsgType: jfsnotify.MSG_CLEAR})
	if err != nil {
		klog.Error("resend all watches error, ", err)
	}
	klog.Info("clear message send to juice")

	// send watches of all pod
	watchers, err := a.watcherLister.List(labels.Everything())
	if err != nil {
		klog.Error("list watcher error, ", err)
		return
	}

	paths := make([]byte, 255)
	copy(paths, []byte("cluster"))

	pathSet := make(map[string]int)
	for _, w := range watchers {
		for _, p := range w.Spec.Paths {
			pathSet[p] = 1
		}
	}

	for p := range pathSet {
		b := make([]byte, 255)
		copy(b, []byte(p))

		paths = append(paths, b...)
	}

	err = client.Write(&ipc.Message{MsgType: jfsnotify.MSG_WATCH, Data: paths})
	if err != nil {
		klog.Error("resend all watches error, ", err)
	}
	klog.Info("watch paths send to juice")
}

func (a *App) SyncWatches(action fswatchers.Action, obj interface{}) error {
	var (
		watcher *sysv1alpha1.FSWatcher
		ok      bool
	)
	if watcher, ok = obj.(*sysv1alpha1.FSWatcher); !ok {
		klog.Error("unknown obj from controller")
		return nil
	}

	list, err := a.watcherLister.List(labels.Everything())
	if err != nil {
		return err
	}

	watchedPaths := make(map[string]int)
	var watcherSelf *sysv1alpha1.FSWatcher
	for _, l := range list {
		if l.Name == watcher.Name && l.Namespace == watcher.Namespace {
			// watcher itself
			watcherSelf = l
			continue
		}

		for _, p := range l.Spec.Paths {
			watchedPaths[p] = 1
		}
	}

	getChanId := func(watcher *sysv1alpha1.FSWatcher) string {
		id := watcher.Namespace + "/" + strings.Replace(watcher.Name, watcher.Spec.Pod+"-", watcher.Spec.Pod+"/", 1)
		id = strings.Replace(id, watcher.Spec.Container+"-", watcher.Spec.Container+"/", 1)

		return id
	}

	addWatch := func(watcher *sysv1alpha1.FSWatcher) {
		addPaths := make([]byte, 255)
		copy(addPaths, []byte(getChanId(watcher)))
		for _, p := range watcher.Spec.Paths {
			if _, ok := watchedPaths[p]; !ok {
				b := make([]byte, 255)
				copy(b, []byte(p))
				addPaths = append(addPaths, b...)
			}
		}

		if len(addPaths) > 0 {
			klog.Info("add new watch, ", addPaths)
			err = a.ipcClient.Write(&ipc.Message{MsgType: jfsnotify.MSG_WATCH, Data: addPaths})
			if err != nil {
				klog.Error("send add watches error, ", err)
			}
		}

	}

	delWatch := func(watcher *sysv1alpha1.FSWatcher) {
		delPaths := make([]byte, 255)
		copy(delPaths, []byte(getChanId(watcher)))
		for _, p := range watcher.Spec.Paths {
			if _, ok := watchedPaths[p]; !ok {
				b := make([]byte, 255)
				copy(b, []byte(p))
				delPaths = append(delPaths, b...)
			}
		}

		if len(delPaths) > 0 {
			klog.Info("del watch, ", delPaths)
			err = a.ipcClient.Write(&ipc.Message{MsgType: jfsnotify.MSG_UNWATCH, Data: delPaths})
			if err != nil {
				klog.Error("send del watches error, ", err)
			}
		}
	}

	switch action {
	case fswatchers.ADD:
		addWatch(watcher)
	case fswatchers.DELETE:
		delWatch(watcher)
	case fswatchers.UPDATE:
		delWatch(watcher)
		addWatch(watcherSelf)
	}
	return nil
}
