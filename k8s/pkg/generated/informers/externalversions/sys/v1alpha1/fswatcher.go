// Code generated by informer-gen. DO NOT EDIT.

package v1alpha1

import (
	"context"
	time "time"

	sysv1alpha1 "bytetrade.io/web3os/fs-lib/k8s/pkg/apis/sys/v1alpha1"
	versioned "bytetrade.io/web3os/fs-lib/k8s/pkg/generated/clientset/versioned"
	internalinterfaces "bytetrade.io/web3os/fs-lib/k8s/pkg/generated/informers/externalversions/internalinterfaces"
	v1alpha1 "bytetrade.io/web3os/fs-lib/k8s/pkg/generated/listers/sys/v1alpha1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	watch "k8s.io/apimachinery/pkg/watch"
	cache "k8s.io/client-go/tools/cache"
)

// FSWatcherInformer provides access to a shared informer and lister for
// FSWatchers.
type FSWatcherInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() v1alpha1.FSWatcherLister
}

type fSWatcherInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
	namespace        string
}

// NewFSWatcherInformer constructs a new informer for FSWatcher type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFSWatcherInformer(client versioned.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers) cache.SharedIndexInformer {
	return NewFilteredFSWatcherInformer(client, namespace, resyncPeriod, indexers, nil)
}

// NewFilteredFSWatcherInformer constructs a new informer for FSWatcher type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredFSWatcherInformer(client versioned.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options v1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.SysV1alpha1().FSWatchers(namespace).List(context.TODO(), options)
			},
			WatchFunc: func(options v1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.SysV1alpha1().FSWatchers(namespace).Watch(context.TODO(), options)
			},
		},
		&sysv1alpha1.FSWatcher{},
		resyncPeriod,
		indexers,
	)
}

func (f *fSWatcherInformer) defaultInformer(client versioned.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredFSWatcherInformer(client, f.namespace, resyncPeriod, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}, f.tweakListOptions)
}

func (f *fSWatcherInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&sysv1alpha1.FSWatcher{}, f.defaultInformer)
}

func (f *fSWatcherInformer) Lister() v1alpha1.FSWatcherLister {
	return v1alpha1.NewFSWatcherLister(f.Informer().GetIndexer())
}
