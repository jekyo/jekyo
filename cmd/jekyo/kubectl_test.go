package main

import (
	"reflect"
	"testing"
)

// kubectl disables flag parsing, so jekyo's global flags arrive in args
// and used to be handed to kubectl (issue #27).
func TestSplitKubectlArgs(t *testing.T) {
	cases := []struct {
		in       []string
		ctx      string
		wantArgs []string
	}{
		{[]string{"--context", "gpu", "get", "pods"}, "gpu", []string{"get", "pods"}},
		{[]string{"--context=gpu", "--", "get", "pods"}, "gpu", []string{"get", "pods"}},
		{[]string{"--ssh-key", "/k", "--context", "gpu", "--", "logs", "x"}, "gpu", []string{"logs", "x"}},
		{[]string{"--", "get", "pods", "-n", "kube-system"}, "", []string{"get", "pods", "-n", "kube-system"}},
		// after --, everything is kubectl's, even a --context
		{[]string{"--", "get", "--context", "x"}, "", []string{"get", "--context", "x"}},
		{[]string{"get", "pods", "-A"}, "", []string{"get", "pods", "-A"}},
	}
	for _, c := range cases {
		ctx, args := splitKubectlArgs(c.in)
		if ctx != c.ctx || !reflect.DeepEqual(args, c.wantArgs) {
			t.Errorf("%v: got ctx=%q args=%v, want ctx=%q args=%v", c.in, ctx, args, c.ctx, c.wantArgs)
		}
	}
}
