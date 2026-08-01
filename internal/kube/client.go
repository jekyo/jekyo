// Package kube wraps client-go for JEKYO's needs: typed reads, dynamic
// server-side apply, and GVK→GVR mapping for the handful of kinds JEKYO
// manages.
package kube

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type Client struct {
	Typed   kubernetes.Interface
	Dynamic dynamic.Interface
	Config  *rest.Config // kept for exec/port-forward style connections
}

func New(kubeconfigPath string) (*Client, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig %s: %w", kubeconfigPath, err)
	}
	cfg.QPS = 50
	cfg.Burst = 100
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{Typed: typed, Dynamic: dyn, Config: cfg}, nil
}

// gvrs maps the kinds JEKYO compiles to their resources. A static map keeps
// deploys working without a discovery round-trip; extend when the compiler
// learns a new kind.
var gvrs = map[schema.GroupVersionKind]schema.GroupVersionResource{
	{Version: "v1", Kind: "Namespace"}:                                      {Version: "v1", Resource: "namespaces"},
	{Version: "v1", Kind: "PersistentVolumeClaim"}:                          {Version: "v1", Resource: "persistentvolumeclaims"},
	{Version: "v1", Kind: "Service"}:                                        {Version: "v1", Resource: "services"},
	{Version: "v1", Kind: "Secret"}:                                         {Version: "v1", Resource: "secrets"},
	{Version: "v1", Kind: "ConfigMap"}:                                      {Version: "v1", Resource: "configmaps"},
	{Group: "apps", Version: "v1", Kind: "Deployment"}:                      {Group: "apps", Version: "v1", Resource: "deployments"},
	{Group: "batch", Version: "v1", Kind: "CronJob"}:                        {Group: "batch", Version: "v1", Resource: "cronjobs"},
	{Group: "apps", Version: "v1", Kind: "StatefulSet"}:                     {Group: "apps", Version: "v1", Resource: "statefulsets"},
	{Group: "networking.k8s.io", Version: "v1", Kind: "Ingress"}:            {Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"},
	{Group: "projectcontour.io", Version: "v1", Kind: "HTTPProxy"}:          {Group: "projectcontour.io", Version: "v1", Resource: "httpproxies"},
}

// GVR resolves the resource for a kind JEKYO manages.
func GVR(gvk schema.GroupVersionKind) (schema.GroupVersionResource, error) {
	gvr, ok := gvrs[gvk]
	if !ok {
		return schema.GroupVersionResource{}, fmt.Errorf("unmapped kind %s", gvk)
	}
	return gvr, nil
}

// PrunableGVRs are the namespaced kinds swept during prune, i.e. everything
// the compiler can emit except the namespace itself and release secrets.
func PrunableGVRs() []schema.GroupVersionResource {
	return []schema.GroupVersionResource{
		{Group: "apps", Version: "v1", Resource: "deployments"},
		{Group: "apps", Version: "v1", Resource: "statefulsets"},
		{Group: "batch", Version: "v1", Resource: "cronjobs"},
		{Version: "v1", Resource: "services"},
		{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"},
		{Version: "v1", Resource: "configmaps"},
		{Group: "projectcontour.io", Version: "v1", Resource: "httpproxies"},
	}
}
