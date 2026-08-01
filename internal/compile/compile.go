// Package compile turns a parsed jekyo.yaml into Kubernetes objects: a
// namespace per app, Deployment or StatefulSet per service, plus Services
// and Ingresses. Output is deterministic (sorted) so diffs and content
// hashes are stable.
package compile

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/jekyo/jekyo/internal/dsl"
)

const (
	LabelApp     = "jekyo.io/app"
	LabelService = "jekyo.io/service"
	kcertLabel   = "kcert.dev/ingress"
)

// NamespaceFor groups all JEKYO apps under a recognizable prefix while
// keeping one namespace per app (isolation, future quotas).
func NamespaceFor(appName string) string {
	return "jekyo-" + appName
}

// PullSecret authenticates image pulls from one external registry host.
type PullSecret struct {
	Host     string
	Username string
	Password string
}

// Options tweak compilation.
type Options struct {
	// PullSecrets for external private registries (from `jekyo registry
	// login`); only hosts actually referenced by images are included.
	PullSecrets []PullSecret
}

const pullSecretName = "jekyo-registry-pull"

// Compile renders every object for the app, namespace first.
func Compile(app *dsl.App, opts Options) ([]runtime.Object, error) {
	objs := []runtime.Object{namespace(app)}

	pull := matchingPullSecrets(app, opts.PullSecrets)
	if len(pull) > 0 {
		sec, err := dockerConfigSecret(app, pull)
		if err != nil {
			return nil, err
		}
		objs = append(objs, sec)
	}

	// Volumes mounted by several services become standalone PVCs shared
	// between their pods (single-node RWO); single-service volumes stay
	// volumeClaimTemplates on their StatefulSet.
	shared := sharedVolumes(app)
	for _, volName := range sortedKeys(shared) {
		objs = append(objs, standalonePVC(app, volName))
	}

	for _, name := range sortedKeys(app.Services) {
		svc := app.Services[name]
		if svc.Image == "" {
			return nil, fmt.Errorf("service %s: build: must be resolved before compile (internal error) — or use image:", name)
		}
		workload, err := workload(app, name, svc, len(pull) > 0, shared)
		if err != nil {
			return nil, err
		}
		objs = append(objs, workload)
		if svc.Schedule != "" {
			continue // cron services have no Service/Ingress
		}
		if len(svc.AllPorts()) > 0 || len(svc.Expose) > 0 {
			objs = append(objs, service(app, name, svc)...)
		}
		if svc.HTTP != nil {
			objs = append(objs, ingress(app, name, svc))
		}
	}
	return objs, nil
}

// matchingPullSecrets returns the logins whose host appears in some image
// reference of the app.
func matchingPullSecrets(app *dsl.App, all []PullSecret) []PullSecret {
	var out []PullSecret
	for _, ps := range all {
		for _, svc := range app.Services {
			if strings.HasPrefix(svc.Image, ps.Host+"/") {
				out = append(out, ps)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

func dockerConfigSecret(app *dsl.App, pull []PullSecret) (*corev1.Secret, error) {
	auths := map[string]any{}
	for _, p := range pull {
		auths[p.Host] = map[string]string{
			"username": p.Username,
			"password": p.Password,
			"auth":     base64.StdEncoding.EncodeToString([]byte(p.Username + ":" + p.Password)),
		}
	}
	cfg, err := json.Marshal(map[string]any{"auths": auths})
	if err != nil {
		return nil, err
	}
	return &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: pullSecretName, Namespace: NamespaceFor(app.Name), Labels: labels(app, "")},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: cfg},
	}, nil
}

func labels(app *dsl.App, svc string) map[string]string {
	l := map[string]string{LabelApp: app.Name}
	if svc != "" {
		l[LabelService] = svc
	}
	return l
}

func namespace(app *dsl.App) *corev1.Namespace {
	return &corev1.Namespace{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{Name: NamespaceFor(app.Name), Labels: labels(app, "")},
	}
}

// sharedVolumes returns volumes mounted by more than one service.
func sharedVolumes(app *dsl.App) map[string]bool {
	count := map[string]int{}
	for _, svc := range app.Services {
		for vol := range svc.Volumes {
			count[vol]++
		}
	}
	out := map[string]bool{}
	for vol, n := range count {
		if n > 1 {
			out[vol] = true
		}
	}
	return out
}

func standalonePVC(app *dsl.App, volName string) *corev1.PersistentVolumeClaim {
	vol := app.Volumes[volName]
	pvc := &corev1.PersistentVolumeClaim{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
		ObjectMeta: metav1.ObjectMeta{Name: volName, Namespace: NamespaceFor(app.Name), Labels: labels(app, "")},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(vol.Size)},
			},
		},
	}
	if vol.Class != "" {
		pvc.Spec.StorageClassName = &vol.Class
	}
	return pvc
}

