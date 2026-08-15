//go:build integration

// Package-level integration test that requires a reachable redis.
//
// Run it with:
//
//	docker run --rm -d -p 6379:6379 --name fs-lib-redis redis:7-alpine
//	REDIS_ADDR=127.0.0.1:6379 go test ./k8s/cmd/fsnotify-proxy/app -tags integration -race -run TestRedisE2E
//
// REDIS_ADDR must be set in the environment before the process starts, since
// the producer package reads it in init.
package app

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/smallnest/goframe"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"bytetrade.io/web3os/fs-lib/jfsnotify"
	sysfake "bytetrade.io/web3os/fs-lib/k8s/pkg/generated/clientset/versioned/fake"
	"bytetrade.io/web3os/fs-lib/k8s/pkg/producer"
)

// TestRedisE2E_PublishReachesWatcher covers the one link the socket-only end to
// end test cannot: a redis publish driving the fan-out.
func TestRedisE2E_PublishReachesWatcher(t *testing.T) {
	if os.Getenv("REDIS_ADDR") == "" {
		t.Skip("REDIS_ADDR not set; start redis and re-run, see file header")
	}

	old := writeDebounce
	writeDebounce = testDebounce
	defer func() { writeDebounce = old }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopCh := make(chan struct{})
	defer close(stopCh)

	pub, err := producer.NewRedisClient(ctx, stopCh)
	if err != nil {
		t.Fatalf("redis not reachable at %s: %v", os.Getenv("REDIS_ADDR"), err)
	}

	addr := freeAddr(t)
	sys := sysfake.NewSimpleClientset()
	k8s := k8sfake.NewSimpleClientset(watchedPod())

	// New wires the real redis subscription on top of the same server.
	a := New(ctx, stopCh, sys, k8s, addr)
	go a.Start()

	var conn net.Conn
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, dialErr := net.Dial("tcp", addr)
		if dialErr == nil {
			conn = c
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("proxy did not start on %s", addr)
	}
	defer conn.Close()

	enc, dec := frameConfigs()
	f := &e2eFixture{sys: sys, fconn: goframe.NewLengthFieldBasedFrameConn(enc, dec, conn)}
	f.watch(t, podPath)

	payload, err := json.Marshal([]*jfsnotify.Event{{
		Name: nodePath + "/docs/via-redis.txt",
		Key:  nodePath,
		Op:   jfsnotify.Chmod,
	}})
	if err != nil {
		t.Fatal(err)
	}

	// The subscription races with our publish, so retry until it lands. The
	// publisher owns no *testing.T: it must not outlive the test body.
	deliver := time.NewTicker(200 * time.Millisecond)
	defer deliver.Stop()

	stopPub := make(chan struct{})
	pubErr := make(chan error, 1)
	var pubWG sync.WaitGroup
	pubWG.Add(1)
	defer func() {
		close(stopPub)
		pubWG.Wait()
		select {
		case err := <-pubErr:
			t.Errorf("publish: %v", err)
		default:
		}
	}()

	go func() {
		defer pubWG.Done()
		for {
			if err := pub.Pub(producer.ChannelName, string(payload)); err != nil {
				pubErr <- err
				return
			}
			select {
			case <-stopPub:
				return
			case <-deliver.C:
			}
		}
	}()

	got := f.readEvent(t, 15*time.Second)
	if want := podPath + "/docs/via-redis.txt"; got.Name != want {
		t.Fatalf("want translated name %q, got %q", want, got.Name)
	}
}
