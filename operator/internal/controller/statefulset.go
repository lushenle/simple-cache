package controller

import (
	"fmt"

	cachev1 "github.com/lushenle/simple-cache/operator/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func buildStatefulSet(cr *cachev1.CacheCluster) (*appsv1.StatefulSet, error) {
	lbls := labels(cr)
	replicas := cr.Spec.Replicas

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      statefulSetName(cr),
			Namespace: cr.Namespace,
			Labels:    lbls,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &replicas,
			ServiceName: headlessServiceName(cr),
			Selector: &metav1.LabelSelector{
				MatchLabels: lbls,
			},
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{
					Partition: ptr.To(int32(0)),
				},
			},
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: lbls,
				},
				Spec: buildPodSpec(cr),
			},
		},
	}

	// VolumeClaimTemplate for persistent data storage
	if cr.Spec.Storage.Size != "" {
		storageSize, err := resource.ParseQuantity(cr.Spec.Storage.Size)
		if err != nil {
			return nil, fmt.Errorf("invalid storage size %q: %w", cr.Spec.Storage.Size, err)
		}
		pvc := corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name: "data",
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: storageSize,
					},
				},
			},
		}
		if cr.Spec.Storage.StorageClassName != "" {
			pvc.Spec.StorageClassName = &cr.Spec.Storage.StorageClassName
		}
		sts.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{pvc}
	}

	return sts, nil
}

func buildPodSpec(cr *cachev1.CacheCluster) corev1.PodSpec {
	terminationGracePeriod := defaultTerminationGracePeriod
	if cr.Spec.TerminationGracePeriodSeconds != nil {
		terminationGracePeriod = *cr.Spec.TerminationGracePeriodSeconds
	}

	env := buildEnvVars(cr)
	volumes := buildVolumes(cr)
	volumeMounts := buildVolumeMounts(cr)
	container := buildContainer(cr, env, volumeMounts)

	podSpec := corev1.PodSpec{
		Containers:                    []corev1.Container{container},
		Volumes:                       volumes,
		TerminationGracePeriodSeconds: &terminationGracePeriod,
	}

	if cr.Spec.Affinity != nil {
		podSpec.Affinity = cr.Spec.Affinity
	} else {
		// Default: prefer spreading pods across nodes to protect Raft quorum.
		podSpec.Affinity = &corev1.Affinity{
			PodAntiAffinity: &corev1.PodAntiAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
					Weight: 100,
					PodAffinityTerm: corev1.PodAffinityTerm{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: labels(cr),
						},
						TopologyKey: "kubernetes.io/hostname",
					},
				}},
			},
		}
	}
	if len(cr.Spec.Tolerations) > 0 {
		podSpec.Tolerations = cr.Spec.Tolerations
	}
	if len(cr.Spec.NodeSelector) > 0 {
		podSpec.NodeSelector = cr.Spec.NodeSelector
	}
	if len(cr.Spec.TopologySpreadConstraints) > 0 {
		podSpec.TopologySpreadConstraints = cr.Spec.TopologySpreadConstraints
	}

	return podSpec
}