func workload(app *dsl.App, name string, svc dsl.Service, withPullSecret bool, shared map[string]bool) (runtime.Object, error) {
	container, err := container(name, svc)
	if err != nil {
		return nil, err
	}
	podSpec := corev1.PodSpec{Containers: []corev1.Container{*container}}
	if svc.GPU.Enabled() {
		rc := "nvidia"
		podSpec.RuntimeClassName = &rc
	}
	if withPullSecret {
		podSpec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: pullSecretName}}
	}

	if svc.Schedule != "" {
		podSpec.RestartPolicy = corev1.RestartPolicyOnFailure
		return &batchv1.CronJob{
			TypeMeta:   metav1.TypeMeta{APIVersion: "batch/v1", Kind: "CronJob"},
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: NamespaceFor(app.Name), Labels: labels(app, name)},
			Spec: batchv1.CronJobSpec{
				Schedule:          svc.Schedule,
				ConcurrencyPolicy: batchv1.ForbidConcurrent,
				JobTemplate: batchv1.JobTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: labels(app, name)},
					Spec: batchv1.JobSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{Labels: labels(app, name)},
							Spec:       podSpec,
						},
					},
				},
			},
		}, nil
	}

	replicas := int32(svc.ReplicaCount())
	meta := metav1.ObjectMeta{Name: name, Namespace: NamespaceFor(app.Name), Labels: labels(app, name)}
	selector := &metav1.LabelSelector{MatchLabels: labels(app, name)}
	podTmpl := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: labels(app, name)},
		Spec:       podSpec,
	}

	if !svc.IsStateful() {
		return &appsv1.Deployment{
			TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
			ObjectMeta: meta,
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: selector,
				Template: podTmpl,
			},
		}, nil
	}

	var claims []corev1.PersistentVolumeClaim
	for _, volName := range sortedKeys(svc.Volumes) {
		vm := svc.Volumes[volName]
		podTmpl.Spec.Containers[0].VolumeMounts = append(podTmpl.Spec.Containers[0].VolumeMounts,
			corev1.VolumeMount{Name: volName, MountPath: vm.Path, SubPath: vm.Subpath})
		if shared[volName] {
			// Mount the app-shared PVC instead of claiming a private one.
			podTmpl.Spec.Volumes = append(podTmpl.Spec.Volumes, corev1.Volume{
				Name: volName,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: volName},
				},
			})
			continue
		}
		vol := app.Volumes[volName]
		claim := corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: volName},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(vol.Size)},
				},
			},
		}
		if vol.Class != "" {
			claim.Spec.StorageClassName = &vol.Class
		}
		claims = append(claims, claim)
	}

	return &appsv1.StatefulSet{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"},
		ObjectMeta: meta,
		Spec: appsv1.StatefulSetSpec{
			Replicas:             &replicas,
			Selector:             selector,
			ServiceName:          name,
			Template:             podTmpl,
			VolumeClaimTemplates: claims,
		},
	}, nil
}

func container(name string, svc dsl.Service) (*corev1.Container, error) {
	c := &corev1.Container{
		Name:    name,
		Image:   svc.Image,
		Command: svc.Command,
		Args:    svc.Args,
	}
	for _, k := range sortedKeys(svc.Env) {
		c.Env = append(c.Env, corev1.EnvVar{Name: k, Value: svc.Env[k]})
	}
	for _, p := range svc.AllPorts() {
		c.Ports = append(c.Ports, corev1.ContainerPort{ContainerPort: int32(p), Name: portName(p)})
	}
	for _, e := range svc.Expose {
		proto := corev1.ProtocolTCP
		if e.Protocol == "udp" {
			proto = corev1.ProtocolUDP
		}
		c.Ports = append(c.Ports, corev1.ContainerPort{ContainerPort: int32(e.Port), Protocol: proto, Name: "x-" + strconv.Itoa(e.Port)})
	}

	if svc.GPU.Enabled() {
		devices := svc.GPU.Devices
		if devices == "" {
			devices = "all"
		}
		c.Env = append(c.Env,
			corev1.EnvVar{Name: "NVIDIA_DRIVER_CAPABILITIES", Value: "compute,utility"},
			corev1.EnvVar{Name: "NVIDIA_VISIBLE_DEVICES", Value: devices},
		)
	}

	res, err := resources(svc.Resources)
	if err != nil {
		return nil, fmt.Errorf("service %s: %w", name, err)
	}
	c.Resources = res

	if h := svc.Health; h != nil {
		probe := &corev1.Probe{}
		if len(h.Command) > 0 {
			probe.ProbeHandler = corev1.ProbeHandler{
				Exec: &corev1.ExecAction{Command: h.Command},
			}
		} else {
			port := h.Port
			if port == 0 {
				port = svc.MainPort()
			}
			probe.ProbeHandler = corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Path: h.Path, Port: intstr.FromInt32(int32(port))},
			}
		}
		c.ReadinessProbe = probe
		liveness := *probe
		liveness.FailureThreshold = 6
		liveness.PeriodSeconds = 10
		c.LivenessProbe = &liveness
	}
	return c, nil
}

