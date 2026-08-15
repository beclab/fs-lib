package app

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/smallnest/goframe"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"bytetrade.io/web3os/fs-lib/jfsnotify"
	sysfake "bytetrade.io/web3os/fs-lib/k8s/pkg/generated/clientset/versioned/fake"
	"bytetrade.io/web3os/fs-lib/k8s/pkg/multicast"
)

const (
	testNS        = "test-ns"
	testPod       = "test-pod"
	testContainer = "test-container"
	testChannel   = testNS + "/" + testPod + "/" + testContainer
	testVolume    = "data"
	nodePath      = "/mnt/node/userspace"
	podPath       = "/data"
)

func frameConfigs() (goframe.EncoderConfig, goframe.DecoderConfig) {
	return goframe.EncoderConfig{
			ByteOrder:                       binary.BigEndian,
			LengthFieldLength:               4,
			LengthAdjustment:                0,
			LengthIncludesLengthFieldLength: false,
		}, goframe.DecoderConfig{
			ByteOrder:           binary.BigEndian,
			LengthFieldOffset:   0,
			LengthFieldLength:   4,
			LengthAdjustment:    0,
			InitialBytesToStrip: 4,
		}
}

func freeAddr(t *testing.T) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// watchedPod mirrors what the proxy needs to map pod paths onto node paths.
func watchedPod() *corev1.Pod {
	hostPathType := corev1.HostPathDirectory
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: testPod, Namespace: testNS},
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{
				Name: testVolume,
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{Path: nodePath, Type: &hostPathType},
				},
			}},
			Containers: []corev1.Container{{
				Name:         testContainer,
				VolumeMounts: []corev1.VolumeMount{{Name: testVolume, MountPath: podPath}},
			}},
		},
	}
}

// watchMsg builds a MSG_WATCH payload the way jfsnotify's send does: one 255
// byte slot for the watcher name, then one slot per watched path.
func watchMsg(msgType int, channel string, paths []string) []byte {
	data := make([]byte, (len(paths)+1)*255)
	copy(data, []byte(channel))

	offset := 255
	for _, p := range paths {
		copy(data[offset:offset+255], []byte(p))
		offset += 255
	}

	return jfsnotify.PackageMsg(msgType, data)
}

type e2eFixture struct {
	server *multicast.Server
	sys    *sysfake.Clientset
	fconn  goframe.FrameConn
}

// startProxy runs a real proxy against fake k8s clients and a real TCP socket.
func startProxy(t *testing.T) *e2eFixture {
	t.Helper()

	old := writeDebounce
	writeDebounce = testDebounce
	t.Cleanup(func() { writeDebounce = old })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	addr := freeAddr(t)
	server := multicast.NewWithoutSubscriber(ctx, addr)
	sys := sysfake.NewSimpleClientset()
	k8s := k8sfake.NewSimpleClientset(watchedPod())

	a := newApp(ctx, server, sys, k8s)
	go a.Start()

	var conn net.Conn
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			conn = c
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("proxy did not start on %s", addr)
	}
	t.Cleanup(func() { conn.Close() })

	enc, dec := frameConfigs()
	return &e2eFixture{
		server: server,
		sys:    sys,
		fconn:  goframe.NewLengthFieldBasedFrameConn(enc, dec, conn),
	}
}

