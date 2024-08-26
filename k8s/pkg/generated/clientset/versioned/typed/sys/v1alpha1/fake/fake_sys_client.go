// Code generated by client-gen. DO NOT EDIT.

package fake

import (
	v1alpha1 "bytetrade.io/web3os/fs-lib/k8s/pkg/generated/clientset/versioned/typed/sys/v1alpha1"
	rest "k8s.io/client-go/rest"
	testing "k8s.io/client-go/testing"
)

type FakeSysV1alpha1 struct {
	*testing.Fake
}

func (c *FakeSysV1alpha1) FSWatchers(namespace string) v1alpha1.FSWatcherInterface {
	return &FakeFSWatchers{c, namespace}
}

// RESTClient returns a RESTClient that is used to communicate
// with API server by this client implementation.
func (c *FakeSysV1alpha1) RESTClient() rest.Interface {
	var ret *rest.RESTClient
	return ret
}
