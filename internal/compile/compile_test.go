package compile

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/jekyo/jekyo/internal/dsl"
)

func mustParse(t *testing.T, y string) *dsl.App {
	t.Helper()
	app, err := dsl.Parse([]byte(y), nil)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

const full = `
app: acme
services:
  api:
    image: ghcr.io/acme/api:1.4
    port: 8080
    replicas: 2
    http:
      domain: api.acme.com
    resources:
      cpu: 500m
      memory-max: 512Mi
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
    gpu:
      devices: "1"
volumes:
  pgdata:
    size: 10Gi
`

func TestCompileFull(t *testing.T) {
	objs, err := Compile(mustParse(t, full), Options{})
	if err != nil {
		t.Fatal(err)
	}

	var ns *corev1.Namespace
	var deps []*appsv1.Deployment
	var stss []*appsv1.StatefulSet
	var svcs []*corev1.Service
	var ings []*networkingv1.Ingress
	for _, o := range objs {
		switch v := o.(type) {
		case *corev1.Namespace:
			ns = v
		case *appsv1.Deployment:
			deps = append(deps, v)
		case *appsv1.StatefulSet:
			stss = append(stss, v)
		case *corev1.Service:
			svcs = append(svcs, v)
		case *networkingv1.Ingress:
			ings = append(ings, v)
		}
	}

	if ns == nil || ns.Name != "jekyo-acme" || ns.Labels[LabelApp] != "acme" {
		t.Fatalf("namespace: %+v", ns)
	}
	if len(deps) != 2 || len(stss) != 1 || len(svcs) != 3 || len(ings) != 1 {
		t.Fatalf("counts: dep=%d sts=%d svc=%d ing=%d", len(deps), len(stss), len(svcs), len(ings))
	}

	// api: replicas, resources, probes.
	var api *appsv1.Deployment
	for _, d := range deps {
		if d.Name == "api" {
			api = d
		}
	}
	if *api.Spec.Replicas != 2 {
		t.Fatalf("api replicas: %d", *api.Spec.Replicas)
	}
	c := api.Spec.Template.Spec.Containers[0]
	if c.Resources.Requests.Cpu().String() != "500m" || c.Resources.Limits.Memory().String() != "512Mi" {
		t.Fatalf("resources: %+v", c.Resources)
	}
	if c.ReadinessProbe == nil || c.ReadinessProbe.HTTPGet.Path != "/healthz" || c.LivenessProbe == nil {
		t.Fatalf("probes: %+v", c.ReadinessProbe)
	}

	// db: StatefulSet with claim.
	sts := stss[0]
	if sts.Name != "db" || len(sts.Spec.VolumeClaimTemplates) != 1 {
		t.Fatalf("sts: %+v", sts)
	}
	if sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests.Storage().String() != "10Gi" {
		t.Fatalf("claim size: %+v", sts.Spec.VolumeClaimTemplates[0].Spec)
	}
	if mounts := sts.Spec.Template.Spec.Containers[0].VolumeMounts; len(mounts) != 1 || mounts[0].MountPath != "/var/lib/postgresql/data" {
		t.Fatalf("mounts: %+v", mounts)
	}

	// llm: gpu runtime class + device pinning.
	var llm *appsv1.Deployment
	for _, d := range deps {
		if d.Name == "llm" {
			llm = d
		}
	}
	if llm.Spec.Template.Spec.RuntimeClassName == nil || *llm.Spec.Template.Spec.RuntimeClassName != "nvidia" {
		t.Fatal("llm should have nvidia runtime class")
	}
	envs := map[string]string{}
	for _, e := range llm.Spec.Template.Spec.Containers[0].Env {
		envs[e.Name] = e.Value
	}
	if envs["NVIDIA_VISIBLE_DEVICES"] != "1" {
		t.Fatalf("gpu devices: %+v", envs)
	}

	// ingress: kcert label, tls default on, backend to main port.
	ing := ings[0]
	if ing.Labels["kcert.dev/ingress"] != "managed" {
		t.Fatal("ingress missing kcert label")
	}
	if len(ing.Spec.TLS) != 1 || ing.Spec.TLS[0].SecretName != "api-tls" {
		t.Fatalf("tls: %+v", ing.Spec.TLS)
	}
	if ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number != 8080 {
		t.Fatalf("backend: %+v", ing.Spec.Rules[0])
	}
}

func TestCompileTLSOff(t *testing.T) {
	y := `
app: a
services:
  s:
    image: x
    port: 80
    http:
      domain: s.local
      tls: false
`
	objs, err := Compile(mustParse(t, y), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if ing, ok := o.(*networkingv1.Ingress); ok {
			if len(ing.Spec.TLS) != 0 || ing.Labels["kcert.dev/ingress"] != "" {
				t.Fatalf("tls should be off: %+v", ing)
			}
			return
		}
	}
	t.Fatal("no ingress found")
}

func TestCompileWebSocket(t *testing.T) {
	y := `
app: a
services:
  bare:
    image: x
    port: 80
    http:
      domain: bare.local
      websocket: true
  listed:
    image: x
    port: 80
    http:
      domain: listed.local
      websocket: ["/ws", "/viewer"]
  off:
    image: x
    port: 80
    http:
      domain: off.local
`
	objs, err := Compile(mustParse(t, y), Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, o := range objs {
		if ing, ok := o.(*networkingv1.Ingress); ok {
			got[ing.Name] = ing.Annotations["projectcontour.io/websocket-routes"]
		}
	}
	if got["bare"] != "/" {
		t.Fatalf("websocket:true should annotate /, got %q", got["bare"])
	}
	if got["listed"] != "/ws,/viewer" {
		t.Fatalf("path list should annotate the paths, got %q", got["listed"])
	}
	if got["off"] != "" {
		t.Fatalf("no websocket should have no annotation, got %q", got["off"])
	}
}

func TestCompileBuildRejected(t *testing.T) {
	y := `
app: a
services:
  s:
    build:
      context: .
`
	_, err := Compile(mustParse(t, y), Options{})
	if err == nil || !strings.Contains(err.Error(), "build must be resolved") {
		t.Fatalf("want build rejection, got: %v", err)
	}
}

func TestCompileCronJob(t *testing.T) {
	y := `
app: a
services:
  nightly:
    image: alpine:3.20
    command: ["sh", "-c", "echo hi"]
    schedule: "0 3 * * *"
`
	objs, err := Compile(mustParse(t, y), Options{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range objs {
		if cj, ok := o.(*batchv1.CronJob); ok {
			found = true
			if cj.Spec.Schedule != "0 3 * * *" {
				t.Fatalf("schedule: %q", cj.Spec.Schedule)
			}
			if cj.Spec.JobTemplate.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyOnFailure {
				t.Fatal("cron pods must not restart forever")
			}
		}
		if _, ok := o.(*corev1.Service); ok {
			t.Fatal("cron services must not get a Service")
		}
	}
	if !found {
		t.Fatal("no CronJob compiled")
	}
}

func TestCompilePullSecrets(t *testing.T) {
	y := `
app: a
services:
  s:
    image: ghcr.io/me/private:1
  pub:
    image: alpine:3.20
`
	objs, err := Compile(mustParse(t, y), Options{PullSecrets: []PullSecret{
		{Host: "ghcr.io", Username: "me", Password: "tok"},
		{Host: "unused.example.com", Username: "x", Password: "y"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var sec *corev1.Secret
	for _, o := range objs {
		if s, ok := o.(*corev1.Secret); ok {
			sec = s
		}
		if d, ok := o.(*appsv1.Deployment); ok {
			if len(d.Spec.Template.Spec.ImagePullSecrets) != 1 {
				t.Fatalf("%s: missing imagePullSecrets", d.Name)
			}
		}
	}
	if sec == nil || sec.Type != corev1.SecretTypeDockerConfigJson {
		t.Fatalf("pull secret: %+v", sec)
	}
	cfg := string(sec.Data[corev1.DockerConfigJsonKey])
	if !strings.Contains(cfg, "ghcr.io") || strings.Contains(cfg, "unused.example.com") {
		t.Fatalf("only referenced hosts should be included: %s", cfg)
	}
}

func TestCompileBackupCronJob(t *testing.T) {
	y := `
app: a
services:
  db:
    image: postgres:16
    volumes:
      data: /var/lib/postgresql/data
volumes:
  data:
    size: 5Gi
    backup:
      schedule: "0 3 * * *"
      keep: 14
`
	objs, err := Compile(mustParse(t, y), Options{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range objs {
		cj, ok := o.(*batchv1.CronJob)
		if !ok {
			continue
		}
		found = true
		if cj.Name != "backup-data" || cj.Spec.Schedule != "0 3 * * *" {
			t.Fatalf("cronjob: %+v", cj.ObjectMeta)
		}
		pod := cj.Spec.JobTemplate.Spec.Template.Spec
		if pod.Volumes[0].PersistentVolumeClaim.ClaimName != "data-db-0" {
			t.Fatalf("claim: %+v", pod.Volumes[0])
		}
		script := pod.Containers[0].Command[2]
		if !strings.Contains(script, "--keep-last 14") {
			t.Fatalf("keep: %s", script)
		}
		if !pod.Containers[0].VolumeMounts[0].ReadOnly {
			t.Fatal("backup mount must be read-only")
		}
	}
	if !found {
		t.Fatal("no backup CronJob compiled")
	}
}

func TestCompileDomains(t *testing.T) {
	y := `
app: a
services:
  web:
    image: x
    port: 3000
    http:
      domains: [vakyn.com, api.vakyn.com]
      path: /v1
      websocket: true
`
	objs, err := Compile(mustParse(t, y), Options{})
	if err != nil {
		t.Fatal(err)
	}
	var ing *networkingv1.Ingress
	for _, o := range objs {
		if i, ok := o.(*networkingv1.Ingress); ok {
			ing = i
		}
	}
	if ing == nil {
		t.Fatal("no ingress found")
	}
	if len(ing.Spec.Rules) != 2 {
		t.Fatalf("want one rule per domain, got %+v", ing.Spec.Rules)
	}
	for i, host := range []string{"vakyn.com", "api.vakyn.com"} {
		r := ing.Spec.Rules[i]
		p := r.HTTP.Paths[0]
		if r.Host != host || p.Path != "/v1" || p.Backend.Service.Name != "web" || p.Backend.Service.Port.Number != 3000 {
			t.Fatalf("rule %d: %+v", i, r)
		}
	}
	// one cert per host: a failed HTTP-01 challenge on one host must not
	// block the others, and the first host keeps <name>-tls so moving from
	// domain: to domains: does not re-issue its cert
	want := []networkingv1.IngressTLS{
		{Hosts: []string{"vakyn.com"}, SecretName: "web-tls"},
		{Hosts: []string{"api.vakyn.com"}, SecretName: "web-tls-api-vakyn-com"},
	}
	if len(ing.Spec.TLS) != len(want) {
		t.Fatalf("tls: %+v", ing.Spec.TLS)
	}
	for i, w := range want {
		g := ing.Spec.TLS[i]
		if g.SecretName != w.SecretName || len(g.Hosts) != 1 || g.Hosts[0] != w.Hosts[0] {
			t.Fatalf("tls[%d] = %+v, want %+v", i, g, w)
		}
	}
	if ing.Annotations["projectcontour.io/websocket-routes"] != "/v1" {
		t.Fatalf("websocket annotation: %v", ing.Annotations)
	}
}

func TestCompileRedirectDomains(t *testing.T) {
	y := `
app: a
services:
  apex:
    http:
      domains: [acme.com, www.acme.com]
      redirect: acme.io
`
	objs, err := Compile(mustParse(t, y), Options{})
	if err != nil {
		t.Fatal(err)
	}
	proxies := map[string]string{} // HTTPProxy name -> fqdn
	secrets := map[string]string{} // vhost fqdn -> tls secret
	certs := map[string]string{}   // cert-request ConfigMap name -> hosts
	for _, o := range objs {
		switch v := o.(type) {
		case *unstructured.Unstructured:
			vh := v.Object["spec"].(map[string]any)["virtualhost"].(map[string]any)
			fqdn := vh["fqdn"].(string)
			proxies[v.GetName()] = fqdn
			secrets[fqdn] = vh["tls"].(map[string]any)["secretName"].(string)
		case *corev1.ConfigMap:
			if v.Labels["kcert.dev/cert-request"] == "request" {
				certs[v.Name] = v.Data["hosts"]
			}
		}
	}
	if len(proxies) != 2 || proxies["apex"] != "acme.com" || proxies["apex-www-acme-com"] != "www.acme.com" {
		t.Fatalf("proxies: %v", proxies)
	}
	if secrets["acme.com"] != "apex-tls" || secrets["www.acme.com"] != "apex-tls-www-acme-com" {
		t.Fatalf("secrets: %v", secrets)
	}
	if len(certs) != 2 || certs["apex-tls"] != "acme.com" || certs["apex-tls-www-acme-com"] != "www.acme.com" {
		t.Fatalf("cert requests: %v", certs)
	}
}

// Without a cluster backup target the CronJob would fail every run on its
// missing secret, so it is rendered suspended and the deploy succeeds
// (issue #28).
func TestCompileBackupWithoutTarget(t *testing.T) {
	y := `
app: a
services:
  db:
    image: postgres:16
    volumes:
      data: /var/lib/postgresql/data
volumes:
  data:
    size: 5Gi
    backup:
      schedule: "0 3 * * *"
`
	for _, missing := range []bool{false, true} {
		objs, err := Compile(mustParse(t, y), Options{NoBackupTarget: missing})
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range objs {
			if cj, ok := o.(*batchv1.CronJob); ok {
				suspended := cj.Spec.Suspend != nil && *cj.Spec.Suspend
				if suspended != missing {
					t.Fatalf("NoBackupTarget=%v: suspend=%v", missing, suspended)
				}
				if missing && cj.Annotations[BackupSuspendedAnnotation] == "" {
					t.Fatalf("suspended cronjob should say why: %v", cj.Annotations)
				}
			}
		}
	}
}
