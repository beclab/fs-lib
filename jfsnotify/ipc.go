package jfsnotify

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"time"
	"unsafe"

	ipc "github.com/james-barrow/golang-ipc"
	"k8s.io/klog/v2"
)

// for test only
type IpcConn struct {
	net.Conn
	client      *ipc.Client
	msgCache    []byte
	cacheOffset int
}

func NewIpcConn(name string) (*IpcConn, error) {
	c, err := ipc.StartClient(name, &ipc.ClientConfig{Timeout: 2, Encryption: false})
	if err != nil {
		return nil, err
	}

	for c.StatusCode() != ipc.Connected {
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

	return &IpcConn{client: c}, nil
}

func (i *IpcConn) Read(b []byte) (n int, err error) {
	if i.msgCache == nil {
		klog.Info("try to read from ipc")
		m, err := i.client.Read()
		if err != nil {
			return 0, err
		}

		r := ipc.ReConnecting
		if m.MsgType == -2 || (m.MsgType == -1 && m.Status == r.String()) {
			klog.Error("pipe broken error")
			return 0, io.ErrClosedPipe
		}

		l := make([]byte, 4)
		binary.BigEndian.PutUint32(l, uint32(4+len(m.Data)))

		t := make([]byte, 4)
		binary.BigEndian.PutUint32(t, uint32(m.MsgType))

		i.msgCache = l                             // frame length
		i.msgCache = append(i.msgCache, t...)      // msg type
		i.msgCache = append(i.msgCache, m.Data...) // msg data
		i.cacheOffset = 0
	}

	klog.Info("read data, " + strconv.Itoa(len(b)) + " cache size: " + strconv.Itoa(len(i.msgCache)))
	size := min(len(b), len(i.msgCache)-i.cacheOffset)
	copy(b, i.msgCache[i.cacheOffset:i.cacheOffset+size])

	i.cacheOffset += size
	if i.cacheOffset >= len(i.msgCache) {
		i.msgCache = nil
	}

	klog.Info("copy data, " + strconv.Itoa(size))
	return size, nil
}

func (i *IpcConn) Write(b []byte) (n int, err error) {
	if len(b) <= 4 {
		return
	}

	// length frame
	// first 4 bytes is frame length
	t := ([]byte)(unsafe.Slice(&b[4], 4))

	err = i.client.Write(int(binary.BigEndian.Uint32(t)), b[8:])
	klog.Info("write data, " + strconv.Itoa(len(b)))
	if err != nil {
		klog.Error("write error, ", err)
		return 0, err
	}

	return len(b), nil
}

func (i *IpcConn) Close() error {
	i.client.Close()
	return nil
}

func (i *IpcConn) LocalAddr() net.Addr { return nil }

func (i *IpcConn) RemoteAddr() net.Addr { return nil }

func (i *IpcConn) SetDeadline(t time.Time) error { return nil }

func (i *IpcConn) SetReadDeadline(t time.Time) error { return nil }

func (i *IpcConn) SetWriteDeadline(t time.Time) error { return nil }
