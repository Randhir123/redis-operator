/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	sha256 "crypto/sha256"
	json "encoding/json"
	fmt "fmt"
	time "time"

	logr "github.com/go-logr/logr"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cachev1alpha1 "github.com/Randhir123/redis-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	errors "k8s.io/apimachinery/pkg/api/errors"
	resource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	intstr "k8s.io/apimachinery/pkg/util/intstr"
)

// RedisReconciler reconciles a Redis object
type RedisReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

var hashStore = make(map[string]string)
var cfgName = "redis-conf"

func asSha256(o interface{}) string {
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%v", o)))

	return fmt.Sprintf("%x", h.Sum(nil))
}

func getLabelsMap(name string, labels map[string]string) map[string]string {
	if labels == nil {
		return map[string]string{"app": "rediscluster", "rediscluster_cr": name}
	} else {
		labels["app"] = "rediscluster"
		labels["rediscluster_cr"] = name
		return labels
	}
}

func buildVolumes(redisCluster *cachev1alpha1.Redis) []corev1.Volume {
	volumes := []corev1.Volume{}
	volumes = append(volumes, corev1.Volume{
		Name: redisCluster.Spec.Deployment.Name,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: cfgName,
				},
			},
		},
	})

	return volumes
}

func buildVolumeMounts(redisCluster *cachev1alpha1.Redis) []corev1.VolumeMount {
	volumeMounts := []corev1.VolumeMount{}
	volumeMounts = append(volumeMounts, corev1.VolumeMount{
		Name:      redisCluster.Spec.Deployment.Name,
		ReadOnly:  true,
		MountPath: "/etc/redis",
	})

	volumeMounts = append(volumeMounts, corev1.VolumeMount{
		Name:      "data",
		MountPath: "/grid",
	})

	return volumeMounts
}

func buildVolumeClaims(redisCluster *cachev1alpha1.Redis) []corev1.PersistentVolumeClaim {
	volumeClaims := []corev1.PersistentVolumeClaim{}

	volumeClaim := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "data",
			Namespace: redisCluster.Namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(redisCluster.Spec.Deployment.StorageSize),
				},
			},
			StorageClassName: &redisCluster.Spec.Deployment.StorageClassName,
		},
	}

	volumeClaims = append(volumeClaims, volumeClaim)

	return volumeClaims
}

//+kubebuilder:rbac:groups=cache.kvstores.com,resources=redis,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=cache.kvstores.com,resources=redis/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=cache.kvstores.com,resources=redis/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Redis object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.17.3/pkg/reconcile
func (r *RedisReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("redis", req.NamespacedName).WithValues("requestid", time.Now().Unix())
	redisCluster := &cachev1alpha1.Redis{}
	err := r.Get(ctx, req.NamespacedName, redisCluster)

	log.Info("Received request to reconcile")

	result, err := buildConfigMap(redisCluster, redisCluster.Namespace, ctx, r.Client, r.Scheme, log)
	if err != nil {
		return result, err
	}

	result, err = buildService(redisCluster, ctx, r.Client, r.Scheme, log)
	if err != nil {
		return result, err
	}

	result, err = buildStatefulset(redisCluster, redisCluster.Namespace, ctx, r.Client, r.Scheme, log)
	if err != nil {
		return result, err
	}

	return ctrl.Result{}, err
}

// SetupWithManager sets up the controller with the Manager.
func (r *RedisReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cachev1alpha1.Redis{}).
		Complete(r)
}

func buildConfigMap(redisCluster *cachev1alpha1.Redis, namespace string, ctx context.Context, cl client.Client, s *runtime.Scheme, log logr.Logger) (ctrl.Result, error) {
	configuration := redisCluster.Spec.Configuration

	cfg := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfgName,
			Namespace: namespace,
		},
		Data: configuration,
	}

	ctrl.SetControllerReference(redisCluster, cfg, s)

	config := &corev1.ConfigMap{}
	err := cl.Get(ctx, types.NamespacedName{Name: cfg.Name, Namespace: namespace}, config)

	if err != nil {
		if errors.IsNotFound(err) {
			// Define a new ConfigMap
			log.Info("Creating a new ConfigMap", "ConfigMap.Namespace", cfg.Namespace, "ConfigMap.Name", cfg.Name)
			err = cl.Create(ctx, cfg)
			if err != nil {
				log.Error(err, "Failed to create new ConfigMap", "ConfigMap.Namespace", cfg.Namespace, "ConfigMap.Name", cfg.Name)
				return ctrl.Result{RequeueAfter: time.Second * 5}, err
			}
			log.Info("Created a new ConfigMap", "ConfigMap.Namespace", cfg.Namespace, "ConfigMap.Name", cfg.Name)
			return ctrl.Result{}, nil
		}

		log.Error(err, "Failed to get ConfigMaps", "ConfigMap.Namespace", namespace)
		return ctrl.Result{RequeueAfter: time.Second * 5}, err
	}

	return ctrl.Result{}, err
}

