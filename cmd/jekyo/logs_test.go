package main

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func pod(name string, containers ...string) corev1.Pod {
	p := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name}}
	for _, c := range containers {
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: c})
	}
	return p
}

// The logs API rejects a request without a container name on a pod with
// a sidecar, so every stream must name its container (vatra/api in ui).
func TestLogStreams(t *testing.T) {
	pods := []corev1.Pod{pod("api-0", "api", "unbound"), pod("web-1", "web")}

	got := logStreams(pods, "")
	want := []logStream{
		{label: "api-0/api", pod: "api-0", container: "api"},
		{label: "api-0/unbound", pod: "api-0", container: "unbound"},
		{label: "web-1", pod: "web-1", container: "web"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stream %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	only := logStreams(pods, "unbound")
	if len(only) != 1 || only[0].container != "unbound" || only[0].pod != "api-0" {
		t.Fatalf("container filter: %+v", only)
	}
}
