package producer

import "github.com/go-redis/redis"

type Interface interface {
	Pub(channel string, msg string) error
	Sub(channel string, subCB func(m *redis.Message))
}
