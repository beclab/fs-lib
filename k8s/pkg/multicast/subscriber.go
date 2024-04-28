package multicast

import (
	"context"

	"bytetrade.io/web3os/fs-lib/k8s/pkg/producer"
	"github.com/go-redis/redis"
	"k8s.io/klog/v2"
)

type Subscriber struct {
	server producer.Interface
}

func NewSubscriber(ctx context.Context, stopCh <-chan struct{}, subCB func(msg string)) (*Subscriber, error) {
	pro, err := producer.NewRedisClient(ctx, stopCh)
	if err != nil {
		return nil, err
	}

	pro.Sub(producer.ChannelName, func(m *redis.Message) {
		klog.Info("subcribed msg, ", m.Payload)
		if producer.ChannelName == m.Channel {
			subCB(m.Payload)
		}
	})

	return &Subscriber{
		server: pro,
	}, nil
}
