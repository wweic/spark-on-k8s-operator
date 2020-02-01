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
	"fmt"
	"strings"

	"github.com/golang/glog"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/GoogleCloudPlatform/spark-on-k8s-operator/pkg/apis/sparkoperator.k8s.io/v1beta2"
	"github.com/GoogleCloudPlatform/spark-on-k8s-operator/pkg/config"
	"github.com/GoogleCloudPlatform/spark-on-k8s-operator/pkg/util"
)

const (
	maxNameLength = 63
)

// patchOperation represents a RFC6902 JSON patch operation.
type patchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

func patchSparkPod(pod *corev1.Pod, app *v1beta2.SparkApplication) []patchOperation {
	var patchOps []patchOperation

	if util.IsDriverPod(pod) {
		patchOps = append(patchOps, addOwnerReference(pod, app))
	}

	glog.V(2).Infof("Pod information %v", pod)

	patchOps = append(patchOps, addVolumes(pod, app)...)
	patchOps = append(patchOps, addGeneralConfigMaps(pod, app)...)
	patchOps = append(patchOps, addSparkConfigMap(pod, app)...)
	patchOps = append(patchOps, addHadoopConfigMap(pod, app)...)
	patchOps = append(patchOps, addPrometheusConfigMap(pod, app)...)
	patchOps = append(patchOps, addTolerations(pod, app)...)
	patchOps = append(patchOps, addSidecarContainers(pod, app)...)
	patchOps = append(patchOps, addInitContainers(pod, app)...)
	patchOps = append(patchOps, addHostNetwork(pod, app)...)
	patchOps = append(patchOps, addNodeSelectors(pod, app)...)
	patchOps = append(patchOps, addDNSConfig(pod, app)...)
	patchOps = append(patchOps, addEnvVars(pod, app)...)

	op := addSchedulerName(pod, app)
	if op != nil {
		patchOps = append(patchOps, *op)
	}

	if pod.Spec.Affinity == nil {
		op := addAffinity(pod, app)
		if op != nil {
			patchOps = append(patchOps, *op)
		}
	}

	op = addSecurityContext(pod, app)
	if op != nil {
		patchOps = append(patchOps, *op)
	}

	op = addGPU(pod, app)
	if op != nil {
		patchOps = append(patchOps, *op)
	}
	return patchOps
}

func addOwnerReference(pod *corev1.Pod, app *v1beta2.SparkApplication) patchOperation {
	ownerReference := util.GetOwnerReference(app)

	path := "/metadata/ownerReferences"
	var value interface{}
	if len(pod.OwnerReferences) == 0 {
		value = []metav1.OwnerReference{ownerReference}
	} else {
		path += "/-"
		value = ownerReference
	}

	return patchOperation{Op: "add", Path: path, Value: value}
}

func addVolumes(pod *corev1.Pod, app *v1beta2.SparkApplication) []patchOperation {
	volumes := app.Spec.Volumes

	volumeMap := make(map[string]corev1.Volume)
	for _, v := range volumes {
		volumeMap[v.Name] = v
	}

	var volumeMounts []corev1.VolumeMount
	if util.IsDriverPod(pod) {
		volumeMounts = app.Spec.Driver.VolumeMounts
	} else if util.IsExecutorPod(pod) {
		volumeMounts = app.Spec.Executor.VolumeMounts
	}

	var ops []patchOperation
	addedVolumeMap := make(map[string]corev1.Volume)
	for _, m := range volumeMounts {
		// Skip adding localDirVolumes
		if strings.HasPrefix(m.Name, config.SparkLocalDirVolumePrefix) {
			continue
		}

		if v, ok := volumeMap[m.Name]; ok {
			if _, ok := addedVolumeMap[m.Name]; !ok {
				ops = append(ops, addVolume(pod, v))
				addedVolumeMap[m.Name] = v
			}
			ops = append(ops, addVolumeMount(pod, m))
		}
	}

	return ops
}

func addVolume(pod *corev1.Pod, volume corev1.Volume) patchOperation {
	path := "/spec/volumes"
	var value interface{}
	if len(pod.Spec.Volumes) == 0 {
		value = []corev1.Volume{volume}
	} else {
		path += "/-"
		value = volume
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, volume)

	return patchOperation{Op: "add", Path: path, Value: value}
}

