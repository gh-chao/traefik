package hrw

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/gh-chao/groupcache"
	"github.com/rs/zerolog/log"
	"github.com/traefik/traefik/v3/pkg/config/dynamic"
	"hash/fnv"
	"k8s.io/apimachinery/pkg/util/rand"
	"math"
	"net/http"
	"sync"
	"time"
)

const CacheSize = 1 << 34

var cache = groupcache.NewGroupWithWorkspace(groupcache.DefaultWorkspace, "hrw-sticky", CacheSize, groupcache.GetterFunc(func(ctx context.Context, key string, dest groupcache.Sink) error {
	return errors.New("not implemented")
}))

type namedHandler struct {
	http.Handler
	name   string
	weight float64
}

// Balancer is a HRW load balancer based on RendezVous hashing Algorithm (HRW).
// (https://en.m.wikipedia.org/wiki/Rendezvous_hashing)
// providing weighted stateless sticky session behavior with floating point weights and an O(n) pick time.
// Client connects to the same server each time based on their IP source
type Balancer struct {
	serviceName string

	wantsHealthCheck bool

	mutex    sync.RWMutex
	handlers []*namedHandler

	handlerMap map[string]*namedHandler

	// status is a record of which child services of the Balancer are healthy, keyed
	// by name of child service. A service is initially added to the map when it is
	// created via Add, and it is later removed or added to the map as needed,
	// through the SetStatus method.
	status map[string]struct{}
	// updaters is the list of hooks that are run (to update the Balancer
	// parent(s)), whenever the Balancer status changes.
	updaters []func(bool)

	sticky *dynamic.Sticky
}

// New creates a new load balancer.
func New(serviceName string, sticky *dynamic.Sticky, wantHealthCheck bool) *Balancer {
	log.Debug().Msgf("New HRW balancer: %s", serviceName)

	balancer := &Balancer{
		serviceName:      serviceName,
		sticky:           sticky,
		status:           make(map[string]struct{}),
		wantsHealthCheck: wantHealthCheck,
		handlerMap:       make(map[string]*namedHandler),
	}

	return balancer
}

// Push append a handler to the balancer list.
func (b *Balancer) Push(x interface{}) {
	h, ok := x.(*namedHandler)
	if !ok {
		return
	}
	b.handlers = append(b.handlers, h)
}

// getNodeScore calcul the score of the couple of src and handler name.
func getNodeScore(handler *namedHandler, src string) float64 {
	h := fnv.New32a()
	h.Write([]byte(src + (*handler).name))
	sum := h.Sum32()
	score := float32(sum) / float32(math.Pow(2, 32))
	logScore := 1.0 / -math.Log(float64(score))
	logWeightedScore := logScore * (*handler).weight
	return logWeightedScore
}

// SetStatus sets on the balancer that its given child is now of the given
// status. balancerName is only needed for logging purposes.
func (b *Balancer) SetStatus(ctx context.Context, childName string, up bool) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	upBefore := len(b.status) > 0

	status := "DOWN"
	if up {
		status = "UP"
	}

	log.Ctx(ctx).Debug().Msgf("Setting status of %s to %v", childName, status)

	if up {
		b.status[childName] = struct{}{}
	} else {
		delete(b.status, childName)
	}

	upAfter := len(b.status) > 0
	status = "DOWN"
	if upAfter {
		status = "UP"
	}

	// No Status Change
	if upBefore == upAfter {
		// We're still with the same status, no need to propagate
		log.Ctx(ctx).Debug().Msgf("Still %s, no need to propagate", status)
		return
	}

	// Status Change
	log.Ctx(ctx).Debug().Msgf("Propagating new %s status", status)
	for _, fn := range b.updaters {
		fn(upAfter)
	}
}

// RegisterStatusUpdater adds fn to the list of hooks that are run when the
// status of the Balancer changes.
// Not thread safe.
func (b *Balancer) RegisterStatusUpdater(fn func(up bool)) error {
	if !b.wantsHealthCheck {
		return errors.New("healthCheck not enabled in config for this weighted service")
	}
	b.updaters = append(b.updaters, fn)
	return nil
}

var errNoAvailableServer = errors.New("no available server")

