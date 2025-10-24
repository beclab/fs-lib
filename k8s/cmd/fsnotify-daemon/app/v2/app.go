package v2

import (
	"context"
	"encoding/json"
	"strings"

	"bytetrade.io/web3os/fs-lib/jfsnotify"
	sysv1alpha1 "bytetrade.io/web3os/fs-lib/k8s/pkg/apis/sys/v1alpha1"
	fswatchers "bytetrade.io/web3os/fs-lib/k8s/pkg/fswatchers/v1alpha1"
	listers "bytetrade.io/web3os/fs-lib/k8s/pkg/generated/listers/sys/v1alpha1"
	"bytetrade.io/web3os/fs-lib/k8s/pkg/producer"
	"github.com/fsnotify/fsnotify"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
)

type App struct {
	producer      producer.Interface
	watcherLister listers.FSWatcherLister
	watchers      *fsnotify.Watcher
}

func New(ctx context.Context, lister listers.FSWatcherLister, stopCh <-chan struct{}) (*App, error) {
	var app App
	app.watcherLister = lister

	producer, err := producer.NewRedisClient(ctx, stopCh)
	if err != nil {
		return nil, err
	}

	app.producer = producer

	go func() {
		for {
			app.watchers, err = fsnotify.NewWatcher()
			if err != nil {
				panic(err)
			}

			// start event loop
			exit, err := app.run(stopCh)
			if err != nil {
				klog.Error("fsnotify watcher exited with error, ", err)
			}

			if exit {
				klog.Info("fsnotify watcher exited normally")
				break
			}
		} // loop restart fsnotify watcher
	}()
	return &app, nil
}

func (a *App) run(stopCh <-chan struct{}) (bool, error) {
	defer a.watchers.Close()
	for {
		select {
		case e, ok := <-a.watchers.Events:
			if !ok {
				return false, nil
			}

			nameTokenized := strings.SplitN(e.Name, "/", -1)
			key := strings.Join(nameTokenized[:len(nameTokenized)-1], "/")
			event := jfsnotify.Event{
				Name: e.Name,
				Op:   jfsnotify.Op(e.Op),
				Key:  key,
			}
			klog.V(8).Info("fsnotify event: ", event)
			data, err := json.Marshal([]*jfsnotify.Event{&event})
			if err != nil {
				klog.Error("marshal event error, ", err)
				continue
			}
			err = a.producer.Pub(producer.ChannelName, string(data))
			if err != nil {
				klog.Error("publish event error, ", err)
			}
		case err, ok := <-a.watchers.Errors:
			if !ok {
				return false, nil
			}
			klog.Error("fsnotify error: ", err)
		case <-stopCh:
			klog.Info("fsnotify watcher stopping")
			return true, nil
		}
	}
}

func (a *App) SyncWatches(action fswatchers.Action, obj interface{}) error {
	var (
		ok bool
	)
	if _, ok = obj.(*sysv1alpha1.FSWatcher); !ok {
		klog.Error("unknown obj from controller")
		return nil
	}

	if a.watchers == nil {
		klog.Error("fsnotify watcher is nil")
		return nil
	}

	list, err := a.watcherLister.List(labels.Everything())
	if err != nil {
		return err
	}

	// add watchers, if path or file is watched, fsnotify will ignore duplicate add
	for _, l := range list {
		for _, p := range l.Spec.Paths {
			if err := a.watchers.Add(p); err != nil {
				klog.Error("add watch error, ", p, ", ", err)
			}
		}
	}

	return nil
}