func findMainContainerIndex(pod *corev1.Pod) int {
	i := 0
	// Find the driver or executor container in the pod.
	for ; i < len(pod.Spec.Containers); i++ {
		if pod.Spec.Containers[i].Name == config.SparkDriverContainerName ||
			pod.Spec.Containers[i].Name == config.SparkExecutorContainerName {
			break
		}
	}
	return i
}

func findVolumeMountIndex(container *corev1.Container) int {
	i := 0
	// Find the driver or executor container in the pod.
	for ; i < len(container.VolumeMounts); i++ {
		glog.V(2).Infof("Processing mount %v(name %v)", container.VolumeMounts[i], container.VolumeMounts[i].Name)
		if container.VolumeMounts[i].Name == "spark-conf-volume" {
			return i
		}
	}
	return -1
}

func addVolumeMount(pod *corev1.Pod, mount corev1.VolumeMount) patchOperation {
	i := findMainContainerIndex(pod)

	path := fmt.Sprintf("/spec/containers/%d/volumeMounts", i)
	var value interface{}
	if len(pod.Spec.Containers[i].VolumeMounts) == 0 {
		value = []corev1.VolumeMount{mount}
	} else {
		path += "/-"
		value = mount
	}
	pod.Spec.Containers[i].VolumeMounts = append(pod.Spec.Containers[i].VolumeMounts, mount)

	return patchOperation{Op: "add", Path: path, Value: value}
}

func addEnvVars(pod *corev1.Pod, app *v1beta2.SparkApplication) []patchOperation {
	var envVars []corev1.EnvVar
	var containerName string
	if util.IsDriverPod(pod) {
		envVars = app.Spec.Driver.Env
		containerName = config.SparkDriverContainerName
	} else if util.IsExecutorPod(pod) {
		envVars = app.Spec.Executor.Env
		containerName = config.SparkExecutorContainerName
	}

	i := 0
	// Find the driver or executor container in the pod.
	for ; i < len(pod.Spec.Containers); i++ {
		if pod.Spec.Containers[i].Name == containerName {
			break
		}
	}
	basePath := fmt.Sprintf("/spec/containers/%d/env", i)

	var value interface{}
	var patchOps []patchOperation

	first := false
	if len(pod.Spec.Containers[i].Env) == 0 {
		first = true
	}

	for _, envVar := range envVars {
		path := basePath
		if first {
			value = []corev1.EnvVar{envVar}
			first = false
		} else {
			path += "/-"
			value = envVar
		}
		patchOps = append(patchOps, patchOperation{Op: "add", Path: path, Value: value})
	}
	return patchOps
}

func addEnvironmentVariable(pod *corev1.Pod, envName, envValue string) patchOperation {
	i := 0
	// Find the driver or executor container in the pod.
	for ; i < len(pod.Spec.Containers); i++ {
		if pod.Spec.Containers[i].Name == config.SparkDriverContainerName ||
			pod.Spec.Containers[i].Name == config.SparkExecutorContainerName {
			break
		}
	}

	path := fmt.Sprintf("/spec/containers/%d/env", i)
	var value interface{}
	if len(pod.Spec.Containers[i].Env) == 0 {
		value = []corev1.EnvVar{{Name: envName, Value: envValue}}
	} else {
		path += "/-"
		value = corev1.EnvVar{Name: envName, Value: envValue}
	}

	return patchOperation{Op: "add", Path: path, Value: value}
}

func addSparkConfigMap(pod *corev1.Pod, app *v1beta2.SparkApplication) []patchOperation {
	var patchOps []patchOperation
	sparkConfigMapName := app.Spec.SparkConfigMap
	if sparkConfigMapName != nil {
		// TODO: Patch existing spark configmap to use subpath.
		i := findMainContainerIndex(pod)
		glog.V(2).Infof("Existing spark configmaps %v", pod.Spec.Containers[i].VolumeMounts)
		mount_index := findVolumeMountIndex(&pod.Spec.Containers[i])

		if mount_index >= 0 {
			glog.V(2).Infof("Replacing VolumeMount in %v", mount_index)

			mnt := pod.Spec.Containers[i].VolumeMounts[mount_index]
			properties_file := "spark.properties"
			mnt.MountPath = mnt.MountPath + "/" + properties_file
			mnt.SubPath = properties_file

			replace_mount_path := fmt.Sprintf("/spec/containers/%d/volumeMounts/%d", i, mount_index)

			glog.V(2).Infof("New VolumeMount %v", replace_mount_path)

			patchOps = append(patchOps, patchOperation{Op: "replace", Path: replace_mount_path, Value: mnt})
		}

		patchOps = append(patchOps, addConfigMapVolume(pod, *sparkConfigMapName, config.SparkConfigMapVolumeName))
		// TODO: Use subpath from user provided options. Might be a loop since there could be multiple files in configmap
		patchOps = append(patchOps, addConfigMapVolumeMountSubpath(pod, config.SparkConfigMapVolumeName,
			"/opt/spark/conf/hive-site.xml", "hive-site.xml"))
		patchOps = append(patchOps, addEnvironmentVariable(pod, config.SparkConfDirEnvVar, config.DefaultSparkConfDir))
	}
	return patchOps
}

