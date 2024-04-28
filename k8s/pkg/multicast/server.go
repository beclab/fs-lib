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

func New(ctx context.Context, stopCh <-chan struct{}, addr string) *Server {
	tcpServer := NewTCP(addr)

	s := &Server{
		tcpServer:    tcpServer,
		watchClients: make(map[string]*Client),
		ctx:          ctx,
	}

	sub, err := NewSubscriber(ctx, stopCh, s.multicastMsg)
	if err != nil {
		panic(err)
	}

	s.subscriber = sub
	tcpServer.onNewMessage = s.messageReceived
	tcpServer.onClientConnectionClosed = s.removeChannel
	tcpServer.onNewClientCallback = func(c *Client) {
		s.mu.Lock()
		c.cid = string(uuid.NewUUID())
		s.watchClients[c.cid] = c
		s.mu.Unlock()

		if s.InitClient != nil {
			s.InitClient(c)
		}
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

func (s *Server) multicastMsg(msg string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, c := range s.watchClients {
		if writer, ok := c.Helper.(MsgWriter); ok {
			err := writer.WriteMsg(msg)
			if err != nil {
				klog.Error("send msg to client error, ", err)
			}
		}
	}
}