func (b *Balancer) nextServer(hashKey string) (*namedHandler, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	if len(b.handlers) == 0 || len(b.status) == 0 {
		log.Debug().Msg("nextServer() len = 0")
		return nil, errNoAvailableServer
	}

	var handler *namedHandler
	score := 0.0
	for _, h := range b.handlers {
		// if handler healthy we calcul score
		// fmt.Printf("b.status = %s\n", b.status[h.name])
		if _, ok := b.status[h.name]; ok {
			s := getNodeScore(h, hashKey)
			if s > score {
				handler = h
				score = s
			}
		}
	}

	if handler != nil {
		log.Debug().Msgf("Service selected by HRW: %s", handler.name)
	}
	return handler, nil
}
func (b *Balancer) GetStickyKey(req *http.Request) string {
	if b.sticky == nil {
		// return random string
		return ""
	}

	if b.sticky.Header == nil {
		// return random string
		return ""
	}
	key := req.Header.Get(b.sticky.Header.Name)
	if key == "" {
		// return random string
		return ""
	}

	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(fmt.Sprintf("%s:%s", b.serviceName, key)))

	return hex.EncodeToString(hasher.Sum(nil))
}

func (b *Balancer) getStickyMaxAge() time.Duration {
	if b.sticky == nil {
		return time.Duration(0)
	}

	if b.sticky.Header == nil {
		return time.Duration(0)
	}

	if b.sticky.Header.MaxAge <= 0 {
		return time.Duration(0)
	}

	// 最大保持半小时
	if b.sticky.Header.MaxAge > 1800 {
		return 30 * time.Minute
	}

	return time.Duration(b.sticky.Header.MaxAge) * time.Second
}

func (b *Balancer) getHandlerWithCache(ctx context.Context, hashKey string) *namedHandler {
	if hashKey == "" {
		return nil
	}

	var serverName string
	err := cache.Get(ctx, hashKey, groupcache.StringSink(&serverName))
	if err != nil {
		return nil
	}

	b.mutex.RLock()
	handler, ok := b.handlerMap[serverName]
	b.mutex.RUnlock()

	// if handler is not found
	if !ok || handler == nil {
		return nil
	}

	b.mutex.RLock()
	_, isHealthy := b.status[handler.name]
	b.mutex.RUnlock()

	// if handler not healthy
	if !isHealthy {
		return nil
	}

	log.Debug().Msgf("ServeHTTP() cache hit hashKey=%s serverName=%s", hashKey, serverName)

	return handler
}

func (b *Balancer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	// give ip fetched to b.nextServer
	stickyKey := b.GetStickyKey(req)

	// check if cache hit
	handler := b.getHandlerWithCache(ctx, stickyKey)
	if handler != nil {
		handler.ServeHTTP(w, req)
		return
	}

	// if stickyKey is empty, we generate a random string
	var hashKey string
	if stickyKey == "" {
		hashKey = rand.String(10)
	} else {
		hashKey = stickyKey
	}

	log.Debug().Msgf("ServeHTTP() stickyKey=%s", stickyKey)
	server, err := b.nextServer(hashKey)
	if err != nil {
		if errors.Is(err, errNoAvailableServer) {
			http.Error(w, errNoAvailableServer.Error(), http.StatusServiceUnavailable)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	if stickyKey != "" {
		// cache the server name
		timeout, cancel := context.WithTimeout(ctx, time.Second/10)
		defer cancel()
		err = cache.Set(timeout, stickyKey, []byte(server.name), time.Now().Add(b.getStickyMaxAge()), true)
		if err != nil {
			log.Debug().Msgf("ServeHTTP() cache set error: %s", err.Error())
		} else {
			log.Debug().Msgf("ServeHTTP() cache set success: %s => %s", stickyKey, server.name)
		}
	}

	req.Header.Add("x-server", server.name)
	w.Header().Add("x-server", server.name)

	server.ServeHTTP(w, req)
}

// Add adds a handler.
// A handler with a non-positive weight is ignored.
func (b *Balancer) Add(name string, handler http.Handler, weight *int) {
	w := 1
	if weight != nil {
		w = *weight
	}

	if w <= 0 { // non-positive weight is meaningless
		return
	}

	h := &namedHandler{Handler: handler, name: name, weight: float64(w)}

	b.mutex.Lock()
	b.Push(h)
	b.handlerMap[name] = h
	b.status[name] = struct{}{}
	b.mutex.Unlock()
}
