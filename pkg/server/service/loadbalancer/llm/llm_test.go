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