func (f *e2eFixture) watch(t *testing.T, paths ...string) {
	t.Helper()

	if err := f.fconn.WriteFrame(watchMsg(jfsnotify.MSG_WATCH, testChannel, paths)); err != nil {
		t.Fatal(err)
	}

	// The CR is created as the last step of processWatcherMessage, so its
	// presence means the path mapping is in place.
	crName := testPod + "-" + testContainer
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cr, err := f.sys.SysV1alpha1().FSWatchers(testNS).Get(context.Background(), crName, metav1.GetOptions{})
		if err == nil {
			if len(cr.Spec.Paths) == 0 {
				t.Fatalf("FSWatcher %s has no mapped paths", crName)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("FSWatcher %s was never created", crName)
}

func (f *e2eFixture) publish(t *testing.T, events ...*jfsnotify.Event) {
	t.Helper()

	payload, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	f.server.Deliver(string(payload))
}

func (f *e2eFixture) readEvent(t *testing.T, timeout time.Duration) jfsnotify.Event {
	t.Helper()

	type result struct {
		event jfsnotify.Event
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		frame, err := f.fconn.ReadFrame()
		if err != nil {
			ch <- result{err: err}
			return
		}
		msgType, body, err := jfsnotify.UnpackMsg(frame)
		if err != nil {
			ch <- result{err: err}
			return
		}
		if msgType != jfsnotify.MSG_EVENT {
			t.Errorf("unexpected msg type %d", msgType)
		}
		events, err := jfsnotify.UnpackEvent(body)
		if err != nil {
			ch <- result{err: err}
			return
		}
		ch <- result{event: *events[0]}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("read event: %v", r.err)
		}
		return r.event
	case <-time.After(timeout):
		t.Fatal("timed out waiting for an event")
		return jfsnotify.Event{}
	}
}

// TestE2E_WatchThenReceiveTranslatedEvent walks the whole proxy path: register a
// watch over a real socket, publish a node-path event, and expect the pod-path
// event back through the real codec.
func TestE2E_WatchThenReceiveTranslatedEvent(t *testing.T) {
	f := startProxy(t)
	f.watch(t, podPath)

	f.publish(t, &jfsnotify.Event{
		Name: nodePath + "/docs/report.txt",
		Key:  nodePath,
		Op:   jfsnotify.Chmod,
	})

	got := f.readEvent(t, 5*time.Second)
	if want := podPath + "/docs/report.txt"; got.Name != want {
		t.Fatalf("want translated name %q, got %q", want, got.Name)
	}
	if got.Op != jfsnotify.Chmod {
		t.Fatalf("want CHMOD, got %v", got.Op)
	}
}

// TestE2E_WriteBurstCoalescesIntoOneEvent proves the debounce survives the full
// round trip rather than only in unit tests.
func TestE2E_WriteBurstCoalescesIntoOneEvent(t *testing.T) {
	f := startProxy(t)
	f.watch(t, podPath)

	for i := 0; i < 5; i++ {
		f.publish(t, &jfsnotify.Event{
			Name: nodePath + "/docs/burst.txt",
			Key:  nodePath,
			Op:   jfsnotify.Write,
		})
		time.Sleep(2 * time.Millisecond)
	}

	got := f.readEvent(t, 5*time.Second)
	if want := podPath + "/docs/burst.txt"; got.Name != want {
		t.Fatalf("want translated name %q, got %q", want, got.Name)
	}

	// A second frame would mean the burst was not coalesced. Use a CHMOD sentinel
	// so the wait cannot pass just because the debounce is still pending.
	f.publish(t, &jfsnotify.Event{
		Name: nodePath + "/docs/sentinel.txt",
		Key:  nodePath,
		Op:   jfsnotify.Chmod,
	})

	next := f.readEvent(t, 5*time.Second)
	if want := podPath + "/docs/sentinel.txt"; next.Name != want {
		t.Fatalf("WRITE burst was not coalesced, got extra event %+v", next)
	}
}

// TestE2E_UnmappedPathIsNotDelivered guards the podPathMap filter.
func TestE2E_UnmappedPathIsNotDelivered(t *testing.T) {
	f := startProxy(t)
	f.watch(t, podPath)

	f.publish(t, &jfsnotify.Event{
		Name: "/mnt/node/other/secret.txt",
		Key:  "/mnt/node/other",
		Op:   jfsnotify.Chmod,
	})
	f.publish(t, &jfsnotify.Event{
		Name: nodePath + "/visible.txt",
		Key:  nodePath,
		Op:   jfsnotify.Chmod,
	})

	got := f.readEvent(t, 5*time.Second)
	if want := podPath + "/visible.txt"; got.Name != want {
		t.Fatalf("unmapped event leaked through, got %q", got.Name)
	}
}
