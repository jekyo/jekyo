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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

// BackupSecretName holds the cluster's S3 target + restic password; written
// to kube-system by `jekyo backup config` and copied into app namespaces at
// deploy time.
const BackupSecretName = "jekyo-backup"

// ResticImage is the pinned backup runner.
const ResticImage = "restic/restic:0.17.3"

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

	for _, volName := range sortedKeys(app.Volumes) {
		if app.Volumes[volName].Backup != nil {
			objs = append(objs, backupCronJob(app, volName, shared[volName]))
		}
	}

	for _, name := range sortedKeys(app.Services) {
		svc := app.Services[name]
		if svc.HTTP != nil && svc.HTTP.Redirect != "" {
			// pure routing rule: the ingress answers, nothing runs
			objs = append(objs, redirectObjects(app, name, svc.HTTP)...)
			continue
		}
		if svc.Image == "" {
			return nil, fmt.Errorf("service %s: build must be resolved before compile (internal error)", name)
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
		if len(svc.Secrets) > 0 {
			data := map[string]string{}
			for k, v := range svc.Secrets {
				data[k] = v
			}
			objs = append(objs, &corev1.Secret{
				TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
				ObjectMeta: metav1.ObjectMeta{Name: secretsName(name), Namespace: NamespaceFor(app.Name), Labels: labels(app, name)},
				StringData: data,
			})
		}
		plainFiles, secretFiles := splitFiles(svc)
		if len(plainFiles) > 0 {
			data := map[string]string{}
			for mount, f := range plainFiles {
				data[fileKey(mount)] = f.Content
			}
			objs = append(objs, &corev1.ConfigMap{
				TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
				ObjectMeta: metav1.ObjectMeta{Name: filesName(name, false), Namespace: NamespaceFor(app.Name), Labels: labels(app, name)},
				Data:       data,
			})
		}
		if len(secretFiles) > 0 {
			data := map[string]string{}
			for mount, f := range secretFiles {
				data[fileKey(mount)] = f.Content
			}
			objs = append(objs, &corev1.Secret{
				TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
				ObjectMeta: metav1.ObjectMeta{Name: filesName(name, true), Namespace: NamespaceFor(app.Name), Labels: labels(app, name)},
				StringData: data,
			})
		}
		if n := svc.Network; n != nil && n.Egress != "" {
			objs = append(objs, egressPolicy(app, name, n))
		}
	}
	return objs, nil
}

// egressPolicy renders the named egress preset as a NetworkPolicy
// (issue #5). k3s ships a policy controller, so these are enforced.
func egressPolicy(app *dsl.App, name string, n *dsl.Network) *networkingv1.NetworkPolicy {
	proto := corev1.ProtocolUDP
	dnsPort := intstr.FromInt32(53)
	dnsTCP := corev1.ProtocolTCP
	rules := []networkingv1.NetworkPolicyEgressRule{{
		// DNS stays open in every preset
		Ports: []networkingv1.NetworkPolicyPort{
			{Protocol: &proto, Port: &dnsPort},
			{Protocol: &dnsTCP, Port: &dnsPort},
		},
	}}
	switch n.Egress {
	case "restricted":
		// public internet only: private ranges, link-local/metadata and
		// the cluster are unreachable
		rules = append(rules, networkingv1.NetworkPolicyEgressRule{
			To: []networkingv1.NetworkPolicyPeer{{
				IPBlock: &networkingv1.IPBlock{
					CIDR: "0.0.0.0/0",
					Except: []string{
						"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16",
					},
				},
			}},
		})
	case "internal":
		rules = append(rules, networkingv1.NetworkPolicyEgressRule{
			To: []networkingv1.NetworkPolicyPeer{
				{IPBlock: &networkingv1.IPBlock{CIDR: "10.42.0.0/16"}},
				{IPBlock: &networkingv1.IPBlock{CIDR: "10.43.0.0/16"}},
			},
		})
	}
	for _, cidr := range n.Allow {
		rules = append(rules, networkingv1.NetworkPolicyEgressRule{
			To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: cidr}}},
		})
	}
	return &networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: metav1.ObjectMeta{Name: name + "-egress", Namespace: NamespaceFor(app.Name), Labels: labels(app, name)},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: labels(app, name)},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      rules,
		},
	}
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
			AccessModes: []corev1.PersistentVolumeAccessMode{accessMode(vol)},
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

