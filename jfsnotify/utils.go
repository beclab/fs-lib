package jfsnotify

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/url"
	"strings"

	"github.com/smallnest/goframe"
	"k8s.io/klog/v2"
)

const (
	MSG_ERROR = iota

	// message data:
	// {
	// 	byte[255] // watcher name
	// 	byte[255][] // path array
	// }
	MSG_WATCH

	// message data:
	// {
	// 	byte[255] // watcher name
	// 	byte[255][] // path array
	// }
	MSG_UNWATCH

	// message data:
	// {
	// 	byte[255] // watcher name
	// }
	MSG_CLEAR

	// message data:
	// {
	// 	byte[255] // watcher name
	// }
	MSG_SUSPEND

	// message data:
	// {
	// 	byte[255] // watcher name
	// }
	MSG_RESUME

	// message data:
	// {
	// 	byte[255] // path
	//  int32 // op
	//  byte[255] // key
	// }[] // event array
	MSG_EVENT
)

var (
	FrameSize = 255 + 4 + 255
)

func PackageMsg(msgType int, data []byte) []byte {
	ret := make([]byte, 4)

	binary.BigEndian.PutUint32(ret, uint32(msgType))

	ret = append(ret, data...)

	return ret
}

func UnpackMsg(data []byte) (int, []byte, error) {
	if len(data) < 4 {
		return 0, nil, errors.New("invalid message to unpack")
	}

	msgType := bytesToInt(data[0:4])

	if len(data) == 4 {
		return msgType, nil, nil
	}

	msgData := data[4:]

	return msgType, msgData, nil
}

func PackEvent(event *Event) []byte {
	var data []byte

	b := make([]byte, 255)
	copy(b, []byte(event.Name))
	data = append(data, b...)

	b = make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(event.Op))
	data = append(data, b...)

	b = make([]byte, 255)
	copy(b, []byte(event.Key))
	data = append(data, b...)

	return data
}

func UnpackEvent(eventData []byte) ([]*Event, error) {
	if len(eventData) < FrameSize {
		return nil, errors.New("invalid event to unpack")
	}

	// multi-frame
	count := len(eventData) / FrameSize

	var events []*Event

	index := 0
	for index < count {
		offset := 0 + index*FrameSize
		if len(eventData[offset:]) < FrameSize {
			klog.Error("invalid event pack, ", string(eventData[offset:]))
			return events, nil
		}
		event := &Event{}

		event.Name = string(bytes.Trim(eventData[offset:offset+255], "\x00"))
		event.Op = Op(binary.BigEndian.Uint32(eventData[offset+255 : offset+259]))
		event.Key = string(bytes.Trim(eventData[offset+259:offset+259+255], "\x00"))

		events = append(events, event)
		index += 1
	}

	return events, nil
}

// parseDialTarget returns the network and address to pass to dialer
func parseDialTarget(target string) (net string, addr string) {
	net = "tcp"

	m1 := strings.Index(target, ":")
	m2 := strings.Index(target, ":/")

	// handle unix:addr which will fail with url.Parse
	if m1 >= 0 && m2 < 0 {
		if n := target[0:m1]; n == "unix" {
			net = n
			addr = target[m1+1:]
			return net, addr
		}
	}
	if m2 >= 0 {
		t, err := url.Parse(target)
		if err != nil {
			return net, target
		}
		scheme := t.Scheme
		addr = t.Path
		if scheme == "unix" || scheme == "ipc" {
			net = scheme
			if addr == "" {
				addr = t.Host
			}
			return net, addr
		}
	}

	return net, target
}

func isSocketError(e error) bool {
	switch e {
	case goframe.ErrTooLessLength, goframe.ErrUnexpectedFixedLength, goframe.ErrUnsupportedlength:
		return false
	default:
		return true
	}
}

func min(x, y int) int {
	if x > y {
		return y
	}

	return x
}

func bytesToInt(b []byte) int {

	var mlen uint32

	binary.Read(bytes.NewReader(b[:]), binary.BigEndian, &mlen) // message length

	return int(mlen)

}
