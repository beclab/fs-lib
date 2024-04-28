package fswatchers

import (
	"encoding/json"
	"fmt"
	"time"

	clientset "bytetrade.io/web3os/fs-lib/k8s/pkg/generated/clientset/versioned"
	"bytetrade.io/web3os/fs-lib/k8s/pkg/generated/clientset/versioned/scheme"
	informers "bytetrade.io/web3os/fs-lib/k8s/pkg/generated/informers/externalversions/sys/v1alpha1"
	listers "bytetrade.io/web3os/fs-lib/k8s/pkg/generated/listers/sys/v1alpha1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"

	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

const controllerAgentName = "fswatcher-controller"

type Controller struct {
	sysClientset  clientset.Interface
	watcherLister listers.FSWatcherLister
	watcherSynced cache.InformerSynced

	workqueue workqueue.RateLimitingInterface

	handler func(action Action, obj interface{}) error
}

type Action int

const (
	UNKNOWN Action = iota
	ADD
	UPDATE
	DELETE
)

type enqueueObj struct {
	action Action
	obj    interface{}
}

func NewController(sysClientset clientset.Interface,
	prInformer informers.FSWatcherInformer,
	handler func(action Action, obj interface{}) error) *Controller {
	utilruntime.Must(scheme.AddToScheme(scheme.Scheme))

	controller := &Controller{
		sysClientset:  sysClientset,
		watcherLister: prInformer.Lister(),
		watcherSynced: prInformer.Informer().HasSynced,
		workqueue:     workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "fswatchers"),
		handler:       handler,
	}

	klog.Info("Setting up fswatcher event handlers")

	// Set up an event handler for when providerregistry resources change
	prInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.handleAddObject,
		UpdateFunc: func(old, new interface{}) {
			if updated, err := diff(old, new); err != nil {
				klog.Error("diff error: ", err)
			} else if updated {
				controller.handleUpdateObject(old)
			}
		},
		DeleteFunc: controller.handleDeleteObject,
	})

	return controller
}

func (c *Controller) enqueue(obj enqueueObj) {
	// var key string
	// var err error
	// if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
	// 	utilruntime.HandleError(err)
	// 	return
	// }
	c.workqueue.Add(obj)
}

func (c *Controller) handleAddObject(obj interface{}) {
	// filter obj
	klog.Info("handle add object")
	c.enqueue(enqueueObj{ADD, obj})
}

func (c *Controller) handleUpdateObject(obj interface{}) {
	// filter obj
	klog.Info("handle update object ")

	c.enqueue(enqueueObj{UPDATE, obj})
}

func (c *Controller) handleDeleteObject(obj interface{}) {
	// filter obj
	klog.Info("handle delete object")

	c.enqueue(enqueueObj{DELETE, obj})
}

func (c *Controller) Run(workers int, stopCh <-chan struct{}) error {
	defer utilruntime.HandleCrash()
	defer c.workqueue.ShutDown()

	// Start the informer factories to begin populating the informer caches
	klog.Info("Starting ", controllerAgentName)

	// Wait for the caches to be synced before starting workers
	klog.Info("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, c.watcherSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	klog.Info("Starting workers")
	// Launch two workers to process Foo resources
	for i := 0; i < workers; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	klog.Info("Started workers")
	<-stopCh
	klog.Info("Shutting down workers")

	return nil
}

func (c *Controller) runWorker() {
	for c.processNextWorkItem() {
	}
}

// processNextWorkItem will read a single work item off the workqueue and
// attempt to process it, by calling the syncHandler.
func (c *Controller) processNextWorkItem() bool {
	obj, shutdown := c.workqueue.Get()

	if shutdown {
		return false
	}

	err := func(obj interface{}) error {
		defer c.workqueue.Done(obj)
		var eobj enqueueObj
		var ok bool
		if eobj, ok = obj.(enqueueObj); !ok {
			// As the item in the workqueue is actually invalid, we call
			// Forget here else we'd go into a loop of attempting to
			// process a work item that is invalid.
			c.workqueue.Forget(obj)
			utilruntime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}

		// Run the syncHandler, passing it the namespace/name string of the
		// Foo resource to be synced.
		if err := c.syncHandler(eobj); err != nil {
			// Put the item back on the workqueue to handle any transient errors.
			c.workqueue.AddRateLimited(eobj)
			return fmt.Errorf("error syncing '%v': %s, requeuing", eobj, err.Error())
		}

		// Finally, if no error occurs we Forget this item so it does not
		// get queued again until another change happens.
		c.workqueue.Forget(obj)
		klog.Infof("Successfully synced '%v'", eobj)
		return nil
	}(obj)

	if err != nil {
		utilruntime.HandleError(err)
		return true
	}

	return true
}

func (c *Controller) syncHandler(obj enqueueObj) error {

	return c.handler(obj.action, obj.obj)
}

func diff(old interface{}, new interface{}) (bool, error) {
	olddata, err := json.Marshal(old)
	if err != nil {
		return false, err
	}

	newdata, err := json.Marshal(new)
	if err != nil {
		return false, err
	}

	return string(olddata) != string(newdata), nil
}