func addHadoopConfigMap(pod *corev1.Pod, app *v1beta2.SparkApplication) []patchOperation {
	var patchOps []patchOperation
	hadoopConfigMapName := app.Spec.HadoopConfigMap
	if hadoopConfigMapName != nil {
		patchOps = append(patchOps, addConfigMapVolume(pod, *hadoopConfigMapName, config.HadoopConfigMapVolumeName))
		patchOps = append(patchOps, addConfigMapVolumeMount(pod, config.HadoopConfigMapVolumeName,
			config.DefaultHadoopConfDir))
		patchOps = append(patchOps, addEnvironmentVariable(pod, config.HadoopConfDirEnvVar, config.DefaultHadoopConfDir))
	}
	return patchOps
}

func addGeneralConfigMaps(pod *corev1.Pod, app *v1beta2.SparkApplication) []patchOperation {
	var configMaps []v1beta2.NamePath
	if util.IsDriverPod(pod) {
		configMaps = app.Spec.Driver.ConfigMaps
	} else if util.IsExecutorPod(pod) {
		configMaps = app.Spec.Executor.ConfigMaps
	}

	var patchOps []patchOperation
	for _, namePath := range configMaps {
		volumeName := namePath.Name + "-vol"
		if len(volumeName) > maxNameLength {
			volumeName = volumeName[0:maxNameLength]
			glog.V(2).Infof("ConfigMap volume name is too long. Truncating to length %d. Result: %s.", maxNameLength, volumeName)
		}
		patchOps = append(patchOps, addConfigMapVolume(pod, namePath.Name, volumeName))
		patchOps = append(patchOps, addConfigMapVolumeMount(pod, volumeName, namePath.Path))
	}
	return patchOps
}

func addPrometheusConfigMap(pod *corev1.Pod, app *v1beta2.SparkApplication) []patchOperation {
	// Skip if Prometheus Monitoring is not enabled or an in-container ConfigFile is used,
	// in which cases a Prometheus ConfigMap won't be created.
	if !app.PrometheusMonitoringEnabled() || app.HasPrometheusConfigFile() {
		return nil
	}

	if util.IsDriverPod(pod) && !app.ExposeDriverMetrics() {
		return nil
	}
	if util.IsExecutorPod(pod) && !app.ExposeExecutorMetrics() {
		return nil
	}

	var patchOps []patchOperation
	name := config.GetPrometheusConfigMapName(app)
	volumeName := name + "-vol"
	mountPath := config.PrometheusConfigMapMountPath
	patchOps = append(patchOps, addConfigMapVolume(pod, name, volumeName))
	patchOps = append(patchOps, addConfigMapVolumeMount(pod, volumeName, mountPath))
	return patchOps
}

func addConfigMapVolume(pod *corev1.Pod, configMapName string, configMapVolumeName string) patchOperation {
	volume := corev1.Volume{
		Name: configMapVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: configMapName,
				},
			},
		},
	}
	return addVolume(pod, volume)
}

func addConfigMapVolumeMount(pod *corev1.Pod, configMapVolumeName string, mountPath string) patchOperation {
	mount := corev1.VolumeMount{
		Name:      configMapVolumeName,
		ReadOnly:  true,
		MountPath: mountPath,
	}
	return addVolumeMount(pod, mount)
}

func addConfigMapVolumeMountSubpath(pod *corev1.Pod, configMapVolumeName string, mountPath string, subpath string) patchOperation {
	mount := corev1.VolumeMount{
		Name:      configMapVolumeName,
		ReadOnly:  true,
		MountPath: mountPath,
		SubPath:   subpath,
	}
	return addVolumeMount(pod, mount)
}

