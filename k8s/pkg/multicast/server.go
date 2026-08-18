package multicast

import (
	"context"
	"sync"

	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/klog/v2"
)

type MsgWriter interface {
	WriteMsg(msg string) error
	Close()
}

type Server struct {
	tcpServer          *server
	watchClients       map[string]*Client
	subscriber         *Subscriber
	ChannelMessageProc func(c *Client, msg []byte) error
	InitClient         func(c *Client)
	mu                 sync.RWMutex
	ctx                context.Context
}

// New builds a Server whose fan-out is fed by the redis subscription.
func New(ctx context.Context, stopCh <-chan struct{}, addr string) *Server {
	s := NewWithoutSubscriber(ctx, addr)

	sub, err := NewSubscriber(ctx, stopCh, s.Deliver)
	if err != nil {
		panic(err)
	}
	s.subscriber = sub

	return s
}

// NewWithoutSubscriber builds a Server with no event source attached. The caller
// drives fan-out through Deliver, which is all the redis subscription does.
func NewWithoutSubscriber(ctx context.Context, addr string) *Server {
	tcpServer := NewTCP(addr)

	s := &Server{
		tcpServer:    tcpServer,
		watchClients: make(map[string]*Client),
		ctx:          ctx,
	}

	tcpServer.onNewMessage = s.messageReceived
	tcpServer.onClientConnectionClosed = s.removeChannel
	tcpServer.onNewClientCallback = func(c *Client) {
		c.cid = string(uuid.NewUUID())

		if s.InitClient != nil {
			s.InitClient(c)
		}

		// Published last: a fan-out picking this client up before Helper is set
		// would silently skip it.
		s.mu.Lock()
		s.watchClients[c.cid] = c
		s.mu.Unlock()
	}

	return s
}

func (s *Server) Start() {
	s.tcpServer.Listen(s.ctx)
}

func (s *Server) messageReceived(c *Client, message []byte) {
	if s.ChannelMessageProc == nil {
		klog.Info("message processor is nil")
		return
	}

	err := s.ChannelMessageProc(c, message)
	if err != nil {
		klog.Error("process message error, ", err)
		return
	}
}

func (s *Server) removeChannel(c *Client, err error) {
	klog.Error("channel closed, ", err)

	if writer, ok := c.Helper.(MsgWriter); ok {
		writer.Close()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.watchClients, c.cid)
}

// Deliver fans one payload out to every connected client.
func (s *Server) Deliver(msg string) {
	var funcs []func()
	s.mu.RLock()
	n := len(s.watchClients)

	for _, c := range s.watchClients {
		c := c // per-iteration copy for closures (Go <1.22 loop var safety)
		writer, ok := c.Helper.(MsgWriter)
		if !ok {
			continue
		}
		funcs = append(funcs, func() {
			if err := writer.WriteMsg(msg); err != nil {
				klog.Error("send msg to client error, ", err, ", cid=", c.CID())
			}
		})
	}

	s.mu.RUnlock()

	if n == 0 || len(funcs) == 0 {
		klog.Warning("redis event not fanned out: no watch clients, connected=", n,
			" writers=", len(funcs), " payload_len=", len(msg))
		return
	}

	klog.V(4).Infof("fanout redis event connected=%d writers=%d payload_len=%d", n, len(funcs), len(msg))

	for _, f := range funcs {
		f()
	}
}
