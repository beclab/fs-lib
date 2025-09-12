// Copyright 2023 bytetrade
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"flag"

	"bytetrade.io/web3os/fs-lib/jfsnotify"
	"k8s.io/klog/v2"
)

func main() {
	// 定义命令行参数
	var watchPath = flag.String("path", "", "Path to watch for file system events")
	flag.Parse()

	// 检查是否提供了路径参数
	if *watchPath == "" {
		klog.Fatal("Please provide a path to watch using -path flag")
	}

	watcher, err := jfsnotify.NewWatcher("mywatcher") // a single name of every watcher
	if err != nil {
		panic(err)
	}

	go func() {
		for {
			select {
			case e := <-watcher.Errors:
				klog.Error(e)
			case ev := <-watcher.Events:
				klog.Info(ev.Name, " - ", ev.Op)
			}
		}
	}()

	klog.Info("add ", *watchPath, " to watch")
	err = watcher.Add(*watchPath)
	if err != nil {
		klog.Error(err)
	}

	c := make(chan int)
	<-c
}
