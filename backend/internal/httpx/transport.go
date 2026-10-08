package httpx

import (
	"net/http"
	"sync"

	"github.com/Nyrest/hindsight-ingestion/internal/proxy"
)

var transports = struct {
	sync.Mutex
	pool map[proxy.Config]*http.Transport
}{pool: map[proxy.Config]*http.Transport{}}

func Transport(config proxy.Config) *http.Transport {
	config = config.Normalize()
	if config.Type == "default" {
		return Shared
	}
	transports.Lock()
	defer transports.Unlock()
	if cached := transports.pool[config]; cached != nil {
		return cached
	}
	// Bound retired configurations without interrupting their in-flight requests.
	if len(transports.pool) >= 64 {
		for key, retired := range transports.pool {
			retired.CloseIdleConnections()
			delete(transports.pool, key)
		}
	}
	transport := Shared.Clone()
	transport.Proxy = config.ProxyFunc()
	transports.pool[config] = transport
	return transport
}
