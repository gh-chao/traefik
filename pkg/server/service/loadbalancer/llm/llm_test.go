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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getScore(tt.args.handler); got != tt.want {
				t.Errorf("getScore() = %v, want %v", got, tt.want)
			}
		})
	}
}
