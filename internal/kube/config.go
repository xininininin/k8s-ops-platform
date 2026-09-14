package kube

import (
	"os"
	"path/filepath"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func Config(kubeconfig string) (*rest.Config, error) {
	if kubeconfig == "" {
		if config, err := rest.InClusterConfig(); err == nil {
			return config, nil
		}
		if home, err := os.UserHomeDir(); err == nil {
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}
