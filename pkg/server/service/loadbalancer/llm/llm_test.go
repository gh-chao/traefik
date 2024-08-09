package llm

import (
	"context"
	"fmt"
	"github.com/prometheus/common/expfmt"
	"net/http"
	"testing"
)

func TestPrometheusMetricsParse(t *testing.T) {

	t.Run("TestPrometheusMetricsParse", func(t *testing.T) {
		req, err := http.NewRequestWithContext(context.TODO(), "GET", "http://api.mlops.tal.com/common-llama-3-70b/metrics", nil)
		if err != nil {
			t.Error(err)
		}
		req.Header.Add("Authorization", "Bearer ca6dc09179d08a7250bc299da47c003e")

		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Error("Failed to get metrics:", response.Body)
		}

		var parser expfmt.TextParser
		mf, err := parser.TextToMetricFamilies(response.Body)
		if err != nil {
			t.Error(err)
		}

		fmt.Println(len(mf))

	})
}

func Test_getScore(t *testing.T) {
	type args struct {
		handler *namedHandler
	}
	tests := []struct {
		name string
		args args
		want float64
	}{
		{
			name: "Test_getScore_1",
			args: args{
				handler: &namedHandler{
					requestWaiting:           1,
					requestRunning:           10,
					kvCacheUsagePercent:      0.9,
					windowPeriodRequestCount: 5,
				},
			},
		},
		{
			name: "Test_getScore_2",
			args: args{
				handler: &namedHandler{
					requestWaiting:           0,
					requestRunning:           10,
					kvCacheUsagePercent:      0.5,
					windowPeriodRequestCount: 10,
				},
			},
		},
		{
			name: "Test_getScore_3",
			args: args{
				handler: &namedHandler{
					requestWaiting:           0,
					requestRunning:           10,
					kvCacheUsagePercent:      0.5,
					windowPeriodRequestCount: 12,
				},
			},
		},
		{
			name: "Test_getScore_4",
			args: args{
				handler: &namedHandler{
					requestWaiting:           454.000000,
					requestRunning:           253.000000,
					kvCacheUsagePercent:      0.631876,
					windowPeriodRequestCount: 12,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getScore(tt.args.handler); got != tt.want {
				t.Errorf("getScore() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getBestNode(t *testing.T) {
	type args struct {
		handlers []*namedHandler
	}
	tests := []struct {
		name    string
		args    args
		want    *namedHandler
		wantErr bool
	}{
		{
			name: "Test_getBestNode_1",
			args: args{
				handlers: []*namedHandler{
					{
						name:                     "1",
						weight:                   0,
						requestWaiting:           0,
						requestRunning:           0,
						kvCacheUsagePercent:      0,
						windowPeriodRequestCount: 0,
						statusError:              nil,
					},
					{
						name:                     "2",
						weight:                   0,
						requestWaiting:           0,
						requestRunning:           0,
						kvCacheUsagePercent:      0,
						windowPeriodRequestCount: 0,
						statusError:              nil,
					},
					{
						name:                     "3",
						weight:                   0,
						requestWaiting:           0,
						requestRunning:           0,
						kvCacheUsagePercent:      0,
						windowPeriodRequestCount: 0,
						statusError:              nil,
					},
					{
						name:                     "4",
						weight:                   0,
						statusError:              nil,
						requestWaiting:           454.000000,
						requestRunning:           253.000000,
						kvCacheUsagePercent:      0.631876,
						windowPeriodRequestCount: 12,
					},
				},
			},
			want:    nil,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := map[string]int{}
			for i := 0; i < 100; i++ {
				h, _ := getBestNode(tt.args.handlers)
				t.Log(h.name)
				t.Log(h.windowPeriodRequestCount)
				if _, ok := results[h.name]; ok {
					results[h.name]++
				} else {
					results[h.name] = 1
				}
			}
			t.Log(results)
		})
	}
}
