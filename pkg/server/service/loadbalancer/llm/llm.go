package llm

import (
	"context"
	"errors"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/rs/zerolog/log"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"
)

type namedHandler struct {
	http.Handler
	directHandler http.Handler
	name          string
	weight        float64
}

type Balancer struct {
	wantsHealthCheck bool

	mutex    sync.RWMutex
	handlers []*namedHandler

	status   map[string]struct{}
	updaters []func(bool)
}

// New creates a new load balancer.
func New(serviceName string, wantHealthCheck bool) *Balancer {
	log.Debug().Msgf("New LLM Balancer: %s", serviceName)

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
	ctx := req.Context()
	server, err := b.nextServer(ctx)
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

	server.ServeHTTP(w, req)
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

	h := &namedHandler{Handler: handler, directHandler: directHandler, name: name, weight: float64(w)}

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

func (b *Balancer) nextServer(ctx context.Context) (*namedHandler, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	if len(b.handlers) == 0 || len(b.status) == 0 {
		log.Debug().Msg("nextServer() len = 0")
		return nil, errNoAvailableServer
	}

	// 获取所有节点状态, 超时时间 500ms
	timeout, cancel := context.WithTimeout(ctx, time.Second/2)
	defer cancel()

	statusCh := make(chan *NodeStatus, len(b.handlers))
	waitGroup := sync.WaitGroup{}
	for _, h := range b.handlers {
		waitGroup.Add(1)
		h2 := h
		go func(h2 *namedHandler) {
			defer waitGroup.Done()
			statusCh <- b.getNodeStatus(timeout, h2)
		}(h2)
	}
	waitGroup.Wait()
	close(statusCh)

	if len(statusCh) == 0 {
		return nil, errNoAvailableServer
	}

	// 计算方式
	// 挑选 RequestWaiting 最少的节点
	// 如果 RequestWaiting 相同，挑选 RequestRunning 最少的节点
	buckets := make(map[float64][]*namedHandler)
	var bestScore float64
	for status := range statusCh {
		score := status.RequestWaiting*100 + status.RequestRunning*10 + status.GPUCacheUsagePercent
		if bestScore == 0 || score < bestScore {
			bestScore = score
			if _, ok := buckets[score]; !ok {
				buckets[bestScore] = make([]*namedHandler, 0)
			}
			buckets[bestScore] = append(buckets[score], status.Handler)
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

type NodeStatus struct {
	// num_requests_waiting
	RequestWaiting float64
	// num_requests_running
	RequestRunning float64
	// gpu_cache_usage_percent
	GPUCacheUsagePercent float64

	Handler *namedHandler
}

func (b *Balancer) getNodeStatus(ctx context.Context, handler *namedHandler) *NodeStatus {
	nodeStatus := NodeStatus{
		RequestWaiting:       0,
		RequestRunning:       0,
		GPUCacheUsagePercent: 0,
		Handler:              handler,
	}

	req, err := http.NewRequestWithContext(ctx, "GET", "/metrics", nil)
	if err != nil {
		return &nodeStatus
	}

	recorder := httptest.NewRecorder()
	handler.directHandler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		log.Error().Msgf("Failed to get metrics from %s, %s", handler.name, recorder.Body.String())
		return &nodeStatus
	}

	var parser expfmt.TextParser
	mf, err := parser.TextToMetricFamilies(recorder.Body)
	if err != nil {
		log.Error().Err(err).Msg("Failed to parse metrics")
		return &nodeStatus
	}

	b.setNodeStatusWithVllm(mf, &nodeStatus)

	log.Debug().Msgf("NodeStatus: %s, RequestWaiting: %f, RequestRunning: %f, GPUCacheUsagePercent: %f", handler.name, nodeStatus.RequestWaiting, nodeStatus.RequestRunning, nodeStatus.GPUCacheUsagePercent)

	return &nodeStatus
}

func (b *Balancer) setNodeStatusWithVllm(mf map[string]*dto.MetricFamily, status *NodeStatus) {
	// 判断是否为 vllm
	if _, ok := mf["vllm:num_requests_running"]; !ok {
		return
	}

	// num_requests_waiting
	if metric, ok := mf["vllm:num_requests_waiting"]; ok {
		for _, m := range metric.Metric {
			status.RequestWaiting += m.GetGauge().GetValue()
		}
	}

	// num_requests_running
	if metric, ok := mf["vllm:num_requests_running"]; ok {
		for _, m := range metric.Metric {
			status.RequestRunning += m.GetGauge().GetValue()
		}
	}

	// gpu_cache_usage_percent
	if metric, ok := mf["vllm:gpu_cache_usage_percent"]; ok {
		for _, m := range metric.Metric {
			status.GPUCacheUsagePercent += m.GetGauge().GetValue()
		}
	}

}