func buildContainer(cr *cachev1.CacheCluster, env []corev1.EnvVar, volumeMounts []corev1.VolumeMount) corev1.Container {
	image := cr.Spec.Image
	if image == "" {
		image = defaultImage
	}
	pullPolicy := cr.Spec.ImagePullPolicy
	if pullPolicy == "" {
		pullPolicy = corev1.PullIfNotPresent
	}

	container := corev1.Container{
		Name:            "simple-cache",
		Image:           image,
		ImagePullPolicy: pullPolicy,
		Command:         []string{"/bin/sh"},
		Args:            []string{configMountPath + "/" + configFileName},
		Env:             env,
		Ports: []corev1.ContainerPort{
			{Name: "grpc", ContainerPort: grpcPort},
			{Name: "http", ContainerPort: httpPort},
			{Name: "raft", ContainerPort: raftPort},
			{Name: "metrics", ContainerPort: metricsPort},
		},
		VolumeMounts: volumeMounts,
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot:             ptr.To(true),
			RunAsUser:                ptr.To(defaultRunAsUser),
			AllowPrivilegeEscalation: ptr.To(false),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
			ReadOnlyRootFilesystem: ptr.To(true),
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/healthz",
					Port: intstrPtr(httpPort),
				},
			},
			InitialDelaySeconds: livenessInitialDelay,
			PeriodSeconds:       livenessPeriod,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/readyz",
					Port: intstrPtr(httpPort),
				},
			},
			InitialDelaySeconds: readinessInitialDelay,
			PeriodSeconds:       readinessPeriod,
		},
		Resources: containerResources(cr.Spec.Resources),
	}

	// Mount data volume if storage is configured
	if cr.Spec.Storage.Size != "" {
		dataDir := cr.Spec.Persistence.DataDir
		if dataDir == "" {
			dataDir = defaultDataDir
		}
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      "data",
			MountPath: dataDir,
		})
	}

	return container
}

func buildEnvVars(cr *cachev1.CacheCluster) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{
			Name: "SIMPLE_CACHE_NODE_ID",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.name",
				},
			},
		},
		{
			Name:  "SIMPLE_CACHE_MODE",
			Value: "distributed",
		},
		{
			Name:  "CONFIG_PATH",
			Value: configFilePath,
		},
	}

	// Auth token
	if cr.Spec.Auth.Enabled {
		tokenRef := cr.Spec.Auth.TokenSecretRef
		if tokenRef.Name != "" {
			env = append(env, corev1.EnvVar{
				Name: "SIMPLE_CACHE_AUTH_TOKEN",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: tokenRef.Name},
						Key:                  tokenRef.Key,
					},
				},
			})
		}
	}

	// TLS
	if cr.Spec.TLS.Enabled {
		env = append(env,
			corev1.EnvVar{Name: "SIMPLE_CACHE_ENABLE_TLS", Value: "true"},
			corev1.EnvVar{
				Name:  "SIMPLE_CACHE_TLS_CERT_FILE",
				Value: tlsCertsMountPath + "/" + cr.Spec.TLS.CertKey,
			},
			corev1.EnvVar{
				Name:  "SIMPLE_CACHE_TLS_KEY_FILE",
				Value: tlsCertsMountPath + "/" + cr.Spec.TLS.KeyKey,
			},
		)
	}

	// Extra env vars
	env = append(env, cr.Spec.ExtraEnv...)

	return env
}

func buildVolumes(cr *cachev1.CacheCluster) []corev1.Volume {
	mode := configMapFileMode
	volumes := []corev1.Volume{
		{
			Name: "config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: configMapName(cr),
					},
					DefaultMode: &mode,
					Items: []corev1.KeyToPath{
						{
							Key:  "init.sh",
							Path: configFileName,
						},
					},
				},
			},
		},
	}

	// TLS cert volume
	if cr.Spec.TLS.Enabled && cr.Spec.TLS.CertSecretRef.Name != "" {
		volumes = append(volumes, corev1.Volume{
			Name: "tls-certs",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: cr.Spec.TLS.CertSecretRef.Name,
				},
			},
		})
	}

	return volumes
}

func buildVolumeMounts(cr *cachev1.CacheCluster) []corev1.VolumeMount {
	mounts := []corev1.VolumeMount{
		{
			Name:      "config",
			MountPath: configMountPath,
		},
	}

	if cr.Spec.TLS.Enabled {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      "tls-certs",
			MountPath: tlsCertsMountPath,
			ReadOnly:  true,
		})
	}

	return mounts
}

// containerResources applies sensible defaults when the CR omits resource reqs/limits.
func containerResources(spec corev1.ResourceRequirements) corev1.ResourceRequirements {
	if spec.Requests == nil && spec.Limits == nil {
		spec.Requests = corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("200m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		}
		spec.Limits = corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		}
	}
	return spec
}
