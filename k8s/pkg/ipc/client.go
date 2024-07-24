package ipc

import (
	"context"
	"errors"
	"time"

	ipc "github.com/james-barrow/golang-ipc"
	"k8s.io/klog/v2"
)

type JFSClient struct {
	client *ipc.Client
}

func NewJFSClient(ctx context.Context, name string, readCB func(msg *ipc.Message), connCB func(client *JFSClient)) *JFSClient {
	client := &JFSClient{}

	reconnect := func() (*ipc.Client, error) {
		c, err := ipc.StartClient(name, &ipc.ClientConfig{Timeout: 2, Encryption: false})
		if err != nil {
			return nil, err
		}

		timeout := time.NewTimer(2 * time.Second)

	waitHandshake:
		for c.StatusCode() != ipc.Connected {
			select {
			case <-timeout.C:
				break waitHandshake
			default:
				m, err := c.Read()
				if err != nil {
					return nil, err
				}
				klog.Info("read ", m.Status, ", type ", m.MsgType)

				if m.MsgType == -2 {
					return nil, errors.New("connect error")
				}

				time.Sleep(100 * time.Millisecond)
			}
		}

		if c.StatusCode() == ipc.Connected {
			return c, nil
		}

		return nil, errors.New("wait for handshake timeout")
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				for client.client == nil {
					if c, err := reconnect(); err != nil {
						klog.Error("reconnect error, ", err)
						time.Sleep(time.Second)
					} else {
						client.client = c
						klog.Info("JuiceFS on nodes IPC connected")
						go connCB(client)
					}
				}
				m, err := client.client.Read()

				clear := func() {
					client.client.Close()
					client.client = nil
				}
				if err != nil {
					klog.Error("read ipc error, ", err)
					clear()
				} else {
					r := ipc.ReConnecting
					if m.MsgType == -2 || (m.MsgType == -1 && m.Status == r.String()) {
						klog.Error("pipe broken error")
						clear()
					} else if m.MsgType > 0 {
						readCB(m)
					}
				}

			}
		}
	}()

	return client
}

func (c *JFSClient) Write(m *ipc.Message) error {
	if c.client == nil {
		return errors.New("juicefs not connected")
	}

	return c.client.Write(m.MsgType, m.Data)
}