// ClaimNameFor resolves the PVC name a volume is stored under: shared
// volumes use a standalone claim named after the volume; single-service
// volumes live in the StatefulSet's claim template (<vol>-<service>-0).
func ClaimNameFor(app *dsl.App, volName string, isShared bool) string {
	if isShared {
		return volName
	}
	for _, svcName := range sortedKeys(app.Services) {
		if _, ok := app.Services[svcName].Volumes[volName]; ok {
			return volName + "-" + svcName + "-0"
		}
	}
	return volName
}

// backupCronJob renders the scheduled restic backup for one volume. The
// jekyo-backup secret provides the S3 target and restic password; the
// repository path is namespaced per app/volume.
func backupCronJob(app *dsl.App, volName string, isShared bool) *batchv1.CronJob {
	b := app.Volumes[volName].Backup
	script := fmt.Sprintf(
		"restic snapshots >/dev/null 2>&1 || restic init; "+
			"restic backup /data --tag jekyo && "+
			"restic forget --keep-last %d --prune",
		b.KeepCount())
	podSpec := BackupPodSpec(app.Name, volName, ClaimNameFor(app, volName, isShared), []string{"/bin/sh", "-c", script}, false)
	l := labels(app, "")
	l["jekyo.io/volume"] = volName
	l["jekyo.io/backup"] = "true"
	return &batchv1.CronJob{
		TypeMeta:   metav1.TypeMeta{APIVersion: "batch/v1", Kind: "CronJob"},
		ObjectMeta: metav1.ObjectMeta{Name: "backup-" + volName, Namespace: NamespaceFor(app.Name), Labels: l},
		Spec: batchv1.CronJobSpec{
			Schedule:                   b.Schedule,
			ConcurrencyPolicy:          batchv1.ForbidConcurrent,
			SuccessfulJobsHistoryLimit: ptrInt32(1),
			FailedJobsHistoryLimit:     ptrInt32(2),
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: l},
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: l},
						Spec:       podSpec,
					},
				},
			},
		},
	}
}

// LocalBackupHostPath is where local backup repositories live on the
// node; mount a dedicated disk there for real durability.
const LocalBackupHostPath = "/var/lib/jekyo/backups"

