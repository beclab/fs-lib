package multicast

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/smallnest/goframe"
)

// peer is a real socket client speaking the same framing as jfsnotify.
type peer struct {
	conn  net.Conn
	fconn goframe.FrameConn
}

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

// freeAddr reserves a loopback port and hands it back as a dial string.
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

// echoHelper is a MsgWriter that forwards each fan-out payload as one frame.
type echoHelper struct {
	client *Client
	closed chan struct{}
	once   sync.Once
}

func (h *echoHelper) WriteMsg(msg string) error { return h.client.SendBytes([]byte(msg)) }
func (h *echoHelper) Close()                    { h.once.Do(func() { close(h.closed) }) }

// startServer boots a real TCP server without the redis subscription.
func startServer(t *testing.T) (*Server, string) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	addr := freeAddr(t)
	s := NewWithoutSubscriber(ctx, addr)
	s.InitClient = func(c *Client) {
		c.Helper = &echoHelper{client: c, closed: make(chan struct{})}
	}

	go s.Start()

	// Wait for the listener to come up.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			conn.Close()
			return s, addr
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server did not start on %s", addr)
	return nil, ""
}

func dialPeer(t *testing.T, addr string) *peer {
	t.Helper()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	enc, dec := frameConfigs()
	return &peer{conn: conn, fconn: goframe.NewLengthFieldBasedFrameConn(enc, dec, conn)}
}

func (s *Server) clientCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.watchClients)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestIntegration_FanOutReachesEveryClient covers accept, publication, fan-out
// and framing over real sockets.
func TestIntegration_FanOutReachesEveryClient(t *testing.T) {
	s, addr := startServer(t)

	const peers = 3
	conns := make([]*peer, 0, peers)
	for i := 0; i < peers; i++ {
		conns = append(conns, dialPeer(t, addr))
	}
	waitFor(t, "clients to register", func() bool { return s.clientCount() == peers })

	// Every registered client must be fully initialized before it is reachable.
	s.mu.RLock()
	for cid, c := range s.watchClients {
		if c.Helper == nil || c.fconn == nil {
			s.mu.RUnlock()
			t.Fatalf("client %s published before initialization", cid)
		}
	}
	s.mu.RUnlock()

	const messages = 50
	for i := 0; i < messages; i++ {
		s.Deliver(fmt.Sprintf("event-%d", i))
	}

	for i, p := range conns {
		for j := 0; j < messages; j++ {
			p.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			frame, err := p.fconn.ReadFrame()
			if err != nil {
				t.Fatalf("peer %d frame %d: %v", i, j, err)
			}
			if want := fmt.Sprintf("event-%d", j); string(frame) != want {
				t.Fatalf("peer %d frame %d: want %q, got %q", i, j, want, string(frame))
			}
		}
	}
}

// TestIntegration_DisconnectRemovesClientOnce checks teardown bookkeeping when a
// peer drops.
func TestIntegration_DisconnectRemovesClientOnce(t *testing.T) {
	s, addr := startServer(t)

	p := dialPeer(t, addr)
	waitFor(t, "client to register", func() bool { return s.clientCount() == 1 })

	s.mu.RLock()
	var helper *echoHelper
	for _, c := range s.watchClients {
		helper = c.Helper.(*echoHelper)
	}
	s.mu.RUnlock()

	p.conn.Close()

	waitFor(t, "client to be removed", func() bool { return s.clientCount() == 0 })
	select {
	case <-helper.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher was not closed on disconnect")
	}
}

// TestIntegration_SlowConsumerDoesNotStallOthers is the core promise of the
// single-writer design: a peer that stops reading gets dropped instead of
// blocking the shared fan-out.
func TestIntegration_SlowConsumerDoesNotStallOthers(t *testing.T) {
	s, addr := startServer(t)

	slow := dialPeer(t, addr)
	healthy := dialPeer(t, addr)
	waitFor(t, "clients to register", func() bool { return s.clientCount() == 2 })

	// The healthy peer behaves like a real client: it reads continuously.
	sentinel := make(chan struct{})
	go func() {
		for {
			frame, err := healthy.fconn.ReadFrame()
			if err != nil {
				return
			}
			if string(frame) == "after-drop" {
				close(sentinel)
				return
			}
		}
	}()

	// Identify the slow peer's server-side client by its remote address.
	var slowCID string
	waitFor(t, "slow client lookup", func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		for cid, c := range s.watchClients {
			if c.conn.RemoteAddr().String() == slow.conn.LocalAddr().String() {
				slowCID = cid
				return true
			}
		}
		return false
	})

	// The slow peer never reads. Keep publishing until the server gives up on it;
	// the payload is large so kernel buffers fill quickly.
	payload := string(make([]byte, 32*1024))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < sendQueueDepth*8; i++ {
			s.Deliver(payload)
			s.mu.RLock()
			_, stillThere := s.watchClients[slowCID]
			s.mu.RUnlock()
			if !stillThere {
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("fan-out stalled on the slow consumer")
	}

	waitFor(t, "slow client to be dropped", func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		_, stillThere := s.watchClients[slowCID]
		return !stillThere
	})

	// The healthy peer must still be served after the slow one is gone.
	if got := s.clientCount(); got != 1 {
		t.Fatalf("only the slow client should have been dropped, %d remain", got)
	}
	s.Deliver("after-drop")

	select {
	case <-sentinel:
	case <-time.After(10 * time.Second):
		t.Fatal("healthy peer stopped receiving after the slow one was dropped")
	}
}
