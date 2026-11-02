package compile

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Issue #35: a scheduled service mounts its volume from a standalone PVC,
// because a CronJob has no claim template to hold one.
func TestCompileCronJobMountsVolume(t *testing.T) {
	y := `
app: a
services:
  feeder:
    image: alpine
    schedule: "0 3 * * *"
    volumes:
      state: /var/lib/feeder
volumes:
  state:
    size: 1Gi
`
	objs, err := Compile(mustParse(t, y), Options{})
	if err != nil {
		t.Fatal(err)
	}
	var cj *batchv1.CronJob
	var pvc *corev1.PersistentVolumeClaim
	for _, o := range objs {
		switch v := o.(type) {
		case *batchv1.CronJob:
			cj = v
		case *corev1.PersistentVolumeClaim:
			pvc = v
		}
	}
	if pvc == nil || pvc.Name != "state" {
		t.Fatalf("want a standalone PVC named state, got %+v", pvc)
	}
	if cj == nil {
		t.Fatal("no CronJob")
	}
	spec := cj.Spec.JobTemplate.Spec.Template.Spec
	if len(spec.Volumes) != 1 || spec.Volumes[0].PersistentVolumeClaim == nil ||
		spec.Volumes[0].PersistentVolumeClaim.ClaimName != "state" {
		t.Fatalf("pod volumes: %+v", spec.Volumes)
	}
	mounts := spec.Containers[0].VolumeMounts
	if len(mounts) != 1 || mounts[0].MountPath != "/var/lib/feeder" {
		t.Fatalf("mounts: %+v", mounts)
	}
	// Forbid is what keeps two runs off one RWO claim.
	if cj.Spec.ConcurrencyPolicy != batchv1.ForbidConcurrent {
		t.Fatalf("concurrency policy: %v", cj.Spec.ConcurrencyPolicy)
	}
}

// Issue #36: every replica gets its own claim, and every claim gets its own
// backup CronJob and its own restic repository. A single job would protect
// replica 0 only.
func TestCompilePerReplicaClaimsAndBackups(t *testing.T) {
	y := `
app: a
services:
  worker:
    image: x
    replicas: 3
    volumes:
      data: /var/lib/app
volumes:
  data:
    size: 10Gi
    per-replica: true
    backup:
      schedule: "0 3 * * *"
      keep: 5
`
	app := mustParse(t, y)
	claims := ClaimNamesFor(app, "data", false)
	want := []string{"data-worker-0", "data-worker-1", "data-worker-2"}
	if len(claims) != 3 {
		t.Fatalf("claims: %v", claims)
	}
	for i := range want {
		if claims[i] != want[i] {
			t.Fatalf("claims: got %v want %v", claims, want)
		}
	}

	objs, err := Compile(app, Options{})
	if err != nil {
		t.Fatal(err)
	}
	repos := map[string]string{}
	for _, o := range objs {
		cj, ok := o.(*batchv1.CronJob)
		if !ok {
			continue
		}
		var claim, repo string
		spec := cj.Spec.JobTemplate.Spec.Template.Spec
		for _, v := range spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				claim = v.PersistentVolumeClaim.ClaimName
			}
		}
		for _, e := range spec.Containers[0].Env {
			if e.Name == "RESTIC_REPOSITORY" {
				repo = e.Value
			}
		}
		repos[cj.Name] = claim + " " + repo
	}
	if len(repos) != 3 {
		t.Fatalf("want one backup CronJob per replica, got %d: %v", len(repos), repos)
	}
	for i, name := range []string{"backup-data-0", "backup-data-1", "backup-data-2"} {
		got, ok := repos[name]
		if !ok {
			t.Fatalf("missing %s (have %v)", name, repos)
		}
		if !strings.HasPrefix(got, want[i]+" ") {
			t.Errorf("%s backs up %q, want claim %s", name, got, want[i])
		}
		if !strings.HasSuffix(got, "/a/data/r"+string(rune('0'+i))) {
			t.Errorf("%s repository %q must end in /r%d so replicas do not share one", name, got, i)
		}
	}

	// The StatefulSet still carries one claim template; Kubernetes expands
	// it per ordinal.
	for _, o := range objs {
		if sts, ok := o.(*appsv1.StatefulSet); ok {
			if len(sts.Spec.VolumeClaimTemplates) != 1 {
				t.Fatalf("claim templates: %+v", sts.Spec.VolumeClaimTemplates)
			}
			if *sts.Spec.Replicas != 3 {
				t.Fatalf("replicas: %d", *sts.Spec.Replicas)
			}
		}
	}
}

// A volume that is not per-replica keeps the historical single job and
// repository path, so existing backups stay where they are.
func TestCompileSingleClaimBackupUnchanged(t *testing.T) {
	y := `
app: a
services:
  db:
    image: x
    volumes:
      data: /data
volumes:
  data:
    size: 1Gi
    backup:
      schedule: "0 3 * * *"
`
	objs, err := Compile(mustParse(t, y), Options{})
	if err != nil {
		t.Fatal(err)
	}
	var found int
	for _, o := range objs {
		cj, ok := o.(*batchv1.CronJob)
		if !ok {
			continue
		}
		found++
		if cj.Name != "backup-data" {
			t.Errorf("name: %s", cj.Name)
		}
		for _, e := range cj.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Env {
			if e.Name == "RESTIC_REPOSITORY" && !strings.HasSuffix(e.Value, "/a/data") {
				t.Errorf("repository must stay /a/data, got %q", e.Value)
			}
		}
	}
	if found != 1 {
		t.Fatalf("want 1 backup CronJob, got %d", found)
	}
}