// BackupPodSpec is the shared pod shape for scheduled backups and the
// on-demand backup/ls/restore jobs the CLI creates. The local backup
// directory is always mounted; S3 targets simply never touch it.
func BackupPodSpec(appName, volName, claimName string, command []string, readWrite bool) corev1.PodSpec {
	env := []corev1.EnvVar{
		{Name: "JEKYO_BACKUP_REPO_BASE", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: BackupSecretName}, Key: "repo-base"}}},
		{Name: "AWS_ACCESS_KEY_ID", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: BackupSecretName}, Key: "access-key"}}},
		{Name: "AWS_SECRET_ACCESS_KEY", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: BackupSecretName}, Key: "secret-key"}}},
		{Name: "RESTIC_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: BackupSecretName}, Key: "restic-password"}}},
		{Name: "RESTIC_REPOSITORY", Value: fmt.Sprintf("$(JEKYO_BACKUP_REPO_BASE)/%s/%s", appName, volName)},
	}
	return corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyOnFailure,
		Containers: []corev1.Container{{
			Name:    "restic",
			Image:   ResticImage,
			Command: command,
			Env:     env,
			VolumeMounts: []corev1.VolumeMount{
				{Name: "data", MountPath: "/data", ReadOnly: !readWrite},
				{Name: "local-repo", MountPath: "/repo"},
			},
		}},
		Volumes: []corev1.Volume{
			{
				Name: "data",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claimName},
				},
			},
			{
				Name: "local-repo",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: LocalBackupHostPath,
						Type: func() *corev1.HostPathType { t := corev1.HostPathDirectoryOrCreate; return &t }(),
					},
				},
			},
		},
	}
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
	if svc.StopGrace > 0 {
		g := int64(svc.StopGrace)
		podSpec.TerminationGracePeriodSeconds = &g // issue #7
	}
	if svc.Shm != "" {
		// browser workloads need a real /dev/shm (issue #3)
		q := resource.MustParse(svc.Shm)
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name: "shm",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory, SizeLimit: &q},
			},
		})
		podSpec.Containers[0].VolumeMounts = append(podSpec.Containers[0].VolumeMounts,
			corev1.VolumeMount{Name: "shm", MountPath: "/dev/shm"})
	}
	if n := svc.Network; n != nil && n.Host {
		podSpec.HostNetwork = true
		podSpec.DNSPolicy = corev1.DNSClusterFirstWithHostNet // issue #2
	}
	if pl := svc.Placement; pl != nil {
		podSpec.NodeSelector = pl.Selector
		for _, t := range pl.Tolerate {
			tol := corev1.Toleration{Key: t.Key, Value: t.Value, Effect: corev1.TaintEffect(t.Effect)}
			if t.Value == "" {
				tol.Operator = corev1.TolerationOpExists
			} else {
				tol.Operator = corev1.TolerationOpEqual
			}
			podSpec.Tolerations = append(podSpec.Tolerations, tol) // issue #11
		}
	}
	// files: mounted with subPath so directories are not shadowed (issue #8)
	plainFiles, secretFiles := splitFiles(svc)
	if len(plainFiles) > 0 {
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name: "files",
			VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: filesName(name, false)},
			}},
		})
		for _, mount := range sortedKeys(plainFiles) {
			podSpec.Containers[0].VolumeMounts = append(podSpec.Containers[0].VolumeMounts,
				corev1.VolumeMount{Name: "files", MountPath: mount, SubPath: fileKey(mount)})
		}
	}
	if len(secretFiles) > 0 {
		mode := int32(0o600)
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name: "files-secret",
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName: filesName(name, true), DefaultMode: &mode,
			}},
		})
		for _, mount := range sortedKeys(secretFiles) {
			podSpec.Containers[0].VolumeMounts = append(podSpec.Containers[0].VolumeMounts,
				corev1.VolumeMount{Name: "files-secret", MountPath: mount, SubPath: fileKey(mount)})
		}
	}
	// init containers inherit image, env, secrets and mounts (issue #13)
	for _, iname := range sortedKeys(svc.Init) {
		ic := svc.Init[iname]
		img := ic.Image
		if img == "" {
			img = svc.Image
		}
		initC := corev1.Container{Name: "init-" + iname, Image: img, Command: ic.Command, Args: ic.Args}
		initC.SecurityContext = securityContext(ic.Caps, ic.Security)
		initC.Env = append(initC.Env, podSpec.Containers[0].Env...)
		for _, k := range sortedKeys(ic.Env) {
			initC.Env = append(initC.Env, corev1.EnvVar{Name: k, Value: ic.Env[k]})
		}
		podSpec.InitContainers = append(podSpec.InitContainers, initC)
	}
	// sidecars share the pod (issue #9)
	for _, scName := range sortedKeys(svc.Sidecars) {
		sc := svc.Sidecars[scName]
		side := corev1.Container{Name: scName, Image: sc.Image, Command: sc.Command, Args: sc.Args}
		side.SecurityContext = securityContext(sc.Caps, sc.Security)
		for _, k := range sortedKeys(sc.Env) {
			side.Env = append(side.Env, corev1.EnvVar{Name: k, Value: sc.Env[k]})
		}
		for _, p := range sc.AllPorts() {
			side.Ports = append(side.Ports, corev1.ContainerPort{ContainerPort: int32(p), Name: scName[:min(9, len(scName))] + "-" + strconv.Itoa(p)})
		}
		if res, err := resources(sc.Resources); err == nil {
			side.Resources = res
		}
		for _, volName := range sortedKeys(sc.Volumes) {
			vm := sc.Volumes[volName]
			side.VolumeMounts = append(side.VolumeMounts,
				corev1.VolumeMount{Name: volName, MountPath: vm.Path, SubPath: vm.Subpath})
		}
		podSpec.Containers = append(podSpec.Containers, side)
	}
	if svc.Metrics != nil || len(svc.Init) > 0 {
		// init volume mounts follow after volumes are wired below
		_ = svc.Metrics
	}

	if svc.Schedule != "" {
		podSpec.RestartPolicy = corev1.RestartPolicyOnFailure
		return &batchv1.CronJob{
			TypeMeta:   metav1.TypeMeta{APIVersion: "batch/v1", Kind: "CronJob"},
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: NamespaceFor(app.Name), Labels: labels(app, name)},
			Spec: batchv1.CronJobSpec{
				Schedule:                   svc.Schedule,
				ConcurrencyPolicy:          batchv1.ForbidConcurrent,
				SuccessfulJobsHistoryLimit: ptrInt32(1),
				FailedJobsHistoryLimit:     ptrInt32(2),
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
	if m := svc.Metrics; m != nil {
		// scrape annotations; a ServiceMonitor can layer on later (issue #12)
		path := m.Path
		if path == "" {
			path = "/metrics"
		}
		port := m.Port
		if port == 0 {
			port = svc.MainPort()
		}
		podTmpl.Annotations = map[string]string{
			"prometheus.io/scrape": "true",
			"prometheus.io/path":   path,
			"prometheus.io/port":   strconv.Itoa(port),
		}
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
		for i := range podTmpl.Spec.InitContainers {
			podTmpl.Spec.InitContainers[i].VolumeMounts = append(podTmpl.Spec.InitContainers[i].VolumeMounts,
				corev1.VolumeMount{Name: volName, MountPath: vm.Path, SubPath: vm.Subpath})
		}
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
				AccessModes: []corev1.PersistentVolumeAccessMode{accessMode(vol)},
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
	// secrets: ride a Secret via secretKeyRef, never inline (issue #1)
	for _, k := range sortedKeys(svc.Secrets) {
		c.Env = append(c.Env, corev1.EnvVar{Name: k, ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretsName(name)}, Key: k,
			},
		}})
	}
	for _, p := range svc.AllPorts() {
		c.Ports = append(c.Ports, corev1.ContainerPort{ContainerPort: int32(p), Name: portName(p)})
	}
	for _, e := range svc.Expose {
		proto := corev1.ProtocolTCP
		if e.Protocol == "udp" {
			proto = corev1.ProtocolUDP
		}
		cp := corev1.ContainerPort{ContainerPort: int32(e.Port), Protocol: proto, Name: "x-" + strconv.Itoa(e.Port)}
		if e.Host != 0 {
			cp.HostPort = int32(e.Host) // conventional TCP port (issue #10)
		}
		c.Ports = append(c.Ports, cp)
	}
	c.SecurityContext = securityContext(svc.Caps, svc.Security)

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
		handler := corev1.ProbeHandler{}
		if len(h.Command) > 0 {
			handler.Exec = &corev1.ExecAction{Command: h.Command}
		} else {
			port := h.Port
			if port == 0 {
				port = svc.MainPort()
			}
			handler.HTTPGet = &corev1.HTTPGetAction{Path: h.Path, Port: intstr.FromInt32(int32(port))}
		}
		// startup owns the boot budget so slow starters are not killed;
		// after it passes, readiness sheds traffic and liveness restarts
		// only the truly wedged (issue #16)
		grace := int32(h.GraceSeconds())
		c.StartupProbe = &corev1.Probe{ProbeHandler: handler, PeriodSeconds: 5, FailureThreshold: (grace + 4) / 5}
		c.ReadinessProbe = &corev1.Probe{ProbeHandler: handler, PeriodSeconds: 10, FailureThreshold: 3}
		c.LivenessProbe = &corev1.Probe{ProbeHandler: handler, PeriodSeconds: 10, FailureThreshold: 3}
	}
	return c, nil
}

