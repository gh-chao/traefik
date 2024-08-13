package least_conn

import (
	"context"
	"errors"
	"github.com/rs/zerolog/log"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
)

type namedHandler struct {
	handler http.Handler
	name    string
	weight  float64

	connections atomic.Int64
	statusError error
}

func (n *namedHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	n.connections.Add(1)
	defer n.connections.Add(-1)

	n.handler.ServeHTTP(writer, request)
}

type Balancer struct {
	wantsHealthCheck bool

	mutex    sync.RWMutex
	handlers []*namedHandler

	status   map[string]struct{}
	updaters []func(bool)
}

// New creates a new load balancer.
func New(ctx context.Context, serviceName string, wantHealthCheck bool) *Balancer {
	log.Debug().Msgf("New LeastConn Balancer: %s", serviceName)

	balancer := &Balancer{
		status:           make(map[string]struct{}),
		wantsHealthCheck: wantHealthCheck,
	}

	return balancer
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

func (b *Balancer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	server, err := b.nextServer()
	if err != nil {
		if errors.Is(err, errNoAvailableServer) {
			http.Error(w, errNoAvailableServer.Error(), http.StatusServiceUnavailable)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	req.Header.Add("x-server", server.name)
	w.Header().Add("x-server", server.name)

	server.handler.ServeHTTP(w, req)
}

// Add adds a handler.
// A handler with a non-positive weight is ignored.
func (b *Balancer) Add(name string, handler http.Handler, directHandler http.Handler, weight *int) {
	w := 1
	if weight != nil {
		w = *weight
	}

	if w <= 0 { // non-positive weight is meaningless
		return
	}

	h := &namedHandler{handler: handler, name: name, weight: float64(w)}

	b.mutex.Lock()
	b.Push(h)
	b.status[name] = struct{}{}
	b.mutex.Unlock()
}

// Push append a handler to the balancer list.
func (b *Balancer) Push(x interface{}) {
	h, ok := x.(*namedHandler)
	if !ok {
		return
	}
	b.handlers = append(b.handlers, h)
}

var errNoAvailableServer = errors.New("no available server")

func (b *Balancer) nextServer() (*namedHandler, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	if len(b.handlers) == 0 || len(b.status) == 0 {
		log.Debug().Msg("nextServer() len = 0")
		return nil, errNoAvailableServer
	}
	buckets := make(map[int64][]*namedHandler)
	var bestScore int64
	for _, handler := range b.handlers {
		score := handler.connections.Load()
		if bestScore == 0 || score < bestScore {
			bestScore = score
			if _, ok := buckets[score]; !ok {
				buckets[bestScore] = make([]*namedHandler, 0)
			}
			buckets[bestScore] = append(buckets[score], handler)
		}
	}
	// 随机选择一个节点
	bestNodes, ok := buckets[bestScore]
	if !ok || len(bestNodes) == 0 {
		return nil, errNoAvailableServer
	}
	randIndex := rand.Intn(len(bestNodes))
	bestNode := bestNodes[randIndex]
	if bestNode == nil {
		return nil, errNoAvailableServer
	}

	log.Debug().Msgf("Service selected by : %s", bestNode.name)

	return bestNode, nil
}