func buildService(redisCluster *cachev1alpha1.Redis, ctx context.Context, cl client.Client, s *runtime.Scheme, log logr.Logger) (ctrl.Result, error) {
	ports := []corev1.ServicePort{}

	ports = append(ports, corev1.ServicePort{
		Name:       redisCluster.Spec.ServiceName,
		Port:       redisCluster.Spec.ContainerPort,
		TargetPort: intstr.FromInt(int(redisCluster.Spec.ContainerPort)),
		Protocol:   corev1.ProtocolTCP,
	})

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      redisCluster.Spec.ServiceName,
			Namespace: redisCluster.Namespace,
			Labels:    getLabelsMap(redisCluster.Spec.ServiceName, nil),
		},
		Spec: corev1.ServiceSpec{
			Type:                     corev1.ServiceTypeClusterIP,
			ClusterIP:                "None",
			PublishNotReadyAddresses: true,
			Selector:                 getLabelsMap(redisCluster.Spec.ServiceName, nil),
			Ports:                    ports,
		},
	}

	ctrl.SetControllerReference(redisCluster, svc, s)

	service := &corev1.Service{}
	err := cl.Get(ctx, types.NamespacedName{Name: svc.Name, Namespace: redisCluster.Namespace}, service)
	if err != nil {
		if errors.IsNotFound(err) {
			// Define a new Service
			log.Info("Creating a new Service", "Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
			err = cl.Create(ctx, svc)
			if err != nil {
				log.Error(err, "Failed to create new Service", "Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
				return ctrl.Result{RequeueAfter: time.Second * 5}, err
			}
			log.Info("Created a new Service", "Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
			return ctrl.Result{}, nil
		}

		log.Error(err, "Failed to get Services", "Service.Namespace", redisCluster.Namespace)
		return ctrl.Result{RequeueAfter: time.Second * 5}, err
	}

	return ctrl.Result{}, err
}

func buildStatefulset(redisCluster *cachev1alpha1.Redis, namespace string, ctx context.Context, cl client.Client, s *runtime.Scheme, log logr.Logger) (ctrl.Result, error) {
	d := redisCluster.Spec.Deployment
	labels := getLabelsMap(d.Name, nil)

	container := corev1.Container{
		Image:        d.Image,
		Name:         d.Name,
		Command:      d.Command,
		Args:         d.Args,
		VolumeMounts: buildVolumeMounts(redisCluster),
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(d.CpuLimit),
				corev1.ResourceMemory: resource.MustParse(d.MemoryLimit),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(d.CpuRequest),
				corev1.ResourceMemory: resource.MustParse(d.MemoryRequest),
			},
		},
	}

	containers := []corev1.Container{}
	containers = append(containers, container)

	newSS := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      d.Name,
			Namespace: namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &d.Size,
			ServiceName: redisCluster.Spec.ServiceName,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			VolumeClaimTemplates: buildVolumeClaims(redisCluster),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},

				Spec: corev1.PodSpec{
					//TerminationGracePeriodSeconds: 30,
					Volumes:    buildVolumes(redisCluster),
					Containers: containers,
				},
			},
		},
	}

	ctrl.SetControllerReference(redisCluster, newSS, s)

	newSSMarshal, _ := json.Marshal(newSS)
	existingSS := &appsv1.StatefulSet{}
	err := cl.Get(ctx, types.NamespacedName{Name: d.Name, Namespace: namespace}, existingSS)

	if err != nil {
		if errors.IsNotFound(err) {
			// Define statefulset
			log.Info("Creating a new StatefulSet", "StatefulSet.Namespace", newSS.Namespace, "StatefulSet.Name", newSS.Name)
			err = cl.Create(ctx, newSS)
			if err != nil {
				log.Error(err, "Failed to create new StatefulSet", "StatefulSet.Namespace", newSS.Namespace, "StatefulSet.Name", newSS.Name)
				return ctrl.Result{RequeueAfter: time.Second * 5}, err
			}
			log.Info("Created a new StatefulSet", "StatefulSet.Namespace", newSS.Namespace, "StatefulSet.Name", newSS.Name)
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 5}, nil
		}

		log.Error(err, "Failed to get StatefulSet", "Service.Namespace", namespace)
		return ctrl.Result{RequeueAfter: time.Second * 5}, err
	} else if asSha256(newSSMarshal) != hashStore["ss-"+newSS.Name] {
		log.Info("Updating StatefulSet", "StatefulSet.Namespace", newSS.Namespace, "StatefulSet.Name", newSS.Name)
		err = cl.Update(ctx, newSS)
		if err != nil {
			log.Error(err, "Failed to update StatefulSet", "StatefulSet.Namespace", newSS.Namespace, "StatefulSet.Name", newSS.Name)
			return ctrl.Result{RequeueAfter: time.Second * 5}, err
		}
		hashStore["ss-"+newSS.Name] = asSha256(newSSMarshal)
		log.Info("Updated StatefulSet", "StatefulSet.Namespace", newSS.Namespace, "StatefulSet.Name", newSS.Name)
		return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 20}, nil
	} else if existingSS.Status.ReadyReplicas != d.Size || existingSS.Status.CurrentRevision != existingSS.Status.UpdateRevision {
		log.Info("Sleeping for 20 seconds. Cluster", "NotReady", existingSS.Status, "Expected Replicas", d.Size)
		return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 20}, nil
	} else {
		log.Info("Reconciled for cluster", "StatefulSet", d.Name)
	}

	return ctrl.Result{}, nil
}