// securityContext renders caps and security into a container security
// context; nil when neither is set.
func securityContext(caps []string, sec *dsl.Security) *corev1.SecurityContext {
	if len(caps) == 0 && sec == nil {
		return nil
	}
	sc := &corev1.SecurityContext{}
	if len(caps) > 0 {
		var add []corev1.Capability
		for _, c := range caps {
			add = append(add, corev1.Capability(c))
		}
		sc.Capabilities = &corev1.Capabilities{Add: add}
	}
	if sec != nil {
		sc.RunAsUser = sec.RunAs
		if sec.ReadOnlyRoot {
			t := true
			sc.ReadOnlyRootFilesystem = &t
		}
		if sec.NoNewPrivileges != nil {
			f := !*sec.NoNewPrivileges
			sc.AllowPrivilegeEscalation = &f
		}
		sc.SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
	}
	return sc
}

// secretsName is the per-service Secret carrying secrets: env values.
func secretsName(svc string) string { return svc + "-secrets" }

// filesName is the per-service ConfigMap/Secret pair for files: mounts.
func filesName(svc string, secret bool) string {
	if secret {
		return svc + "-files-secret"
	}
	return svc + "-files"
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
func sidecarPorts(svc dsl.Service) []int {
	var out []int
	for _, scName := range sortedKeys(svc.Sidecars) {
		out = append(out, svc.Sidecars[scName].AllPorts()...)
	}
	return out
}

func service(app *dsl.App, name string, svc dsl.Service) []runtime.Object {
	var objs []runtime.Object
	if ports := append(svc.AllPorts(), sidecarPorts(svc)...); len(ports) > 0 {
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

// redirectObjects compiles http.redirect into an Envoy-native redirect:
// a Contour HTTPProxy with a requestRedirectPolicy, plus a kcert
// cert-request ConfigMap so the domain still gets a certificate. kcert
// names the issued Secret after the ConfigMap, so <name>-tls matches what
// an Ingress-managed cert for this service would have used.
func redirectObjects(app *dsl.App, name string, h *dsl.HTTP) []runtime.Object {
	tls := h.TLS == nil || *h.TLS
	scheme, target := "https", h.Redirect
	if s, rest, ok := strings.Cut(target, "://"); ok {
		scheme, target = s, rest
	}
	target = strings.TrimSuffix(target, "/")

	lbl := map[string]any{}
	for k, v := range labels(app, name) {
		lbl[k] = v
	}
	vhost := map[string]any{"fqdn": h.Domain}
	var objs []runtime.Object
	if tls {
		vhost["tls"] = map[string]any{"secretName": name + "-tls"}
		objs = append(objs, &corev1.ConfigMap{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
			ObjectMeta: metav1.ObjectMeta{
				Name: name + "-tls", Namespace: NamespaceFor(app.Name),
				Labels: mergeLabels(labels(app, name), map[string]string{"kcert.dev/cert-request": "request"}),
			},
			Data: map[string]string{"hosts": h.Domain},
		})
	}
	objs = append(objs, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "projectcontour.io/v1",
		"kind":       "HTTPProxy",
		"metadata": map[string]any{
			"name": name, "namespace": NamespaceFor(app.Name), "labels": lbl,
		},
		"spec": map[string]any{
			"virtualhost": vhost,
			"routes": []any{map[string]any{
				"requestRedirectPolicy": map[string]any{
					"hostname":   target,
					"scheme":     scheme,
					"statusCode": int64(301),
				},
			}},
		},
	}})
	return objs
}

func mergeLabels(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func ptrInt32(v int32) *int32 { return &v }

func accessMode(v dsl.Volume) corev1.PersistentVolumeAccessMode {
	if v.Access == "rwx" {
		return corev1.ReadWriteMany
	}
	return corev1.ReadWriteOnce
}

// splitFiles partitions files: into plain (ConfigMap) and secret content.
func splitFiles(svc dsl.Service) (plain, secret map[string]dsl.FileMount) {
	plain, secret = map[string]dsl.FileMount{}, map[string]dsl.FileMount{}
	for mount, f := range svc.Files {
		if f.From != "" {
			secret[mount] = f
		} else {
			plain[mount] = f
		}
	}
	return
}

// fileKey flattens a mount path into a ConfigMap/Secret key.
func fileKey(mount string) string {
	return strings.Trim(strings.ReplaceAll(mount, "/", "-"), "-")
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
