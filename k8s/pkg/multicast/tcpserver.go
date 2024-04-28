// copy from https://github.com/firstrow/tcp_server/blob/master/tcp_server.go

package multicast

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"log"
	"net"

	"github.com/smallnest/goframe"
	"k8s.io/klog/v2"
)

// Client holds info about connection
type Client struct {
	cid    string
	conn   net.Conn
	Server *server
	fconn  goframe.FrameConn
	Helper any
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
	c.Server.onNewClientCallback(c)

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

	c.fconn = goframe.NewLengthFieldBasedFrameConn(encoderConfig, decoderConfig, c.conn)
	for {
		klog.Info("start to read client")
		message, err := c.fconn.ReadFrame()
		if err != nil {
			c.conn.Close()
			c.Server.onClientConnectionClosed(c, err)
			return
		}
		klog.Info("start to process new message")
		c.Server.onNewMessage(c, message)
	}
}

// Send text message to client
func (c *Client) Send(message string) error {
	return c.SendBytes([]byte(message))
}

// Send bytes to client
func (c *Client) SendBytes(b []byte) error {

	err := c.fconn.WriteFrame(b)
	if err != nil {
		c.fconn.Close()
		c.Server.onClientConnectionClosed(c, err)
	}
	return err
}

func (c *Client) Conn() net.Conn {
	return c.conn
}

func (c *Client) Close() error {
	return c.fconn.Close()
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
			conn, _ := listener.Accept()
			client := &Client{
				conn:   conn,
				Server: s,
			}
			go client.listen()
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