func resources(r dsl.Resources) (corev1.ResourceRequirements, error) {
	out := corev1.ResourceRequirements{}
	set := func(dst *corev1.ResourceList, key corev1.ResourceName, val string) error {
		if val == "" {
			return nil
		}
		q, err := resource.ParseQuantity(val)
		if err != nil {
			return fmt.Errorf("resources: %s: %w", key, err)
		}
		if *dst == nil {
			*dst = corev1.ResourceList{}
		}
		(*dst)[key] = q
		return nil
	}
	if err := set(&out.Requests, corev1.ResourceCPU, r.CPU); err != nil {
		return out, err
	}
	if err := set(&out.Requests, corev1.ResourceMemory, r.Memory); err != nil {
		return out, err
	}
	if err := set(&out.Limits, corev1.ResourceCPU, r.CPUMax); err != nil {
		return out, err
	}
	if err := set(&out.Limits, corev1.ResourceMemory, r.MemoryMax); err != nil {
		return out, err
	}
	return out, nil
}

// service renders the ClusterIP Service (named ports) and, when expose: is
// used, a separate NodePort Service.
func service(app *dsl.App, name string, svc dsl.Service) []runtime.Object {
	var objs []runtime.Object
	if ports := svc.AllPorts(); len(ports) > 0 {
		var sp []corev1.ServicePort
		for _, p := range ports {
			sp = append(sp, corev1.ServicePort{
				Name: portName(p), Port: int32(p), TargetPort: intstr.FromInt32(int32(p)),
			})
		}
		objs = append(objs, &corev1.Service{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: NamespaceFor(app.Name), Labels: labels(app, name)},
			Spec: corev1.ServiceSpec{
				Selector: labels(app, name),
				Ports:    sp,
			},
		})
	}
	if len(svc.Expose) > 0 {
		var sp []corev1.ServicePort
		for _, e := range svc.Expose {
			proto := corev1.ProtocolTCP
			if e.Protocol == "udp" {
				proto = corev1.ProtocolUDP
			}
			sp = append(sp, corev1.ServicePort{
				Name: "x-" + strconv.Itoa(e.Port), Port: int32(e.Port),
				TargetPort: intstr.FromInt32(int32(e.Port)), NodePort: int32(e.Node), Protocol: proto,
			})
		}
		objs = append(objs, &corev1.Service{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
			ObjectMeta: metav1.ObjectMeta{Name: name + "-nodeport", Namespace: NamespaceFor(app.Name), Labels: labels(app, name)},
			Spec: corev1.ServiceSpec{
				Type:     corev1.ServiceTypeNodePort,
				Selector: labels(app, name),
				Ports:    sp,
			},
		})
	}
	return objs
}

func ingress(app *dsl.App, name string, svc dsl.Service) *networkingv1.Ingress {
	h := svc.HTTP
	path := h.Path
	if path == "" {
		path = "/"
	}
	tls := h.TLS == nil || *h.TLS
	pathType := networkingv1.PathTypePrefix

	meta := metav1.ObjectMeta{Name: name, Namespace: NamespaceFor(app.Name), Labels: labels(app, name)}
	ing := &networkingv1.Ingress{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "Ingress"},
		ObjectMeta: meta,
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{{
				Host: h.Domain,
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{{
							Path:     path,
							PathType: &pathType,
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: name,
									Port: networkingv1.ServiceBackendPort{Number: int32(svc.MainPort())},
								},
							},
						}},
					},
				},
			}},
		},
	}
	if tls {
		// The kcert label is the CertProvider seam (SPEC §2.2): kcert
		// watches labeled ingresses and issues into the named secret.
		ing.Labels[kcertLabel] = "managed"
		ing.Spec.TLS = []networkingv1.IngressTLS{{Hosts: []string{h.Domain}, SecretName: name + "-tls"}}
	}
	return ing
}

func portName(p int) string { return "p" + strconv.Itoa(p) }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
