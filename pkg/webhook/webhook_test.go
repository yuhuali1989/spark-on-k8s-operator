/*
Copyright 2018 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package webhook

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"k8s.io/api/admission/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	spov1beta1 "github.com/GoogleCloudPlatform/spark-on-k8s-operator/pkg/apis/sparkoperator.k8s.io/v1beta1"
	crdclientfake "github.com/GoogleCloudPlatform/spark-on-k8s-operator/pkg/client/clientset/versioned/fake"
	crdinformers "github.com/GoogleCloudPlatform/spark-on-k8s-operator/pkg/client/informers/externalversions"
	"github.com/GoogleCloudPlatform/spark-on-k8s-operator/pkg/config"
)

func TestMutatePod(t *testing.T) {
	crdClient := crdclientfake.NewSimpleClientset()
	informerFactory := crdinformers.NewSharedInformerFactory(crdClient, 0*time.Second)
	informer := informerFactory.Sparkoperator().V1beta1().SparkApplications()
	lister := informer.Lister()

	pod1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spark-driver",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  sparkDriverContainerName,
					Image: "spark-driver:latest",
				},
			},
		},
	}

	// 1. Testing processing non-Spark pod.
	podBytes, err := serializePod(pod1)
	if err != nil {
		t.Error(err)
	}
	review := &v1beta1.AdmissionReview{
		Request: &v1beta1.AdmissionRequest{
			Resource: metav1.GroupVersionResource{
				Group:    corev1.SchemeGroupVersion.Group,
				Version:  corev1.SchemeGroupVersion.Version,
				Resource: "pods",
			},
			Object: runtime.RawExtension{
				Raw: podBytes,
			},
			Namespace: "default",
		},
	}
	response := mutatePods(review, lister, "default")
	assert.True(t, response.Allowed)

	// 2. Test processing Spark pod with only one patch: adding an OwnerReference.
	app1 := &spov1beta1.SparkApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spark-app1",
			Namespace: "default",
		},
	}
	crdClient.SparkoperatorV1beta1().SparkApplications(app1.Namespace).Create(app1)
	informer.Informer().GetIndexer().Add(app1)
	pod1.Labels = map[string]string{
		config.SparkRoleLabel:               config.SparkDriverRole,
		config.LaunchedBySparkOperatorLabel: "true",
		config.SparkAppNameLabel:            app1.Name,
	}
	podBytes, err = serializePod(pod1)
	if err != nil {
		t.Error(err)
	}
	review.Request.Object.Raw = podBytes
	response = mutatePods(review, lister, "default")
	assert.True(t, response.Allowed)
	assert.Equal(t, v1beta1.PatchTypeJSONPatch, *response.PatchType)
	assert.True(t, len(response.Patch) > 0)

	// 3. Test processing Spark pod with patches.
	var user int64 = 1000
	app2 := &spov1beta1.SparkApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spark-app2",
			Namespace: "default",
		},
		Spec: spov1beta1.SparkApplicationSpec{
			Volumes: []corev1.Volume{
				{
					Name: "spark",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/spark",
						},
					},
				},
				{
					Name: "unused", // Expect this to not be added to the driver.
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
			},
			Driver: spov1beta1.DriverSpec{
				SparkPodSpec: spov1beta1.SparkPodSpec{
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "spark",
							MountPath: "/mnt/spark",
						},
					},
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{
									LabelSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{config.SparkRoleLabel: config.SparkDriverRole},
									},
									TopologyKey: "kubernetes.io/hostname",
								},
							},
						},
					},
					Tolerations: []corev1.Toleration{
						{
							Key:      "Key",
							Operator: "Equal",
							Value:    "Value",
							Effect:   "NoEffect",
						},
					},
					SecurityContenxt: &corev1.PodSecurityContext{
						RunAsUser: &user,
					},
				},
			},
		},
	}
	crdClient.SparkoperatorV1beta1().SparkApplications(app2.Namespace).Update(app2)
	informer.Informer().GetIndexer().Add(app2)

	pod1.Labels[config.SparkAppNameLabel] = app2.Name
	podBytes, err = serializePod(pod1)
	if err != nil {
		t.Error(err)
	}
	review.Request.Object.Raw = podBytes
	response = mutatePods(review, lister, "default")
	assert.True(t, response.Allowed)
	assert.Equal(t, v1beta1.PatchTypeJSONPatch, *response.PatchType)
	assert.True(t, len(response.Patch) > 0)
	var patchOps []*patchOperation
	json.Unmarshal(response.Patch, &patchOps)
	assert.Equal(t, 6, len(patchOps))
}

func serializePod(pod *corev1.Pod) ([]byte, error) {
	return json.Marshal(pod)
}
