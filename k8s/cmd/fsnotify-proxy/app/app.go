package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"bytetrade.io/web3os/fs-lib/jfsnotify"
	"bytetrade.io/web3os/fs-lib/k8s/pkg/apis/sys/v1alpha1"
	"bytetrade.io/web3os/fs-lib/k8s/pkg/multicast"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	sysclientset "bytetrade.io/web3os/fs-lib/k8s/pkg/generated/clientset/versioned"
)

const MaxName = 255

type App struct {
	proxyServer  *multicast.Server
	clientSet    *sysclientset.Clientset
	k8sClientSet *kubernetes.Clientset
	ctx          context.Context
}

func New(ctx context.Context, stopCh <-chan struct{}, clientSet *sysclientset.Clientset, k8sClient *kubernetes.Clientset, addr string) *App {
	app := &App{
		proxyServer:  multicast.New(ctx, stopCh, addr),
		clientSet:    clientSet,
		k8sClientSet: k8sClient,
		ctx:          ctx,
	}

	app.proxyServer.ChannelMessageProc = app.processWatcherMessage

	app.proxyServer.InitClient = func(c *multicast.Client) {
		w := NewWatcher(c)
		w.Remove = func() {
			err := clientSet.SysV1alpha1().FSWatchers(w.CRNamespace).Delete(ctx, w.CRName, metav1.DeleteOptions{})
			if err != nil {
				klog.Error("delete cr error, ", w.CRNamespace, " / ", w.CRName, ", ", err)
			}
		}
		c.Helper = w
	}

	return app
}

func (a *App) processWatcherMessage(c *multicast.Client, msg []byte) error {
	watcher, ok := c.Helper.(*watcher)
	if !ok {
		return errors.New("unsupported client helper")
	}

	// unpack the jfsnotify message from pod
	jfsMsgType, jfsMsg, err := jfsnotify.UnpackMsg(msg)
	if err != nil {
		klog.Error("unable to unpack jfsnotify message")
		return err
	}

	parseMsg := func() (string, []string) {
		var paths []string
		offset := 0
		n := len(jfsMsg)
		for offset < n {
			strlen := min(n-offset, MaxName)
			paths = append(paths, string(bytes.Trim(jfsMsg[offset:offset+strlen], "\x00")))
			offset += MaxName
		}

		if len(paths) == 0 {
			return "", nil
		}

		if len(paths) == 1 {
			return paths[0], nil
		}

		return paths[0], paths[1:]
	}

	var (
		channelId string
		keys      []string
	)

	formatingChanId := func(cid string) string {
		return strings.ToLower(cid)
	}
	switch jfsMsgType {
	case jfsnotify.MSG_CLEAR:
		channelId := formatingChanId(string(bytes.Trim(jfsMsg, "\x00")))
		_, w, err := a.getWatcher(channelId)
		if err != nil && apierrors.IsNotFound(err) {
			return nil
		}

		if err != nil {
			return err
		}

		klog.Info("watcher cleared")

		return a.clientSet.SysV1alpha1().FSWatchers(w.Namespace).Delete(a.ctx, w.Name, metav1.DeleteOptions{})
	case jfsnotify.MSG_UNWATCH:
		channelId, keys = parseMsg()
		channelId = formatingChanId(channelId)
	case jfsnotify.MSG_WATCH:
		channelId, keys = parseMsg()
		channelId = formatingChanId(channelId)
	default:
		return fmt.Errorf("unknown msg, %v", jfsMsgType)
	}

	namespace, podname, containername, err := getPodName(channelId)
	if err != nil {
		klog.Error("invalid channel id, ", channelId)
		return err
	}

	// find pod's volume to map the pod path to node path
	pod, err := a.k8sClientSet.CoreV1().Pods(namespace).Get(a.ctx, podname, metav1.GetOptions{})
	if err != nil {
		klog.Error("unable to get pod in cluster, ", namespace, " / ", pod)
		return err
	}

	klog.Info("watcher ", channelId, " send path ", keys)

	volumeMap := make(map[string]string)
	for _, v := range pod.Spec.Volumes {
		if v.HostPath != nil &&
			(*v.HostPath.Type == corev1.HostPathDirectory || *v.HostPath.Type == corev1.HostPathDirectoryOrCreate) {
			volumeMap[v.Name] = v.HostPath.Path
		}
	}

	// translate keys
	for _, c := range pod.Spec.Containers {
		if c.Name != containername {
			continue
		}

		for _, vm := range c.VolumeMounts {
			podPath := vm.MountPath
			nodePath, ok := volumeMap[vm.Name]
			if ok {
				watcher.mu.Lock()
				for _, k := range keys {
					if strings.HasPrefix(k, podPath) {
						watchPath := nodePath
						podWatchPath := podPath
						if len(k) > len(podPath) {
							sub := k[len(podPath):]
							if watchPath[len(watchPath)-1] != '/' && sub[0] != '/' {
								watchPath += "/"
							}

							if watchPath[len(watchPath)-1] == '/' && sub[0] == '/' {
								sub = sub[1:]
							}

							watchPath += sub
							podWatchPath += sub
						}
						switch jfsMsgType {
						case jfsnotify.MSG_WATCH:
							watcher.podPathMap[watchPath] = podWatchPath

						case jfsnotify.MSG_UNWATCH:
							delete(watcher.podPathMap, watchPath)
						}
					}

				}
				watcher.mu.Unlock()
			} // end if find volume

		} // end volumeMount loop
	} // end container loop

	var currentPath []string
	for k := range watcher.podPathMap {
		currentPath = append(currentPath, k)
	}

	if len(currentPath) == 0 {
		return errors.New("watch paths invalid, cannot mapping to nodes path")
	}

	watcherName, w, err := a.getWatcher(channelId)
	if err != nil && apierrors.IsNotFound(err) {
		w = &v1alpha1.FSWatcher{
			ObjectMeta: metav1.ObjectMeta{
				Name: watcherName,
			},
			Spec: v1alpha1.FSWatcherSpec{
				Pod:       podname,
				Paths:     currentPath,
				Container: containername,
			},
		}

		_, err = a.clientSet.SysV1alpha1().FSWatchers(namespace).Create(a.ctx, w, metav1.CreateOptions{})
		return err
	}

	if err != nil {
		return err
	}

	w.Spec.Paths = currentPath
	_, err = a.clientSet.SysV1alpha1().FSWatchers(namespace).Update(a.ctx, w, metav1.UpdateOptions{})

	watcher.CRName = watcherName
	watcher.CRNamespace = namespace
	return err
}

func (a *App) Start() {
	a.proxyServer.Start()
}

func getPodName(channel string) (namespace, podname, containername string, err error) {
	ctoken := strings.Split(channel, "/")
	if len(ctoken) < 3 {
		return "", "", "", fmt.Errorf("invalid channel id, %s", channel)
	}

	return ctoken[0], ctoken[1], ctoken[2], nil
}

func (a *App) getWatcher(channel string) (string, *v1alpha1.FSWatcher, error) {
	ctoken := strings.Split(channel, "/")
	if len(ctoken) < 2 {
		return "", nil, fmt.Errorf("invalid channel id, %s", channel)
	}

	namespace := ctoken[0]
	watcherName := strings.Join(ctoken[1:], "-")

	w, err := a.clientSet.SysV1alpha1().FSWatchers(namespace).Get(a.ctx, watcherName, metav1.GetOptions{})

	return watcherName, w, err
}

func min(x, y int) int {
	if x > y {
		return y
	}

	return x
}
