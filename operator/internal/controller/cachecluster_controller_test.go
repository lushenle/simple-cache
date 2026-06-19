package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	cachev1 "github.com/lushenle/simple-cache/operator/api/v1"
)

var _ = Describe("CacheCluster Controller", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	ctx := context.Background()

	AfterEach(func() {
		// Clean up CacheClusters
		crList := &cachev1.CacheClusterList{}
		Expect(k8sClient.List(ctx, crList)).To(Succeed())
		for _, cr := range crList.Items {
			// Remove finalizers first
			cr.Finalizers = nil
			Expect(k8sClient.Update(ctx, &cr)).To(Succeed())
			Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())
		}
	})

	Describe("Basic Reconciliation", func() {
		It("should create StatefulSet and Services for a valid CacheCluster", func() {
			cr := newCacheCluster("test-basic", "default", 3)
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())

			// Wait for StatefulSet to be created
			stsLookupKey := types.NamespacedName{Name: "test-basic", Namespace: "default"}
			Eventually(func() bool {
				sts := &appsv1.StatefulSet{}
				err := k8sClient.Get(ctx, stsLookupKey, sts)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			// Verify StatefulSet has correct replicas
			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, stsLookupKey, sts)).To(Succeed())
			Expect(*sts.Spec.Replicas).To(Equal(int32(3)))
			Expect(sts.Spec.ServiceName).To(Equal("test-basic"))

			// Verify Headless Service exists
			headlessSvc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, stsLookupKey, headlessSvc)).To(Succeed())
			Expect(headlessSvc.Spec.ClusterIP).To(Equal(corev1.ClusterIPNone))

			// Verify Client Service exists
			clientSvc := &corev1.Service{}
			clientSvcKey := types.NamespacedName{Name: "test-basic-client", Namespace: "default"}
			Expect(k8sClient.Get(ctx, clientSvcKey, clientSvc)).To(Succeed())
		})

		It("should create ConfigMap with correct peers count", func() {
			cr := newCacheCluster("test-configmap", "default", 3)
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())

			cmKey := types.NamespacedName{Name: "test-configmap", Namespace: "default"}
			Eventually(func() bool {
				cm := &corev1.ConfigMap{}
				err := k8sClient.Get(ctx, cmKey, cm)
				return err == nil && cm.Data != nil && cm.Data["init.sh"] != ""
			}, timeout, interval).Should(BeTrue())

			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, cmKey, cm)).To(Succeed())
			Expect(cm.Data["init.sh"]).To(ContainSubstring("REPLICAS=3"))
		})

		It("should update StatefulSet replicas on scale up", func() {
			cr := newCacheCluster("test-scale", "default", 3)
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())

			// Wait for initial creation
			stsKey := types.NamespacedName{Name: "test-scale", Namespace: "default"}
			Eventually(func() bool {
				sts := &appsv1.StatefulSet{}
				return k8sClient.Get(ctx, stsKey, sts) == nil && *sts.Spec.Replicas == 3
			}, timeout, interval).Should(BeTrue())

			// Scale up to 5
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-scale", Namespace: "default"}, cr)).To(Succeed())
			cr.Spec.Replicas = 5
			Expect(k8sClient.Update(ctx, cr)).To(Succeed())

			Eventually(func() int32 {
				sts := &appsv1.StatefulSet{}
				if err := k8sClient.Get(ctx, stsKey, sts); err != nil {
					return 0
				}
				return *sts.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(5)))
		})
	})

	Describe("Validation", func() {
		It("should reject invalid replicas count", func() {
			cr := newCacheCluster("test-invalid", "default", 2)
			err := k8sClient.Create(ctx, cr)
			// The XValidation CEL rule in the CRD will reject this
			// If CRD validation doesn't catch it (in envtest), the controller handles it
			if err == nil {
				// The controller should set a Degraded condition
				Eventually(func() bool {
					updated := &cachev1.CacheCluster{}
					if err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-invalid", Namespace: "default"}, updated); err != nil {
						return false
					}
					for _, c := range updated.Status.Conditions {
						if c.Type == conditionTypeDegraded && c.Status == metav1.ConditionTrue {
							return true
						}
					}
					return false
				}, timeout, interval).Should(BeTrue())
			}
		})
	})

	Describe("ConfigMap content", func() {
		It("should generate single-mode config for replicas=1", func() {
			cr := newCacheCluster("test-single", "default", 1)
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())

			cmKey := types.NamespacedName{Name: "test-single", Namespace: "default"}
			Eventually(func() bool {
				cm := &corev1.ConfigMap{}
				err := k8sClient.Get(ctx, cmKey, cm)
				return err == nil && cm.Data != nil && cm.Data["init.sh"] != ""
			}, timeout, interval).Should(BeTrue())

			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, cmKey, cm)).To(Succeed())
			Expect(cm.Data["init.sh"]).To(ContainSubstring("mode: single"))
			Expect(cm.Data["init.sh"]).To(ContainSubstring("peers: []"))
		})

		It("should generate distributed-mode config for replicas=3", func() {
			cr := newCacheCluster("test-dist", "default", 3)
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())

			cmKey := types.NamespacedName{Name: "test-dist", Namespace: "default"}
			Eventually(func() bool {
				cm := &corev1.ConfigMap{}
				err := k8sClient.Get(ctx, cmKey, cm)
				return err == nil && cm.Data != nil && cm.Data["init.sh"] != ""
			}, timeout, interval).Should(BeTrue())

			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, cmKey, cm)).To(Succeed())
			Expect(cm.Data["init.sh"]).To(ContainSubstring("mode: distributed"))
			Expect(cm.Data["init.sh"]).To(ContainSubstring("for i in $(seq 0 $((REPLICAS-1))"))
		})
	})

	Describe("Secret validation", func() {
		It("should not create child resources when referenced secret is missing", func() {
			cr := newCacheCluster("test-secret", "default", 3)
			cr.Spec.Auth.Enabled = true
			cr.Spec.Auth.TokenSecretRef.Name = "nonexistent-secret"
			err := k8sClient.Create(ctx, cr)
			if err != nil {
				// CRD CEL validation may reject; test still valid
				return
			}
			// The controller's validateSpec should fail on the missing secret,
			// preventing child resource creation. Verify no StatefulSet is created.
			Consistently(func() error {
				sts := &appsv1.StatefulSet{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-secret", Namespace: "default"}, sts)
			}, 5*time.Second, interval).ShouldNot(Succeed())
		})
	})

	Describe("Deletion", func() {
		It("should remove finalizer on deletion", func() {
			cr := newCacheCluster("test-delete", "default", 3)
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())

			// Wait for finalizer to be added
			Eventually(func() bool {
				updated := &cachev1.CacheCluster{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-delete", Namespace: "default"}, updated); err != nil {
					return false
				}
				return len(updated.Finalizers) > 0
			}, timeout, interval).Should(BeTrue())

			// Delete the CR
			Expect(k8sClient.Delete(ctx, cr)).To(Succeed())

			// CR should be fully removed after finalizer cleanup
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-delete", Namespace: "default"}, &cachev1.CacheCluster{})
				return apierrors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue())
		})
	})
})

func newCacheCluster(name, namespace string, replicas int32) *cachev1.CacheCluster {
	return &cachev1.CacheCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: cachev1.CacheClusterSpec{
			Replicas: replicas,
			Image:    "registry.shenle.lu/ishenle/simple-cache:latest",
			Cache: cachev1.CacheConfig{
				MaxKeys:        0,
				EvictionPolicy: "none",
			},
			Raft: cachev1.RaftConfig{
				HeartbeatMs:       200,
				ElectionMs:        5000,
				SnapshotEnabled:   true,
				SnapshotThreshold: 1024,
			},
			Persistence: cachev1.PersistenceConfig{
				DumpOnShutdown: true,
				LoadOnStartup:  false,
				DumpFormat:     "binary",
				DataDir:        "/data",
			},
			Storage: cachev1.StorageConfig{
				Size: "10Gi",
			},
			TerminationGracePeriodSeconds: ptr.To(int64(60)),
		},
	}
}
