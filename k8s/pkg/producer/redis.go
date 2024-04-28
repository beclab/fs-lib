package producer

import (
	"context"
	"os"

	"github.com/go-redis/redis"
	"k8s.io/klog/v2"
)

var (
	redisAddr = "redis.kubesphere-system:6379"
	redisDB   = 10
	redisPwd  = ""
)

func init() {
	getEnv := func(v *string, ns ...string) {
		for _, n := range ns {
			if e := os.Getenv(n); e != "" {
				*v = e
				return
			}
		}
	}

	getEnv(&redisAddr, "REDIS_ADDR")
	//getEnv(&redisDB, "REDIS_DB")
	getEnv(&redisPwd, "REDIS_PASSWORD")
}

type redisClient struct {
	client *redis.Client
	cancel context.CancelFunc
}

// redisOptions used to create a redis client.
// type redisOptions struct {
// 	Host     string `json:"host" yaml:"host" mapstructure:"host"`
// 	Port     int    `json:"port" yaml:"port" mapstructure:"port"`
// 	Password string `json:"password" yaml:"password" mapstructure:"password"`
// 	DB       int    `json:"db" yaml:"db" mapstructure:"db"`
// }

func NewRedisClient(ctx context.Context, stopCh <-chan struct{}) (Interface, error) {
	var r redisClient

	redisOptions := &redis.Options{
		Addr:     redisAddr,
		Password: redisPwd,
		DB:       redisDB,
	}

	if stopCh == nil {
		klog.Fatalf("no stop channel passed, redis connections will leak.")
	}

	redisCtx, cancel := context.WithCancel(ctx)
	r.client = redis.NewClient(redisOptions).WithContext(redisCtx)
	r.cancel = cancel

	if err := r.client.Ping().Err(); err != nil {
		cancel()
		r.client.Close()
		return nil, err
	}

	// close redis in case of connection leak
	if stopCh != nil {
		go func() {
			<-stopCh
			cancel()
			if err := r.client.Close(); err != nil {
				klog.Error(err)
			}
		}()
	}

	return &r, nil
}

func (r *redisClient) Pub(channel string, msg string) error {
	return r.client.Publish(channel, msg).Err()
}

func (r *redisClient) Sub(channel string, subCB func(m *redis.Message)) {
	go func() {
		ps := r.client.Subscribe(channel)
		defer ps.Close()

		ch := ps.Channel()

		for {
			select {
			case <-r.client.Context().Done():
				klog.Info("redis client closed")
				return
			case m := <-ch:
				if m == nil {
					klog.Info("redis subscribe stoped, ", channel)
					return
				}

				subCB(m)
			}
		}
	}()
}

func (r *redisClient) Close() {
	r.cancel()
}
