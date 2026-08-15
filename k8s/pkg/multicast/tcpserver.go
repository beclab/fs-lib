// copy from https://github.com/firstrow/tcp_server/blob/master/tcp_server.go

package multicast

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"log"
	"net"
	"sync"

	"github.com/smallnest/goframe"
	"k8s.io/klog/v2"
)

// sendQueueDepth mirrors the client-side sendQ in jfsnotify.
const sendQueueDepth = 255

var (
	ErrClientClosed = errors.New("client connection closed")
	ErrSlowConsumer = errors.New("client send queue full")
)

// Client holds info about connection.
//
// Frames are written by writeLoop only. WriteFrame emits the length header and
// the payload as separate writes on a shared bufio.Writer, so a single owner is
// what keeps two senders from interleaving halves of two frames.
type Client struct {
	cid       string
	conn      net.Conn
	Server    *server
	fconn     goframe.FrameConn
	sendQ     chan []byte
	done      chan struct{}
	closeOnce sync.Once
	Helper    any
}

func newClient(conn net.Conn, s *server) *Client {
	return &Client{
		conn:   conn,
		Server: s,
		sendQ:  make(chan []byte, sendQueueDepth),
		done:   make(chan struct{}),
	}
}

// TCP server
type server struct {
	address                  string // Address to open connection: localhost:9999
	config                   *tls.Config
	onNewClientCallback      func(c *Client)
	onClientConnectionClosed func(c *Client, err error)
	onNewMessage             func(c *Client, message []byte)
}

// Read client data from channel
func (c *Client) listen() {
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

	// fconn must be ready before the callback publishes this client to the
	// fan-out map, otherwise senders race with this assignment.
	c.fconn = goframe.NewLengthFieldBasedFrameConn(encoderConfig, decoderConfig, c.conn)

	go c.writeLoop()

	c.Server.onNewClientCallback(c)

	for {
		klog.Info("start to read client")
		message, err := c.fconn.ReadFrame()
		if err != nil {
			c.shutdown(err)
			return
		}
		klog.Info("start to process new message")
		c.Server.onNewMessage(c, message)
	}
}

// writeLoop is the only writer of this connection.
func (c *Client) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case b := <-c.sendQ:
			if err := c.fconn.WriteFrame(b); err != nil {
				c.shutdown(err)
				return
			}
		}
	}
}

// shutdown tears the connection down exactly once. Closing conn also unblocks a
// WriteFrame parked in the kernel, so callers never wait for the writer.
func (c *Client) shutdown(err error) {
	c.closeOnce.Do(func() {
		close(c.done)
		c.conn.Close()
		c.Server.onClientConnectionClosed(c, err)
	})
}

// Send bytes to client. The frame is queued for writeLoop rather than written
// inline, so a stalled peer cannot block the shared fan-out.
func (c *Client) SendBytes(b []byte) error {
	select {
	case <-c.done:
		return ErrClientClosed
	default:
	}

	select {
	case c.sendQ <- b:
		return nil
	case <-c.done:
		return ErrClientClosed
	default:
		// A peer this far behind will not catch up; drop it and let it reconnect
		// and re-send its watches instead of stalling every other client.
		klog.Error("send queue full, dropping client, cid=", c.cid)
		c.shutdown(ErrSlowConsumer)
		return ErrSlowConsumer
	}
}

// CID returns the server-assigned connection id for this client.
func (c *Client) CID() string {
	if c == nil {
		return ""
	}
	return c.cid
}

func (c *Client) Close() error {
	c.shutdown(nil)
	return nil
}

// Called right after server starts listening new client
func (s *server) OnNewClient(callback func(c *Client)) {
	s.onNewClientCallback = callback
}

// Called right after connection closed
func (s *server) OnClientConnectionClosed(callback func(c *Client, err error)) {
	s.onClientConnectionClosed = callback
}

// Called when Client receives new message
func (s *server) OnNewMessage(callback func(c *Client, message []byte)) {
	s.onNewMessage = callback
}

// Listen starts network server
func (s *server) Listen(ctx context.Context) {
	var listener net.Listener
	var err error
	if s.config == nil {
		listener, err = net.Listen("tcp", s.address)
	} else {
		listener, err = tls.Listen("tcp", s.address, s.config)
	}
	if err != nil {
		log.Fatal("Error starting TCP server.\r\n", err)
	}
	defer listener.Close()

	for {
		select {
		case <-ctx.Done():
			return
		default:
			conn, err := listener.Accept()
			if err != nil {
				// A nil conn would panic both the read loop and writeLoop.
				klog.Error("accept connection error, ", err)
				continue
			}

			go newClient(conn, s).listen()
		}
	}
}

// Creates new tcp server instance
func NewTCP(address string) *server {
	log.Println("Creating server with address", address)
	server := &server{
		address: address,
	}

	server.OnNewClient(func(c *Client) {})
	server.OnNewMessage(func(c *Client, message []byte) {})
	server.OnClientConnectionClosed(func(c *Client, err error) {})

	return server
}

func NewTCPWithTLS(address, certFile, keyFile string) *server {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		log.Fatal("Error loading certificate files. Unable to create TCP server with TLS functionality.\r\n", err)
	}
	config := &tls.Config{
		Certificates: []tls.Certificate{cert},
	}
	server := NewTCP(address)
	server.config = config
	return server
}
