package multicast

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/smallnest/goframe"
)

// maxTestFrame caps the length header before allocating, so a corrupted header
// fails the test instead of trying to allocate gigabytes.
const maxTestFrame = 1 << 16

// blockingFrameConn parks inside WriteFrame until release is closed, and tracks
// how many WriteFrame calls overlap.
type blockingFrameConn struct {
	mu      sync.Mutex
	entered chan struct{}
	release chan struct{}
	active  int
	maxSeen int
	frames  int
	block   bool
}

func newBlockingFrameConn(block bool) *blockingFrameConn {
	return &blockingFrameConn{
		entered: make(chan struct{}, 64),
		release: make(chan struct{}),
		block:   block,
	}
}

func (f *blockingFrameConn) WriteFrame(p []byte) error {
	f.mu.Lock()
	f.active++
	if f.active > f.maxSeen {
		f.maxSeen = f.active
	}
	f.frames++
	f.mu.Unlock()

	f.entered <- struct{}{}
	if f.block {
		<-f.release
	}

	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	return nil
}

func (f *blockingFrameConn) ReadFrame() ([]byte, error) { return nil, io.EOF }
func (f *blockingFrameConn) Close() error               { return nil }
func (f *blockingFrameConn) Conn() net.Conn             { return nil }

func (f *blockingFrameConn) stats() (peak, frames int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxSeen, f.frames
}

// testClient wires a client to one end of a pipe so shutdown has a real conn to
// close, while frame writes go to the supplied fake.
func testClient(t *testing.T, fconn goframe.FrameConn) (*Client, chan error) {
	t.Helper()

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		serverConn.Close()
		clientConn.Close()
	})

	closed := make(chan error, 8)
	srv := NewTCP("127.0.0.1:0")
	srv.OnClientConnectionClosed(func(c *Client, err error) { closed <- err })

	c := newClient(serverConn, srv)
	c.fconn = fconn
	return c, closed
}

func TestClose_DoesNotWaitForBlockedWrite(t *testing.T) {
	fake := newBlockingFrameConn(true)
	c, closed := testClient(t, fake)
	go c.writeLoop()

	if err := c.SendBytes([]byte("stuck")); err != nil {
		t.Fatalf("SendBytes: %v", err)
	}

	select {
	case <-fake.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("writeLoop never reached WriteFrame")
	}

	done := make(chan struct{})
	go func() {
		c.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked behind an in-flight write")
	}

	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not report the connection as closed")
	}

	// The peer must observe a closed socket even though the writer is still parked.
	if _, err := c.conn.Write([]byte("x")); err == nil {
		t.Fatal("Close should have closed the underlying conn")
	}

	close(fake.release)
}

func TestSendBytes_RejectsAfterClose(t *testing.T) {
	c, _ := testClient(t, newBlockingFrameConn(false))

	c.Close()

	if err := c.SendBytes([]byte("late")); !errors.Is(err, ErrClientClosed) {
		t.Fatalf("want ErrClientClosed after Close, got %v", err)
	}
}

func TestSendBytes_DropsSlowConsumerOnce(t *testing.T) {
	fake := newBlockingFrameConn(true)
	c, closed := testClient(t, fake)
	go c.writeLoop()

	// One frame occupies the writer, the rest fill the queue.
	var err error
	for i := 0; i < sendQueueDepth+8; i++ {
		if err = c.SendBytes([]byte(fmt.Sprintf("frame-%d", i))); err != nil {
			break
		}
	}
	if !errors.Is(err, ErrSlowConsumer) {
		t.Fatalf("want ErrSlowConsumer once the queue fills, got %v", err)
	}

	select {
	case got := <-closed:
		if !errors.Is(got, ErrSlowConsumer) {
			t.Fatalf("want teardown reported with ErrSlowConsumer, got %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("slow consumer was not torn down")
	}

	// Further sends must not report the teardown again.
	if err := c.SendBytes([]byte("after")); !errors.Is(err, ErrClientClosed) {
		t.Fatalf("want ErrClientClosed after teardown, got %v", err)
	}
	select {
	case extra := <-closed:
		t.Fatalf("teardown reported more than once: %v", extra)
	case <-time.After(100 * time.Millisecond):
	}

	close(fake.release)
}

func TestSendBytes_SingleWriterNoOverlap(t *testing.T) {
	fake := newBlockingFrameConn(false)
	c, _ := testClient(t, fake)
	go c.writeLoop()

	const senders = 32
	var wg sync.WaitGroup
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := c.SendBytes([]byte(fmt.Sprintf("payload-%d", i))); err != nil {
				t.Errorf("SendBytes: %v", err)
			}
		}(i)
	}
	wg.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, frames := fake.stats(); frames == senders {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	peak, frames := fake.stats()
	if frames != senders {
		t.Fatalf("want %d frames written, got %d", senders, frames)
	}
	if peak != 1 {
		t.Fatalf("WriteFrame must have a single owner, peak concurrency %d", peak)
	}
}

// TestSendBytes_FramesArriveIntactInOrder drives the real length-field codec.
func TestSendBytes_FramesArriveIntactInOrder(t *testing.T) {
	encoderConfig := goframe.EncoderConfig{
		ByteOrder:                       binary.BigEndian,
		LengthFieldLength:               4,
		LengthAdjustment:                0,
		LengthIncludesLengthFieldLength: false,
	}
	decoderConfig := goframe.DecoderConfig{
		ByteOrder:           binary.BigEndian,
		LengthFieldOffset:   0,
		LengthFieldLength:   4,
		LengthAdjustment:    0,
		InitialBytesToStrip: 4,
	}

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	srv := NewTCP("127.0.0.1:0")
	c := newClient(serverConn, srv)
	c.fconn = goframe.NewLengthFieldBasedFrameConn(encoderConfig, decoderConfig, serverConn)
	go c.writeLoop()

	const frames = 16
	want := make([]string, 0, frames)
	for i := 0; i < frames; i++ {
		// Vary the length so a desynced reader cannot accidentally realign.
		want = append(want, fmt.Sprintf("payload-%d%s", i, string(make([]byte, i))))
	}

	got := make(chan string, frames)
	readErr := make(chan error, 1)
	go func() {
		for i := 0; i < frames; i++ {
			payload, err := readBoundedFrame(clientConn)
			if err != nil {
				readErr <- err
				return
			}
			got <- payload
		}
		readErr <- nil
	}()

	for _, payload := range want {
		if err := c.SendBytes([]byte(payload)); err != nil {
			t.Fatalf("SendBytes: %v", err)
		}
	}

	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out reading frames")
	}

	close(got)
	var received []string
	for payload := range got {
		received = append(received, payload)
	}
	if len(received) != len(want) {
		t.Fatalf("want %d frames, got %d", len(want), len(received))
	}
	for i, payload := range want {
		if received[i] != payload {
			t.Fatalf("frame %d corrupted or reordered: want %q, got %q", i, payload, received[i])
		}
	}
}

// readBoundedFrame decodes one length-prefixed frame, refusing absurd lengths so
// a corrupted header surfaces as a test failure rather than a huge allocation.
func readBoundedFrame(r io.Reader) (string, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(r, header); err != nil {
		return "", err
	}

	length := binary.BigEndian.Uint32(header)
	if length > maxTestFrame {
		return "", fmt.Errorf("frame length %d exceeds cap %d, stream desynced", length, maxTestFrame)
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return "", err
	}
	return string(payload), nil
}