// Issue #37: gpu: N requests the resource so the scheduler accounts for it,
// and no longer hands out every card.
func TestCompileGPURequestsResource(t *testing.T) {
	y := "app: a\nservices:\n  s:\n    image: x\n    gpu: 2\n"
	c := containerOf(t, mustParseCompile(t, y))
	q, ok := c.Resources.Limits[GPUResourceName]
	if !ok {
		t.Fatalf("no %s request: %+v", GPUResourceName, c.Resources)
	}
	if q.Value() != 2 {
		t.Fatalf("want 2 GPUs, got %d", q.Value())
	}
	for _, e := range c.Env {
		if e.Name == "NVIDIA_VISIBLE_DEVICES" {
			t.Fatalf("NVIDIA_VISIBLE_DEVICES=%q must be left to the device plugin", e.Value)
		}
	}
}

// Explicit pinning stays an opt-out: no resource request, cards named.
func TestCompileGPUExplicitDevicesBypassesScheduling(t *testing.T) {
	y := "app: a\nservices:\n  s:\n    image: x\n    gpu:\n      count: 1\n      devices: \"1\"\n"
	c := containerOf(t, mustParseCompile(t, y))
	if _, ok := c.Resources.Limits[GPUResourceName]; ok {
		t.Fatal("pinned devices must not also request the resource")
	}
	var devices string
	for _, e := range c.Env {
		if e.Name == "NVIDIA_VISIBLE_DEVICES" {
			devices = e.Value
		}
	}
	if devices != "1" {
		t.Fatalf("NVIDIA_VISIBLE_DEVICES: %q", devices)
	}
}

func TestCompileNoGPUNoRequest(t *testing.T) {
	y := "app: a\nservices:\n  s:\n    image: x\n    port: 80\n"
	c := containerOf(t, mustParseCompile(t, y))
	if _, ok := c.Resources.Limits[GPUResourceName]; ok {
		t.Fatal("a service without gpu: must not request one")
	}
}

// Issue #33: apparmor maps 1:1 onto securityContext.appArmorProfile.
func TestCompileAppArmorProfile(t *testing.T) {
	cases := []struct {
		value string
		typ   corev1.AppArmorProfileType
		local string
	}{
		{"runtime-default", corev1.AppArmorProfileTypeRuntimeDefault, ""},
		{"unconfined", corev1.AppArmorProfileTypeUnconfined, ""},
		{"localhost/jekyo-pg", corev1.AppArmorProfileTypeLocalhost, "jekyo-pg"},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			y := "app: a\nservices:\n  s:\n    image: x\n    security:\n      apparmor: " + tc.value + "\n"
			c := containerOf(t, mustParseCompile(t, y))
			p := c.SecurityContext.AppArmorProfile
			if p == nil {
				t.Fatal("no appArmorProfile")
			}
			if p.Type != tc.typ {
				t.Fatalf("type: %v", p.Type)
			}
			if tc.local == "" {
				if p.LocalhostProfile != nil {
					t.Fatalf("localhostProfile set: %v", *p.LocalhostProfile)
				}
			} else if p.LocalhostProfile == nil || *p.LocalhostProfile != tc.local {
				t.Fatalf("localhostProfile: %v", p.LocalhostProfile)
			}
		})
	}
}

// Unset must leave the field alone so a node with a corrected default
// keeps it, rather than being pinned to runtime-default.
func TestCompileAppArmorUnsetLeavesFieldEmpty(t *testing.T) {
	y := "app: a\nservices:\n  s:\n    image: x\n    security:\n      run-as: 1000\n"
	c := containerOf(t, mustParseCompile(t, y))
	if c.SecurityContext.AppArmorProfile != nil {
		t.Fatalf("appArmorProfile must stay unset, got %+v", c.SecurityContext.AppArmorProfile)
	}
}

// mustParseCompile parses and compiles, returning the objects.
func mustParseCompile(t *testing.T, y string) []runtime.Object {
	t.Helper()
	objs, err := Compile(mustParse(t, y), Options{})
	if err != nil {
		t.Fatal(err)
	}
	return objs
}

// containerOf returns the first workload's first container.
func containerOf(t *testing.T, objs []runtime.Object) corev1.Container {
	t.Helper()
	for _, o := range objs {
		switch v := o.(type) {
		case *appsv1.Deployment:
			return v.Spec.Template.Spec.Containers[0]
		case *appsv1.StatefulSet:
			return v.Spec.Template.Spec.Containers[0]
		case *batchv1.CronJob:
			return v.Spec.JobTemplate.Spec.Template.Spec.Containers[0]
		}
	}
	t.Fatal("no workload found")
	return corev1.Container{}
}