func addAffinity(pod *corev1.Pod, app *v1beta2.SparkApplication) *patchOperation {
	var affinity *corev1.Affinity
	if util.IsDriverPod(pod) {
		affinity = app.Spec.Driver.Affinity
	} else if util.IsExecutorPod(pod) {
		affinity = app.Spec.Executor.Affinity
	}

	if affinity == nil {
		return nil
	}
	return &patchOperation{Op: "add", Path: "/spec/affinity", Value: *affinity}
}

func addTolerations(pod *corev1.Pod, app *v1beta2.SparkApplication) []patchOperation {
	var tolerations []corev1.Toleration
	if util.IsDriverPod(pod) {
		tolerations = app.Spec.Driver.Tolerations
	} else if util.IsExecutorPod(pod) {
		tolerations = app.Spec.Executor.Tolerations
	}

	first := false
	if len(pod.Spec.Tolerations) == 0 {
		first = true
	}

	var ops []patchOperation
	for _, v := range tolerations {
		ops = append(ops, addToleration(pod, v, first))
		if first {
			first = false
		}
	}
	return ops
}

func addNodeSelectors(pod *corev1.Pod, app *v1beta2.SparkApplication) []patchOperation {
	var nodeSelector map[string]string
	if util.IsDriverPod(pod) {
		nodeSelector = app.Spec.Driver.NodeSelector
	} else if util.IsExecutorPod(pod) {
		nodeSelector = app.Spec.Executor.NodeSelector
	}

	var ops []patchOperation
	if len(nodeSelector) > 0 {
		ops = append(ops, patchOperation{Op: "add", Path: "/spec/nodeSelector", Value: nodeSelector})
	}
	return ops
}

func addDNSConfig(pod *corev1.Pod, app *v1beta2.SparkApplication) []patchOperation {
	var dnsConfig *corev1.PodDNSConfig

	if util.IsDriverPod(pod) {
		dnsConfig = app.Spec.Driver.DNSConfig
	} else if util.IsExecutorPod(pod) {
		dnsConfig = app.Spec.Executor.DNSConfig
	}

	var ops []patchOperation
	if dnsConfig != nil {
		ops = append(ops, patchOperation{Op: "add", Path: "/spec/dnsConfig", Value: dnsConfig})
	}
	return ops
}

func addSchedulerName(pod *corev1.Pod, app *v1beta2.SparkApplication) *patchOperation {
	var schedulerName *string

	//NOTE: Preferred to use `BatchScheduler` if application spec has it configured.
	if app.Spec.BatchScheduler != nil {
		schedulerName = app.Spec.BatchScheduler
	} else if util.IsDriverPod(pod) {
		schedulerName = app.Spec.Driver.SchedulerName
	} else if util.IsExecutorPod(pod) {
		schedulerName = app.Spec.Executor.SchedulerName
	}
	if schedulerName == nil || *schedulerName == "" {
		return nil
	}
	return &patchOperation{Op: "add", Path: "/spec/schedulerName", Value: *schedulerName}
}

func addToleration(pod *corev1.Pod, toleration corev1.Toleration, first bool) patchOperation {
	path := "/spec/tolerations"
	var value interface{}
	if first {
		value = []corev1.Toleration{toleration}
	} else {
		path += "/-"
		value = toleration
	}

	return patchOperation{Op: "add", Path: path, Value: value}
}

func addSecurityContext(pod *corev1.Pod, app *v1beta2.SparkApplication) *patchOperation {
	var secContext *corev1.PodSecurityContext
	if util.IsDriverPod(pod) {
		secContext = app.Spec.Driver.SecurityContenxt
	} else if util.IsExecutorPod(pod) {
		secContext = app.Spec.Executor.SecurityContenxt
	}

	if secContext == nil {
		return nil
	}
	return &patchOperation{Op: "add", Path: "/spec/securityContext", Value: *secContext}
}

func addSidecarContainers(pod *corev1.Pod, app *v1beta2.SparkApplication) []patchOperation {
	var sidecars []corev1.Container
	if util.IsDriverPod(pod) {
		sidecars = app.Spec.Driver.Sidecars
	} else if util.IsExecutorPod(pod) {
		sidecars = app.Spec.Executor.Sidecars
	}

	var ops []patchOperation
	for _, c := range sidecars {
		sd := c
		if !hasContainer(pod, &sd) {
			ops = append(ops, patchOperation{Op: "add", Path: "/spec/containers/-", Value: &sd})
		}
	}
	return ops
}

