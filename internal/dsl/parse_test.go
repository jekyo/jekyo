package dsl

import (
	"strings"
	"testing"
)

const sample = `
app: acme
services:
  api:
    image: ghcr.io/acme/api:1.4
    env:
      DB_URL: postgres://db:5432/acme
      API_KEY: ${API_KEY}
      LITERAL: cost is $$5
    port: 8080
    http:
      domain: api.acme.com
    resources:
      cpu: 500m
      memory-max: 512Mi
    replicas: 2
    health:
      path: /healthz
  db:
    image: pgvector/pgvector:pg16
    port: 5432
    volumes:
      pgdata: /var/lib/postgresql/data
  llm:
    image: vllm/vllm-openai:latest
    port: 5000
    gpu: 2
volumes:
  pgdata:
    size: 10Gi
`

func TestParseSample(t *testing.T) {
	app, err := Parse([]byte(sample), map[string]string{"API_KEY": "sk-123"})
	if err != nil {
		t.Fatal(err)
	}
	if app.Name != "acme" || len(app.Services) != 3 {
		t.Fatalf("app: %+v", app)
	}
	api := app.Services["api"]
	if api.Env["API_KEY"] != "sk-123" {
		t.Fatalf("interpolation: %q", api.Env["API_KEY"])
	}
	if api.Env["LITERAL"] != "cost is $5" {
		t.Fatalf("$$ escape: %q", api.Env["LITERAL"])
	}
	if api.IsStateful() || api.ReplicaCount() != 2 || api.MainPort() != 8080 {
		t.Fatalf("api: %+v", api)
	}
	db := app.Services["db"]
	if !db.IsStateful() {
		t.Fatal("db with volumes should be stateful")
	}
	llm := app.Services["llm"]
	if !llm.GPU.Enabled() || llm.GPU.Count != 2 {
		t.Fatalf("gpu scalar: %+v", llm.GPU)
	}
}

func TestParseGPUMapping(t *testing.T) {
	y := `
app: a
services:
  s:
    image: x
    gpu:
      count: 1
      devices: "0,2"
`
	app, err := Parse([]byte(y), nil)
	if err != nil {
		t.Fatal(err)
	}
	g := app.Services["s"].GPU
	if g.Devices != "0,2" || g.Count != 1 {
		t.Fatalf("gpu: %+v", g)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct{ name, yaml, want string }{
		{"unknown key", "app: a\nservices:\n  s:\n    image: x\n    prot: 80\n", "prot"},
		{"missing image", "app: a\nservices:\n  s:\n    port: 80\n", "image: or build:"},
		{"image and build", "app: a\nservices:\n  s:\n    image: x\n    build:\n      context: .\n", "mutually exclusive"},
		{"undeclared volume", "app: a\nservices:\n  s:\n    image: x\n    volumes:\n      d: /data\n", "not declared"},
		{"unused volume", "app: a\nservices:\n  s:\n    image: x\nvolumes:\n  d:\n    size: 1Gi\n", "not mounted"},
		{"bad quantity", "app: a\nservices:\n  s:\n    image: x\n    resources:\n      cpu: half\n", "invalid quantity"},
		{"http without domain", "app: a\nservices:\n  s:\n    image: x\n    port: 80\n    http:\n      path: /\n", "http.domain"},
		{"domain and domains", "app: a\nservices:\n  s:\n    image: x\n    port: 80\n    http:\n      domain: a.com\n      domains: [b.com]\n", "mutually exclusive"},
		{"empty domains", "app: a\nservices:\n  s:\n    image: x\n    port: 80\n    http:\n      domains: []\n", "http.domain"},
		{"force-https without tls", "app: a\nservices:\n  s:\n    image: x\n    port: 80\n    http:\n      domain: a.com\n      tls: false\n      force-https: true\n", "force-https needs http.tls"},
		{"force-https on redirect", "app: a\nservices:\n  s:\n    http:\n      domain: a.com\n      redirect: b.com\n      force-https: true\n", "no effect on a redirect"},
		{"comma in domain", "app: a\nservices:\n  s:\n    image: x\n    port: 80\n    http:\n      domain: \"a.com,b.com\"\n", `"a.com,b.com" is not a valid domain`},
		{"space in domains", "app: a\nservices:\n  s:\n    image: x\n    port: 80\n    http:\n      domains: [a.com, \"b .com\"]\n", `"b .com" is not a valid domain`},
		{"wildcard domain", "app: a\nservices:\n  s:\n    image: x\n    port: 80\n    http:\n      domain: \"*.a.com\"\n", `"*.a.com" is not a valid domain`},
		{"uppercase domain", "app: a\nservices:\n  s:\n    image: x\n    port: 80\n    http:\n      domain: A.com\n", `"A.com" is not a valid domain`},
		{"empty domain in list", "app: a\nservices:\n  s:\n    image: x\n    port: 80\n    http:\n      domains: [a.com, \"\"]\n", `"" is not a valid domain`},
		{"duplicate in list", "app: a\nservices:\n  s:\n    image: x\n    port: 80\n    http:\n      domains: [a.com, a.com]\n", "a.com is listed twice"},
		{"bad timeout", "app: a\nservices:\n  s:\n    image: x\n    port: 80\n    http:\n      domain: a.com\n      timeout: 5 minutes\n", "http.timeout"},
		{"zero timeout", "app: a\nservices:\n  s:\n    image: x\n    port: 80\n    http:\n      domain: a.com\n      timeout: 0s\n", "http.timeout"},
		{"redirect timeout", "app: a\nservices:\n  s:\n    http:\n      domain: a.com\n      redirect: b.com\n      timeout: 30s\n", "no effect on a redirect"},
		{"duplicate across services", "app: a\nservices:\n  s:\n    image: x\n    port: 80\n    http:\n      domains: [a.com, b.com]\n  t:\n    image: x\n    port: 80\n    http:\n      domain: b.com\n", "b.com is also served by service s"},
		{"missing env var", "app: a\nservices:\n  s:\n    image: x\n    env:\n      K: ${NOPE_UNSET}\n", "NOPE_UNSET"},
		{"bad nodeport", "app: a\nservices:\n  s:\n    image: x\n    expose:\n      - port: 53\n        node: 100\n", "NodePort range"},
		{"stateful replicas", "app: a\nservices:\n  s:\n    image: x\n    replicas: 3\n    volumes:\n      d: /data\nvolumes:\n  d:\n    size: 1Gi\n", "replicas > 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml), nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got: %v", tc.want, err)
			}
		})
	}
}

func TestBuildInlineAndDockerfileExclusive(t *testing.T) {
	y := `
app: a
services:
  s:
    build:
      context: .
      dockerfile: Dockerfile
      inline: |
        FROM alpine
`
	_, err := Parse([]byte(y), nil)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("got: %v", err)
	}
}

func TestHTTPHosts(t *testing.T) {
	y := `
app: a
services:
  one:
    image: x
    port: 80
    http:
      domain: one.com
  many:
    image: x
    port: 80
    http:
      domains: [many.com, api.many.com]
`
	app, err := Parse([]byte(y), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := app.Services["one"].HTTP.Hosts(); len(got) != 1 || got[0] != "one.com" {
		t.Fatalf("domain: hosts = %v", got)
	}
	if got := app.Services["many"].HTTP.Hosts(); len(got) != 2 || got[0] != "many.com" || got[1] != "api.many.com" {
		t.Fatalf("domains: hosts = %v", got)
	}
}
