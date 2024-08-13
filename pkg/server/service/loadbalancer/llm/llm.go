package llm

import (
	"context"
	"errors"
	"fmt"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/rs/zerolog/log"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"
)

type namedHandler struct {
	handler http.Handler
	direct  http.Handler
	name    string
	weight  float64

	requestWaiting           float64
	requestRunning           float64
	kvCacheUsagePercent      float64
	windowPeriodRequestCount float64
	statusError              error

	connections atomic.Int64
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
	log.Debug().Msgf("New LLM Balancer: %s", serviceName)

	balancer := &Balancer{
		status:           make(map[string]struct{}),
		wantsHealthCheck: wantHealthCheck,
	}

	go balancer.watchNodeStatus(ctx)

	return balancer
}

func (b *Balancer) watchNodeStatus(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.updateNodeStatus(ctx)
		}
	}
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

	h := &namedHandler{handler: handler, direct: directHandler, name: name, weight: float64(w)}

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

const requestWaitingWeight = 10
const requestRunningWeight = 10
const kvCacheUsagePercentWeight = 100
const connectionsWeight = 20

func getScore(handler *namedHandler) float64 {
	var score float64 = 0

	// waiting 分数
	score += handler.requestWaiting * requestWaitingWeight

	// running 分数
	score += handler.requestRunning * requestRunningWeight

	// kvCache 分数
	score += handler.kvCacheUsagePercent * kvCacheUsagePercentWeight

	// 窗口期内的请求分数
	score += getWindowPeriodRequestScore(handler)

	// 连接数
	score += float64(handler.connections.Load()) * connectionsWeight

	return score
}

// 计算窗口期内的请求分数
func getWindowPeriodRequestScore(handler *namedHandler) float64 {
	// 已有排队
	if handler.requestWaiting > 0 {
		return handler.windowPeriodRequestCount * requestWaitingWeight
	}

	// 当前没有请求
	if handler.requestRunning == 0 || handler.kvCacheUsagePercent == 0 {
		return handler.windowPeriodRequestCount * requestRunningWeight
	}

	// 计算每个请求的平均 kvCache 使用量
	kvCacheUsagePerRequest := handler.kvCacheUsagePercent / handler.requestRunning
	// 预估当前kvCache余量还能调度多少请求
	remainingRequests := math.Floor((1 / kvCacheUsagePerRequest) - handler.requestRunning)

	// 如果 kvCache 余量足够，10分
	if handler.windowPeriodRequestCount <= remainingRequests {
		return handler.windowPeriodRequestCount * requestRunningWeight
	}

	// 如果kvCache余量不够，分开计算
	return remainingRequests*requestRunningWeight + (handler.windowPeriodRequestCount-remainingRequests)*requestWaitingWeight
}

func getBestNode(handlers []*namedHandler) (*namedHandler, error) {
	if len(handlers) == 0 {
		return nil, errNoAvailableServer
	}

	// 计算方式
	// 挑选 RequestWaiting 最少的节点
	// 如果 RequestWaiting 相同，挑选 RequestRunning 最少的节点
	buckets := make(map[float64][]*namedHandler)
	var bestScore = math.MaxFloat64
	for _, handler := range handlers {
		score := getScore(handler)
		if _, ok := buckets[score]; !ok {
			buckets[score] = make([]*namedHandler, 0)
		}
		buckets[score] = append(buckets[score], handler)
		if score < bestScore {
			bestScore = score
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

	bestNode.windowPeriodRequestCount++

	return bestNode, nil
}

func (b *Balancer) nextServer() (*namedHandler, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	if len(b.status) == 0 {
		return nil, errNoAvailableServer
	}

	return getBestNode(b.handlers)
}

func (b *Balancer) updateNodeStatus(ctx context.Context) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	timeout, cancel := context.WithTimeout(ctx, time.Second/2)
	defer cancel()

	waitGroup := sync.WaitGroup{}
	for _, h := range b.handlers {
		waitGroup.Add(1)
		h2 := h
		go func(h2 *namedHandler) {
			b.setNodeStatus(timeout, h2)
			defer waitGroup.Done()
		}(h2)
	}
	waitGroup.Wait()
}

func (b *Balancer) setNodeStatus(ctx context.Context, handler *namedHandler) {
	// reset status
	handler.kvCacheUsagePercent = 0
	handler.requestWaiting = 0
	handler.requestRunning = 0
	handler.windowPeriodRequestCount = 0
	handler.statusError = nil

	req, err := http.NewRequestWithContext(ctx, "GET", "/metrics", nil)
	if err != nil {
		handler.statusError = fmt.Errorf("failed to create request for %s, %v", handler.name, err)
		return
	}

	recorder := httptest.NewRecorder()
	handler.direct.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		handler.statusError = fmt.Errorf("failed to get metrics from %s, %s", handler.name, recorder.Body.String())
		return
	}

	var parser expfmt.TextParser
	mf, err := parser.TextToMetricFamilies(recorder.Body)
	if err != nil {
		handler.statusError = fmt.Errorf("failed to parse metrics from %s, %v", handler.name, err)
		return
	}

	b.setNodeStatusWithVllm(mf, handler)

	log.Debug().Msgf("NodeStatus: %s, RequestWaiting: %f, RequestRunning: %f, KVCacheUsagePercent: %f", handler.name, handler.requestWaiting, handler.requestRunning, handler.kvCacheUsagePercent)
}

func (b *Balancer) setNodeStatusWithVllm(mf map[string]*dto.MetricFamily, handler *namedHandler) {
	// 判断是否为 vllm
	if _, ok := mf["vllm:num_requests_running"]; !ok {
		return
	}

	// num_requests_waiting
	if metric, ok := mf["vllm:num_requests_waiting"]; ok {
		for _, m := range metric.Metric {
			handler.requestWaiting += m.GetGauge().GetValue()
		}
	}

	// num_requests_running
	if metric, ok := mf["vllm:num_requests_running"]; ok {
		for _, m := range metric.Metric {
			handler.requestRunning += m.GetGauge().GetValue()
		}
	}

	// gpu_cache_usage_percent
	if metric, ok := mf["vllm:gpu_cache_usage_perc"]; ok {
		for _, m := range metric.Metric {
			handler.kvCacheUsagePercent += m.GetGauge().GetValue()
		}
	}

}