func addInitContainers(pod *corev1.Pod, app *v1beta2.SparkApplication) []patchOperation {
	var initContainers []corev1.Container
	if util.IsDriverPod(pod) {
		initContainers = app.Spec.Driver.InitContainers
	} else if util.IsExecutorPod(pod) {
		initContainers = app.Spec.Executor.InitContainers
	}

	first := false
	if len(pod.Spec.InitContainers) == 0 {
		first = true
	}

	var ops []patchOperation
	for _, c := range initContainers {
		sd := c
		if first {
			first = false
			value := []corev1.Container{sd}
			ops = append(ops, patchOperation{Op: "add", Path: "/spec/initContainers", Value: value})
		} else if !hasInitContainer(pod, &sd) {
			ops = append(ops, patchOperation{Op: "add", Path: "/spec/initContainers/-", Value: &sd})
		}

	}
	return ops
}

func addGPU(pod *corev1.Pod, app *v1beta2.SparkApplication) *patchOperation {
	var gpu *v1beta2.GPUSpec
	if util.IsDriverPod(pod) {
		gpu = app.Spec.Driver.GPU
	}
	if util.IsExecutorPod(pod) {
		gpu = app.Spec.Executor.GPU
	}
	if gpu == nil {
		return nil
	}
	if gpu.Name == "" {
		glog.V(2).Infof("Please specify GPU resource name, such as: nvidia.com/gpu, amd.com/gpu etc. Current gpu spec: %+v", gpu)
		return nil
	}
	if gpu.Quantity <= 0 {
		glog.V(2).Infof("GPU Quantity must be positive. Current gpu spec: %+v", gpu)
		return nil
	}
	i := 0
	// Find the driver or executor container in the pod.
	for ; i < len(pod.Spec.Containers); i++ {
		if pod.Spec.Containers[i].Name == config.SparkDriverContainerName ||
			pod.Spec.Containers[i].Name == config.SparkExecutorContainerName {
			break
		}
	}
	path := fmt.Sprintf("/spec/containers/%d/resources/limits", i)
	var value interface{}
	if len(pod.Spec.Containers[i].Resources.Limits) == 0 {
		value = corev1.ResourceList{
			corev1.ResourceName(gpu.Name): *resource.NewQuantity(gpu.Quantity, resource.DecimalSI),
		}
	} else {
		encoder := strings.NewReplacer("~", "~0", "/", "~1")
		path += "/" + encoder.Replace(gpu.Name)
		value = *resource.NewQuantity(gpu.Quantity, resource.DecimalSI)
	}
	return &patchOperation{Op: "add", Path: path, Value: value}
}

func addHostNetwork(pod *corev1.Pod, app *v1beta2.SparkApplication) []patchOperation {
	var hostNetwork *bool
	if util.IsDriverPod(pod) {
		hostNetwork = app.Spec.Driver.HostNetwork
	}
	if util.IsExecutorPod(pod) {
		hostNetwork = app.Spec.Executor.HostNetwork
	}

	if hostNetwork == nil || *hostNetwork == false {
		return nil
	}
	var ops []patchOperation
	ops = append(ops, patchOperation{Op: "add", Path: "/spec/hostNetwork", Value: true})
	// For Pods with hostNetwork, explicitly set its DNS policy  to “ClusterFirstWithHostNet”
	// Detail: https://kubernetes.io/docs/concepts/services-networking/dns-pod-service/#pod-s-dns-policy
	ops = append(ops, patchOperation{Op: "add", Path: "/spec/dnsPolicy", Value: corev1.DNSClusterFirstWithHostNet})
	return ops
}

func hasContainer(pod *corev1.Pod, container *corev1.Container) bool {
	for _, c := range pod.Spec.Containers {
		if container.Name == c.Name && container.Image == c.Image {
			return true
		}
	}
	return false
}

func hasInitContainer(pod *corev1.Pod, container *corev1.Container) bool {
	for _, c := range pod.Spec.InitContainers {
		if container.Name == c.Name && container.Image == c.Image {
			return true
		}
	}
	return false
}
