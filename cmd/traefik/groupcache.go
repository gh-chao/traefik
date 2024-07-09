package main

import (
	"context"
	"fmt"
	"github.com/gh-chao/groupcache"
	kubernetesDiscover "github.com/gh-chao/groupcache/kubernetes"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"net/http"
	"os"
)

func setupGroupCache(ctx context.Context) {
	lg := logrus.WithField("component", "groupcache")

	inClusterConfig, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}
	client, err := kubernetes.NewForConfig(inClusterConfig)
	if err != nil {
		panic(err.Error())
	}

	selfAddress := fmt.Sprintf("http://%s:20240", kubernetesDiscover.GetLocalIP())
	lg.Debugf("Self Address: %s", selfAddress)

	pool := groupcache.NewHTTPPoolWithWorkspace(groupcache.DefaultWorkspace, selfAddress)

	namespace := os.Getenv("POD_NAMESPACE")
	selector := os.Getenv("GROUP_CACHE_ENDPOINT_SELECTOR")

	discover := kubernetesDiscover.NewDiscover(pool, client, namespace, selector, "20240", lg)
	err = discover.WatchEndpoint(ctx)
	if err != nil {
		panic(err.Error())
	}

	server := http.Server{
		Addr:    ":20240",
		Handler: pool,
	}

	go func() {
		lg.Debugf("start groupcache server")
		err := server.ListenAndServe()
		if err != nil {
			lg.Fatalf("Failed to start HTTP server - %v", err)
		}
	}()
}
