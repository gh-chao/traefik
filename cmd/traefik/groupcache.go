package main

import (
	"context"
	"github.com/gh-chao/groupcache"
	kubernetesDiscover "github.com/gh-chao/groupcache/kubernetes"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"os"
)

func setupGroupCache(ctx context.Context) {
	inClusterConfig, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}
	client, err := kubernetes.NewForConfig(inClusterConfig)
	if err != nil {
		panic(err.Error())
	}

	pool := groupcache.NewHTTPPoolWithWorkspace(groupcache.DefaultWorkspace, kubernetesDiscover.GetLocalIP())

	selector := os.Getenv("GROUP_CACHE_ENDPOINT_SELECTOR")

	discover := kubernetesDiscover.NewDiscover(pool, client, "default", selector, "20240", logrus.WithField("component", "groupcache"))
	err = discover.WatchEndpoint(ctx)
	if err != nil {
		panic(err.Error())
	}
}
