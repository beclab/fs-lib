package main

import (
	"context"
	"flag"
	"os"

	"bytetrade.io/web3os/fs-lib/k8s/cmd/fsnotify-proxy/app"
	"bytetrade.io/web3os/fs-lib/k8s/pkg/signals"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/client-go/kubernetes"
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
	k8sClient := kubernetes.NewForConfigOrDie(config)

	addr := os.Getenv("MULTICAST_SERVER")
	if addr == "" {
		addr = ":5079"
	}

	app := app.New(apiCtx, stopCh, sysClient, k8sClient, addr)

	cmd := &cobra.Command{
		Use:   "fsnotify-proxy",
		Short: "fsnotify proxy server",
		Long:  `The fsnotify proxy server provides juicefs event multicast`,
		Run: func(cmd *cobra.Command, args []string) {
			app.Start()
		},
	}

	klog.Info("fsnotify-proxy starting ... ")

	if err := cmd.Execute(); err != nil {
		klog.Fatalln(err)
	}

}
