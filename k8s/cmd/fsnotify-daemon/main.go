package main

import (
	"context"
	"flag"

	"bytetrade.io/web3os/fs-lib/k8s/cmd/fsnotify-daemon/app"
	fswatchers "bytetrade.io/web3os/fs-lib/k8s/pkg/fswatchers/v1alpha1"
	informers "bytetrade.io/web3os/fs-lib/k8s/pkg/generated/informers/externalversions"
	"bytetrade.io/web3os/fs-lib/k8s/pkg/signals"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/klog/v2"

	sysclientset "bytetrade.io/web3os/fs-lib/k8s/pkg/generated/clientset/versioned"
	ctrl "sigs.k8s.io/controller-runtime"
)

func main() {
	if flag.CommandLine.Lookup("add_dir_header") == nil {
		klog.InitFlags(nil)
	}
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	config := ctrl.GetConfigOrDie()
	apiCtx, cancel := context.WithCancel(context.Background())
	stopCh := signals.SetupSignalHandler(apiCtx, cancel)

	sysClient := sysclientset.NewForConfigOrDie(config)

	informerFactory := informers.NewSharedInformerFactory(sysClient, 0)
	informer := informerFactory.Sys().V1alpha1().FSWatchers()

	app, err := app.New(apiCtx, informer.Lister(), stopCh)
	if err != nil {
		panic(err)
	}

	controller := fswatchers.NewController(sysClient, informer, app.SyncWatches)

	cmd := &cobra.Command{
		Use:   "fsnotify-daemon",
		Short: "fsnotify daemon server",
		Long:  `The fsnodify daemon server provides underlayer IPC daemonset to juicefs`,
		Run: func(cmd *cobra.Command, args []string) {
			defer func() {
				informerFactory.Shutdown()
			}()
			informerFactory.Start(stopCh)

			if err := controller.Run(1, stopCh); err != nil {
				panic(err)
			}
		},
	}

	klog.Info("fsnotify-daemon starting ... ")

	if err := cmd.Execute(); err != nil {
		klog.Fatalln(err)
	}

}
