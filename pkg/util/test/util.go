package test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/libopenstorage/openstorage/api"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/mock"
	"github.com/libopenstorage/operator/pkg/util"
	ocp_configv1 "github.com/openshift/api/config/v1"
	appops "github.com/portworx/sched-ops/k8s/apps"
	coreops "github.com/portworx/sched-ops/k8s/core"
	k8serrors "github.com/portworx/sched-ops/k8s/errors"
	operatorops "github.com/portworx/sched-ops/k8s/operator"
	prometheusops "github.com/portworx/sched-ops/k8s/prometheus"
	"github.com/portworx/sched-ops/task"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	fakeextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
	pluginhelper "k8s.io/kubernetes/pkg/scheduler/framework/plugins/helper"
	cluster_v1alpha1 "sigs.k8s.io/cluster-api/pkg/apis/deprecated/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	// PxReleaseManifestURLEnvVarName is a release manifest URL Env variable name
	PxReleaseManifestURLEnvVarName = "PX_RELEASE_MANIFEST_URL"

	// PxRegistryUserEnvVarName is a Docker username Env variable name
	PxRegistryUserEnvVarName = "REGISTRY_USER"
	// PxRegistryPasswordEnvVarName is a Docker password Env variable name
	PxRegistryPasswordEnvVarName = "REGISTRY_PASS"
	// PxImageEnvVarName is the env variable to specify a specific Portworx image to install
	PxImageEnvVarName = "PX_IMAGE"
)

// TestSpecPath is the path for all test specs. Due to currently functional test and
// unit test use different path, this needs to be set accordingly.
var TestSpecPath = "testspec"

// MockDriver creates a mock storage driver
func MockDriver(mockCtrl *gomock.Controller) *mock.MockDriver {
	return mock.NewMockDriver(mockCtrl)
}

// FakeK8sClient creates a fake controller-runtime Kubernetes client. Also
// adds the CRDs defined in this repository to the scheme
func FakeK8sClient(initObjects ...runtime.Object) client.Client {
	s := scheme.Scheme
	corev1.AddToScheme(s)
	monitoringv1.AddToScheme(s)
	cluster_v1alpha1.AddToScheme(s)
	ocp_configv1.AddToScheme(s)
	return fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(initObjects...).Build()
}

// List returns a list of objects using the given Kubernetes client
func List(k8sClient client.Client, obj client.ObjectList) error {
	return k8sClient.List(context.TODO(), obj, &client.ListOptions{})
}

// Get returns an object using the given Kubernetes client
func Get(k8sClient client.Client, obj client.Object, name, namespace string) error {
	return k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
		obj,
	)
}

// Delete deletes an object using the given Kubernetes client
func Delete(k8sClient client.Client, obj client.Object) error {
	return k8sClient.Delete(context.TODO(), obj)
}

// Update changes an object using the given Kubernetes client and updates the resource version
func Update(k8sClient client.Client, obj client.Object) error {
	return k8sClient.Update(
		context.TODO(),
		obj,
	)
}

// GetExpectedClusterRole returns the ClusterRole object from given yaml spec file
func GetExpectedClusterRole(t *testing.T, fileName string) *rbacv1.ClusterRole {
	obj := getKubernetesObject(t, fileName)
	clusterRole, ok := obj.(*rbacv1.ClusterRole)
	assert.True(t, ok, "Expected ClusterRole object")
	return clusterRole
}

// GetExpectedClusterRoleBinding returns the ClusterRoleBinding object from given
// yaml spec file
func GetExpectedClusterRoleBinding(t *testing.T, fileName string) *rbacv1.ClusterRoleBinding {
	obj := getKubernetesObject(t, fileName)
	crb, ok := obj.(*rbacv1.ClusterRoleBinding)
	assert.True(t, ok, "Expected ClusterRoleBinding object")
	return crb
}

// GetExpectedRole returns the Role object from given yaml spec file
func GetExpectedRole(t *testing.T, fileName string) *rbacv1.Role {
	obj := getKubernetesObject(t, fileName)
	role, ok := obj.(*rbacv1.Role)
	assert.True(t, ok, "Expected Role object")
	return role
}

// GetExpectedRoleBinding returns the RoleBinding object from given yaml spec file
func GetExpectedRoleBinding(t *testing.T, fileName string) *rbacv1.RoleBinding {
	obj := getKubernetesObject(t, fileName)
	roleBinding, ok := obj.(*rbacv1.RoleBinding)
	assert.True(t, ok, "Expected RoleBinding object")
	return roleBinding
}

// GetExpectedStorageClass returns the StorageClass object from given yaml spec file
func GetExpectedStorageClass(t *testing.T, fileName string) *storagev1.StorageClass {
	obj := getKubernetesObject(t, fileName)
	storageClass, ok := obj.(*storagev1.StorageClass)
	assert.True(t, ok, "Expected StorageClass object")
	return storageClass
}

// GetExpectedConfigMap returns the ConfigMap object from given yaml spec file
func GetExpectedConfigMap(t *testing.T, fileName string) *v1.ConfigMap {
	obj := getKubernetesObject(t, fileName)
	configMap, ok := obj.(*v1.ConfigMap)
	assert.True(t, ok, "Expected ConfigMap object")
	return configMap
}

// GetExpectedSecret returns the Secret object from given yaml spec file
func GetExpectedSecret(t *testing.T, fileName string) *v1.Secret {
	obj := getKubernetesObject(t, fileName)
	secret, ok := obj.(*v1.Secret)
	assert.True(t, ok, "Expected Secret object")
	return secret
}

// GetExpectedService returns the Service object from given yaml spec file
func GetExpectedService(t *testing.T, fileName string) *v1.Service {
	obj := getKubernetesObject(t, fileName)
	service, ok := obj.(*v1.Service)
	assert.True(t, ok, "Expected Service object")
	return service
}

// GetExpectedDeployment returns the Deployment object from given yaml spec file
func GetExpectedDeployment(t *testing.T, fileName string) *appsv1.Deployment {
	obj := getKubernetesObject(t, fileName)
	deployment, ok := obj.(*appsv1.Deployment)
	assert.True(t, ok, "Expected Deployment object")
	return deployment
}

// GetExpectedStatefulSet returns the StatefulSet object from given yaml spec file
func GetExpectedStatefulSet(t *testing.T, fileName string) *appsv1.StatefulSet {
	obj := getKubernetesObject(t, fileName)
	statefulSet, ok := obj.(*appsv1.StatefulSet)
	assert.True(t, ok, "Expected StatefulSet object")
	return statefulSet
}

// GetExpectedDaemonSet returns the DaemonSet object from given yaml spec file
func GetExpectedDaemonSet(t *testing.T, fileName string) *appsv1.DaemonSet {
	obj := getKubernetesObject(t, fileName)
	daemonSet, ok := obj.(*appsv1.DaemonSet)
	assert.True(t, ok, "Expected DaemonSet object")
	return daemonSet
}

// GetExpectedCRD returns the CustomResourceDefinition object from given yaml spec file
func GetExpectedCRD(t *testing.T, fileName string) *apiextensionsv1beta1.CustomResourceDefinition {
	obj := getKubernetesObject(t, fileName)
	crd, ok := obj.(*apiextensionsv1beta1.CustomResourceDefinition)
	assert.True(t, ok, "Expected CustomResourceDefinition object")
	return crd
}

// GetExpectedPrometheus returns the Prometheus object from given yaml spec file
func GetExpectedPrometheus(t *testing.T, fileName string) *monitoringv1.Prometheus {
	obj := getKubernetesObject(t, fileName)
	prometheus, ok := obj.(*monitoringv1.Prometheus)
	assert.True(t, ok, "Expected Prometheus object")
	return prometheus
}

// GetExpectedServiceMonitor returns the ServiceMonitor object from given yaml spec file
func GetExpectedServiceMonitor(t *testing.T, fileName string) *monitoringv1.ServiceMonitor {
	obj := getKubernetesObject(t, fileName)
	serviceMonitor, ok := obj.(*monitoringv1.ServiceMonitor)
	assert.True(t, ok, "Expected ServiceMonitor object")
	return serviceMonitor
}

// GetExpectedPrometheusRule returns the PrometheusRule object from given yaml spec file
func GetExpectedPrometheusRule(t *testing.T, fileName string) *monitoringv1.PrometheusRule {
	obj := getKubernetesObject(t, fileName)
	prometheusRule, ok := obj.(*monitoringv1.PrometheusRule)
	assert.True(t, ok, "Expected PrometheusRule object")
	return prometheusRule
}

// GetExpectedAlertManager returns the AlertManager object from given yaml spec file
func GetExpectedAlertManager(t *testing.T, fileName string) *monitoringv1.Alertmanager {
	obj := getKubernetesObject(t, fileName)
	alertManager, ok := obj.(*monitoringv1.Alertmanager)
	assert.True(t, ok, "Expected Alertmanager object")
	return alertManager

}

// GetExpectedPSP returns the PodSecurityPolicy object from given yaml spec file
func GetExpectedPSP(t *testing.T, fileName string) *policyv1beta1.PodSecurityPolicy {
	obj := getKubernetesObject(t, fileName)
	psp, ok := obj.(*policyv1beta1.PodSecurityPolicy)
	assert.True(t, ok, "Expected PodSecurityPolicy object")
	return psp
}

// getKubernetesObject returns a generic Kubernetes object from given yaml file
func getKubernetesObject(t *testing.T, fileName string) runtime.Object {
	json, err := ioutil.ReadFile(path.Join(TestSpecPath, fileName))
	assert.NoError(t, err)
	s := scheme.Scheme
	apiextensionsv1beta1.AddToScheme(s)
	monitoringv1.AddToScheme(s)
	codecs := serializer.NewCodecFactory(s)
	obj, _, err := codecs.UniversalDeserializer().Decode([]byte(json), nil, nil)
	assert.NoError(t, err)
	return obj
}

// GetPullPolicyForContainer returns the image pull policy for given deployment
// and container name
func GetPullPolicyForContainer(
	deployment *appsv1.Deployment,
	containerName string,
) v1.PullPolicy {
	for _, c := range deployment.Spec.Template.Spec.Containers {
		if c.Name == containerName {
			return c.ImagePullPolicy
		}
	}
	return ""
}

// ActivateCRDWhenCreated activates the given CRD by updating it's status. It waits for
// CRD to be created for 1 minute before returning an error
func ActivateCRDWhenCreated(fakeClient *fakeextclient.Clientset, crdName string) error {
	return wait.Poll(1*time.Second, 1*time.Minute, func() (bool, error) {
		crd, err := fakeClient.ApiextensionsV1().
			CustomResourceDefinitions().
			Get(context.TODO(), crdName, metav1.GetOptions{})
		if err == nil {
			crd.Status.Conditions = []apiextensionsv1.CustomResourceDefinitionCondition{{
				Type:   apiextensionsv1.Established,
				Status: apiextensionsv1.ConditionTrue,
			}}
			fakeClient.ApiextensionsV1().
				CustomResourceDefinitions().
				UpdateStatus(context.TODO(), crd, metav1.UpdateOptions{})
			return true, nil
		} else if !errors.IsNotFound(err) {
			return false, err
		}
		return false, nil
	})
}

// ActivateV1beta1CRDWhenCreated activates the given CRD by updating it's status. It waits for
// CRD to be created for 1 minute before returning an error
func ActivateV1beta1CRDWhenCreated(fakeClient *fakeextclient.Clientset, crdName string) error {
	return wait.Poll(1*time.Second, 1*time.Minute, func() (bool, error) {
		crd, err := fakeClient.ApiextensionsV1beta1().
			CustomResourceDefinitions().
			Get(context.TODO(), crdName, metav1.GetOptions{})
		if err == nil {
			crd.Status.Conditions = []apiextensionsv1beta1.CustomResourceDefinitionCondition{{
				Type:   apiextensionsv1beta1.Established,
				Status: apiextensionsv1beta1.ConditionTrue,
			}}
			fakeClient.ApiextensionsV1beta1().
				CustomResourceDefinitions().
				UpdateStatus(context.TODO(), crd, metav1.UpdateOptions{})
			return true, nil
		} else if !errors.IsNotFound(err) {
			return false, err
		}
		return false, nil
	})
}

// UninstallStorageCluster uninstalls and wipe storagecluster from k8s
func UninstallStorageCluster(cluster *corev1.StorageCluster, kubeconfig ...string) error {
	var err error
	if len(kubeconfig) != 0 && kubeconfig[0] != "" {
		os.Setenv("KUBECONFIG", kubeconfig[0])
	}
	cluster, err = operatorops.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if cluster.Spec.DeleteStrategy == nil ||
		(cluster.Spec.DeleteStrategy.Type != corev1.UninstallAndWipeStorageClusterStrategyType &&
			cluster.Spec.DeleteStrategy.Type != corev1.UninstallStorageClusterStrategyType) {
		cluster.Spec.DeleteStrategy = &corev1.StorageClusterDeleteStrategy{
			Type: corev1.UninstallAndWipeStorageClusterStrategyType,
		}
		if _, err = operatorops.Instance().UpdateStorageCluster(cluster); err != nil {
			return err
		}
	}

	return operatorops.Instance().DeleteStorageCluster(cluster.Name, cluster.Namespace)
}

// ValidateStorageCluster validates a StorageCluster spec
func ValidateStorageCluster(
	pxImageList map[string]string,
	clusterSpec *corev1.StorageCluster,
	timeout, interval time.Duration,
	shouldStartSuccessfully bool,
	kubeconfig ...string,
) error {
	// Set kubeconfig
	if len(kubeconfig) != 0 && kubeconfig[0] != "" {
		os.Setenv("KUBECONFIG", kubeconfig[0])
	}

	// Validate StorageCluster
	var liveCluster *corev1.StorageCluster
	var err error
	if shouldStartSuccessfully {
		liveCluster, err = ValidateStorageClusterIsOnline(clusterSpec, timeout, interval)
		if err != nil {
			return err
		}
	} else {
		// If we shouldn't start successfully, this is all we need to check
		return validateStorageClusterIsFailed(clusterSpec, timeout, interval)
	}

	// Validate that spec matches live spec
	if err = validateDeployedSpec(clusterSpec, liveCluster); err != nil {
		return err
	}

	// Validate StorageNodes
	if err = validateStorageNodes(pxImageList, clusterSpec, timeout, interval); err != nil {
		return err
	}

	// Get list of expected Portworx node names
	expectedPxNodeNameList, err := GetExpectedPxNodeNameList(clusterSpec)
	if err != nil {
		return err
	}

	// Validate Portworx pods
	if err = validateStorageClusterPods(clusterSpec, expectedPxNodeNameList, timeout, interval); err != nil {
		return err
	}

	// Validate Portworx nodes
	if err = validatePortworxNodes(liveCluster, len(expectedPxNodeNameList)); err != nil {
		return err
	}

	if err = validateComponents(pxImageList, liveCluster, timeout, interval); err != nil {
		return err
	}

	return nil
}

func validateStorageNodes(pxImageList map[string]string, cluster *corev1.StorageCluster, timeout, interval time.Duration) error {
	var pxVersion string

	imageOverride := ""

	// Check if we have a PX_IMAGE set
	for _, env := range cluster.Spec.Env {
		if env.Name == PxImageEnvVarName {
			imageOverride = env.Value
			break
		}
	}

	// Construct PX Version string used to match to deployed expected PX version
	if strings.Contains(pxImageList["version"], "_") {
		if len(cluster.Spec.Env) > 0 {
			for _, env := range cluster.Spec.Env {
				if env.Name == PxReleaseManifestURLEnvVarName {
					pxVersion = strings.TrimSpace(regexp.MustCompile(`\S+\/(\S+)\/version`).FindStringSubmatch(env.Value)[1])
					if pxVersion == "" {
						return fmt.Errorf("failed to extract version from value of %s", PxReleaseManifestURLEnvVarName)
					}
				}
			}
		}
	} else {
		pxVersion = strings.TrimSpace(regexp.MustCompile(`:(\S+)`).FindStringSubmatch(pxImageList["version"])[1])
	}

	t := func() (interface{}, bool, error) {
		// Get all StorageNodes
		storageNodeList, err := operatorops.Instance().ListStorageNodes(cluster.Namespace)
		if err != nil {
			return nil, true, err
		}

		// Check StorageNodes status and PX version
		expectedStatus := "Online"
		var readyNodes int
		for _, storageNode := range storageNodeList.Items {
			logString := fmt.Sprintf("storagenode: %s Expected status: %s Got: %s, ", storageNode.Name, expectedStatus, storageNode.Status.Phase)
			if imageOverride != "" {
				logString += fmt.Sprintf("Running PX version: %s From image: %s", storageNode.Spec.Version, imageOverride)
			} else {
				logString += fmt.Sprintf("Expected PX version: %s Got: %s", pxVersion, storageNode.Spec.Version)
			}
			logrus.Debug(logString)

			// Don't mark this node as ready if it's not in the expected phase
			if storageNode.Status.Phase != expectedStatus {
				continue
			}
			// If we didn't specify a custom image, make sure it's running the expected version
			if imageOverride == "" && !strings.Contains(storageNode.Spec.Version, pxVersion) {
				continue
			}

			readyNodes++
		}

		if readyNodes != len(storageNodeList.Items) {
			return nil, true, fmt.Errorf("waiting for all storagenodes to be ready: %d/%d", readyNodes, len(storageNodeList.Items))
		}
		return nil, false, nil
	}

	if _, err := task.DoRetryWithTimeout(t, timeout, interval); err != nil {
		return err
	}

	return nil
}

// nodeSpecsToMaps takes the given node spec list and converts it to a map of node names to
// cloud storage specs. Note that this will not work for label selectors at the moment, only
// node names.
func nodeSpecsToMaps(nodes []corev1.NodeSpec) map[string]*corev1.CloudStorageNodeSpec {
	toReturn := map[string]*corev1.CloudStorageNodeSpec{}

	for _, node := range nodes {
		toReturn[node.Selector.NodeName] = node.CloudStorage
	}

	return toReturn
}

func validateDeployedSpec(expected, live *corev1.StorageCluster) error {
	// Validate cloudStorage
	if !reflect.DeepEqual(expected.Spec.CloudStorage, live.Spec.CloudStorage) {
		return fmt.Errorf("deployed CloudStorage spec doesn't match expected")
	}
	// Validate kvdb
	if !reflect.DeepEqual(expected.Spec.Kvdb, live.Spec.Kvdb) {
		return fmt.Errorf("deployed Kvdb spec doesn't match expected")
	}
	// Validate nodes
	if !reflect.DeepEqual(nodeSpecsToMaps(expected.Spec.Nodes), nodeSpecsToMaps(live.Spec.Nodes)) {
		return fmt.Errorf("deployed Nodes spec doesn't match expected")
	}

	// TODO: validate more parts of the spec as we test with them

	return nil
}

// NewResourceVersion creates a random 16 character string
// to simulate a k8s resource version
func NewResourceVersion() string {
	var randBytes = make([]byte, 32)
	_, err := rand.Read(randBytes)
	if err != nil {
		return ""
	}

	ver := make([]byte, base64.StdEncoding.EncodedLen(len(randBytes)))
	base64.StdEncoding.Encode(ver, randBytes)

	return string(ver[:16])
}

func getSdkConnection(cluster *corev1.StorageCluster) (*grpc.ClientConn, error) {
	pxEndpoint, err := coreops.Instance().GetServiceEndpoint("portworx-service", cluster.Namespace)
	if err != nil {
		return nil, err
	}

	svc, err := coreops.Instance().GetService("portworx-service", cluster.Namespace)
	if err != nil {
		return nil, err
	}

	servicePort := int32(0)
	nodePort := ""
	for _, port := range svc.Spec.Ports {
		if port.Name == "px-sdk" {
			servicePort = port.Port
			nodePort = port.TargetPort.StrVal
			break
		}
	}

	if servicePort == 0 {
		return nil, fmt.Errorf("px-sdk port not found in service")
	}

	conn, err := grpc.Dial(fmt.Sprintf("%s:%d", pxEndpoint, servicePort), grpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	// try over the service endpoint
	cli := api.NewOpenStorageIdentityClient(conn)
	if _, err = cli.Version(context.Background(), &api.SdkIdentityVersionRequest{}); err == nil {
		return conn, nil
	}

	// if  service endpoint IP is not accessible, we pick one node IP
	if nodes, err := coreops.Instance().GetNodes(); err == nil {
		for _, node := range nodes.Items {
			for _, addr := range node.Status.Addresses {
				if addr.Type == v1.NodeInternalIP {
					conn, err := grpc.Dial(fmt.Sprintf("%s:%s", addr.Address, nodePort), grpc.WithInsecure())
					if err != nil {
						return nil, err
					}
					if _, err = cli.Version(context.Background(), &api.SdkIdentityVersionRequest{}); err == nil {
						return conn, nil
					}
				}
			}
		}
	}
	return nil, err
}

// ValidateUninstallStorageCluster validates if storagecluster and its related objects
// were properly uninstalled and cleaned
func ValidateUninstallStorageCluster(
	cluster *corev1.StorageCluster,
	timeout, interval time.Duration,
	kubeconfig ...string,
) error {
	if len(kubeconfig) != 0 && kubeconfig[0] != "" {
		os.Setenv("KUBECONFIG", kubeconfig[0])
	}
	t := func() (interface{}, bool, error) {
		cluster, err := operatorops.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		if err != nil {
			if errors.IsNotFound(err) {
				return "", false, nil
			}
			return "", true, err
		}

		pods, err := coreops.Instance().GetPodsByOwner(cluster.UID, cluster.Namespace)
		if err != nil && err != k8serrors.ErrPodsNotFound {
			return "", true, fmt.Errorf("failed to get pods for StorageCluster %s/%s. Err: %v",
				cluster.Namespace, cluster.Name, err)
		}

		var podsToBeDeleted []string
		for _, pod := range pods {
			podsToBeDeleted = append(podsToBeDeleted, pod.Name)
		}

		if len(pods) > 0 {
			return "", true, fmt.Errorf("%d pods are still present, waiting for Portworx pods to be deleted: %s", len(pods), podsToBeDeleted)
		}

		return "", true, fmt.Errorf("pods are deleted, but StorageCluster %v/%v still present",
			cluster.Namespace, cluster.Name)
	}

	if _, err := task.DoRetryWithTimeout(t, timeout, interval); err != nil {
		return err
	}
	return nil
}

func validateStorageClusterPods(
	clusterSpec *corev1.StorageCluster,
	expectedPxNodeNameList []string,
	timeout, interval time.Duration,
) error {
	t := func() (interface{}, bool, error) {
		cluster, err := operatorops.Instance().GetStorageCluster(clusterSpec.Name, clusterSpec.Namespace)
		if err != nil {
			return "", true, err
		}

		pods, err := coreops.Instance().GetPodsByOwner(cluster.UID, cluster.Namespace)
		if err != nil || pods == nil {
			return "", true, fmt.Errorf("failed to get pods for StorageCluster %s/%s. Err: %v",
				cluster.Namespace, cluster.Name, err)
		}

		if len(pods) != len(expectedPxNodeNameList) {
			return "", true, fmt.Errorf("expected pods: %v. actual pods: %v", len(expectedPxNodeNameList), len(pods))
		}

		var pxNodeNameList []string
		var podsNotReady []string
		var podsReady []string
		for _, pod := range pods {
			if !coreops.Instance().IsPodReady(pod) {
				podsNotReady = append(podsNotReady, pod.Name)
			}
			pxNodeNameList = append(pxNodeNameList, pod.Spec.NodeName)
			podsReady = append(podsReady, pod.Name)
		}

		if len(podsNotReady) > 0 {
			return "", true, fmt.Errorf("waiting for Portworx pods to be ready: %s", podsNotReady)
		}

		if !assert.ElementsMatch(&testing.T{}, expectedPxNodeNameList, pxNodeNameList) {
			return "", false, fmt.Errorf("expected Portworx nodes: %+v, got %+v", expectedPxNodeNameList, pxNodeNameList)
		}

		logrus.Debugf("All Portworx pods are ready: %s", podsReady)
		return "", false, nil
	}

	if _, err := task.DoRetryWithTimeout(t, timeout, interval); err != nil {
		return err
	}

	return nil
}

// Set default Node Affinity rules as Portworx Operator would when deploying StorageCluster
func defaultPxNodeAffinityRules() *v1.NodeAffinity {
	nodeAffinity := &v1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
			NodeSelectorTerms: []v1.NodeSelectorTerm{
				{
					MatchExpressions: []v1.NodeSelectorRequirement{
						{
							Key:      "px/enabled",
							Operator: v1.NodeSelectorOpNotIn,
							Values:   []string{"false"},
						},
						{
							Key:      "node-role.kubernetes.io/master",
							Operator: v1.NodeSelectorOpDoesNotExist,
						},
					},
				},
			},
		},
	}

	return nodeAffinity
}

func validatePortworxNodes(cluster *corev1.StorageCluster, expectedNodes int) error {
	conn, err := getSdkConnection(cluster)
	if err != nil {
		return nil
	}

	nodeClient := api.NewOpenStorageNodeClient(conn)
	nodeEnumerateResp, err := nodeClient.Enumerate(context.Background(), &api.SdkNodeEnumerateRequest{})
	if err != nil {
		return err
	}

	actualNodes := len(nodeEnumerateResp.GetNodeIds())
	if actualNodes != expectedNodes {
		return fmt.Errorf("expected nodes: %v. actual nodes: %v", expectedNodes, actualNodes)
	}

	// TODO: Validate Portworx is started with correct params. Check individual options
	for _, n := range nodeEnumerateResp.GetNodeIds() {
		nodeResp, err := nodeClient.Inspect(context.Background(), &api.SdkNodeInspectRequest{NodeId: n})
		if err != nil {
			return err
		}
		if nodeResp.Node.Status != api.Status_STATUS_OK {
			return fmt.Errorf("node %s is not online. Current: %v", nodeResp.Node.SchedulerNodeName,
				nodeResp.Node.Status)
		}

	}
	return nil
}

// GetExpectedPxNodeNameList will get the list of node names that should be included
// in the given Portworx cluster, by seeing if each non-master node matches the given
// node selectors and affinities.
func GetExpectedPxNodeNameList(cluster *corev1.StorageCluster) ([]string, error) {
	var nodeNameListWithPxPods []string
	nodeList, err := coreops.Instance().GetNodes()
	if err != nil {
		return nodeNameListWithPxPods, err
	}

	dummyPod := &v1.Pod{}
	if cluster.Spec.Placement != nil && cluster.Spec.Placement.NodeAffinity != nil {
		dummyPod.Spec.Affinity = &v1.Affinity{
			NodeAffinity: cluster.Spec.Placement.NodeAffinity.DeepCopy(),
		}
	} else {
		dummyPod.Spec.Affinity = &v1.Affinity{
			NodeAffinity: defaultPxNodeAffinityRules(),
		}
	}

	for _, node := range nodeList.Items {
		if coreops.Instance().IsNodeMaster(node) {
			continue
		}
		if pluginhelper.PodMatchesNodeSelectorAndAffinityTerms(dummyPod, &node) {
			nodeNameListWithPxPods = append(nodeNameListWithPxPods, node.Name)
		}
	}

	return nodeNameListWithPxPods, nil
}

func validateComponents(pxImageList map[string]string, cluster *corev1.StorageCluster, timeout, interval time.Duration) error {
	k8sVersion, err := GetK8SVersion()
	if err != nil {
		return err
	}

	if isPVCControllerEnabled(cluster) {
		pvcControllerDp := &appsv1.Deployment{}
		pvcControllerDp.Name = "portworx-pvc-controller"
		pvcControllerDp.Namespace = cluster.Namespace
		if err = appops.Instance().ValidateDeployment(pvcControllerDp, timeout, interval); err != nil {
			return err
		}

		if err = validateImageTag(k8sVersion, cluster.Namespace, map[string]string{"name": "portworx-pvc-controller"}); err != nil {
			return err
		}
	}

	// Validate Stork components and images
	if err := ValidateStork(pxImageList, cluster, k8sVersion, timeout, interval); err != nil {
		return err
	}

	if cluster.Spec.Autopilot != nil && cluster.Spec.Autopilot.Enabled {
		autopilotDp := &appsv1.Deployment{}
		autopilotDp.Name = "autopilot"
		autopilotDp.Namespace = cluster.Namespace
		if err = appops.Instance().ValidateDeployment(autopilotDp, timeout, interval); err != nil {
			return err
		}

		var autopilotImageName string
		if cluster.Spec.Autopilot.Image == "" {
			if value, ok := pxImageList["autopilot"]; ok {
				autopilotImageName = value
			} else {
				return fmt.Errorf("failed to find image for autopilot")
			}
		} else {
			autopilotImageName = cluster.Spec.Autopilot.Image
		}

		autopilotImage := util.GetImageURN(cluster, autopilotImageName)
		if err = validateImageOnPods(autopilotImage, cluster.Namespace, map[string]string{"name": "autopilot"}); err != nil {
			return err
		}
	}

	if cluster.Spec.UserInterface != nil && cluster.Spec.UserInterface.Enabled {
		lighthouseDp := &appsv1.Deployment{}
		lighthouseDp.Name = "px-lighthouse"
		lighthouseDp.Namespace = cluster.Namespace
		if err = appops.Instance().ValidateDeployment(lighthouseDp, timeout, interval); err != nil {
			return err
		}

		var lighthouseImageName string
		if cluster.Spec.UserInterface.Image == "" {
			if value, ok := pxImageList["lighthouse"]; ok {
				lighthouseImageName = value
			} else {
				return fmt.Errorf("failed to find image for lighthouse")
			}
		} else {
			lighthouseImageName = cluster.Spec.UserInterface.Image
		}

		lhImage := util.GetImageURN(cluster, lighthouseImageName)
		if err = validateImageOnPods(lhImage, cluster.Namespace, map[string]string{"name": "lighthouse"}); err != nil {
			return err
		}
	}

	// Validate CSI components and images
	if validateCSI(pxImageList, cluster, timeout, interval); err != nil {
		return err
	}

	// Validate Monitoring
	if err = validateMonitoring(pxImageList, cluster, timeout, interval); err != nil {
		return err
	}

	return nil
}

// ValidateStork validates Stork components and images
func ValidateStork(pxImageList map[string]string, cluster *corev1.StorageCluster, k8sVersion string, timeout, interval time.Duration) error {
	storkDp := &appsv1.Deployment{}
	storkDp.Name = "stork"
	storkDp.Namespace = cluster.Namespace

	storkSchedulerDp := &appsv1.Deployment{}
	storkSchedulerDp.Name = "stork-scheduler"
	storkSchedulerDp.Namespace = cluster.Namespace

	if cluster.Spec.Stork != nil && cluster.Spec.Stork.Enabled {
		logrus.Debug("Stork is Enabled in StorageCluster")

		// Validate stork deployment and pods
		if err := validateDeployment(storkDp, timeout, interval); err != nil {
			return err
		}

		var storkImageName string
		if cluster.Spec.Stork.Image == "" {
			if value, ok := pxImageList["stork"]; ok {
				storkImageName = value
			} else {
				return fmt.Errorf("failed to find image for stork")
			}
		} else {
			storkImageName = cluster.Spec.Stork.Image
		}

		storkImage := util.GetImageURN(cluster, storkImageName)
		err := validateImageOnPods(storkImage, cluster.Namespace, map[string]string{"name": "stork"})
		if err != nil {
			return err
		}

		// Validate stork-scheduler deployment and pods
		if err := validateDeployment(storkSchedulerDp, timeout, interval); err != nil {
			return err
		}

		if err = validateImageTag(k8sVersion, cluster.Namespace, map[string]string{"name": "stork-scheduler"}); err != nil {
			return err
		}

		// Validate webhook-controller arguments
		if err := validateStorkWebhookController(cluster.Spec.Stork.Args, storkDp, timeout, interval); err != nil {
			return err
		}

		// Validate hostNetwork parameter
		if err := validateStorkHostNetwork(cluster.Spec.Stork.HostNetwork, storkDp, timeout, interval); err != nil {
			return err
		}
	} else {
		logrus.Debug("Stork is Disabled in StorageCluster")
		// Validate stork deployment is terminated or doesn't exist
		if err := validateTerminatedDeployment(storkDp, timeout, interval); err != nil {
			return err
		}

		// Validate stork-scheduler deployment is terminated or doesn't exist
		if err := validateTerminatedDeployment(storkSchedulerDp, timeout, interval); err != nil {
			return err
		}
	}

	return nil
}

func validateStorkWebhookController(webhookControllerArgs map[string]string, storkDeployment *appsv1.Deployment, timeout, interval time.Duration) error {
	logrus.Debug("Validate Stork webhook-controller")

	t := func() (interface{}, bool, error) {
		pods, err := appops.Instance().GetDeploymentPods(storkDeployment)
		if err != nil {
			return nil, false, err
		}

		// Go through every Stork pod and look for --weebhook-controller command in every stork container and match it to the webhook-controller arg passed in spec
		for _, pod := range pods {
			webhookExist := false
			for _, container := range pod.Spec.Containers {
				if container.Name == "stork" {
					if len(container.Command) > 0 {
						for _, containerCommand := range container.Command {
							if strings.Contains(containerCommand, "--webhook-controller") {
								if len(webhookControllerArgs["webhook-controller"]) == 0 {
									return nil, true, fmt.Errorf("failed to validate webhook-controller, webhook-controller is missing from Stork args in the StorageCluster, but is found in the Stork pod [%s]", pod.Name)
								} else if webhookControllerArgs["webhook-controller"] != strings.Split(containerCommand, "=")[1] {
									return nil, true, fmt.Errorf("failed to validate webhook-controller, wrong --webhook-controller value in the command in Stork pod [%s]: expected: %s, got: %s", pod.Name, webhookControllerArgs["webhook-controller"], strings.Split(containerCommand, "=")[1])
								}
								logrus.Debugf("Value for webhook-controller inside Stork pod [%s] command args: expected %s, got %s", pod.Name, webhookControllerArgs["webhook-controller"], strings.Split(containerCommand, "=")[1])
								webhookExist = true
								continue
							}
						}
					}
					// Validate that if webhook-controller arg is missing from StorageCluster, it is also not found in pods
					if len(webhookControllerArgs["webhook-controller"]) != 0 && !webhookExist {
						return nil, true, fmt.Errorf("failed to validate webhook-controller, webhook-controller is found in Stork args in the StorageCluster, but missing from Stork pod [%s]", pod.Name)
					}
				}
			}
		}
		return nil, false, nil
	}

	if _, err := task.DoRetryWithTimeout(t, timeout, interval); err != nil {
		return err
	}

	return nil
}

func validateStorkHostNetwork(hostNetwork *bool, storkDeployment *appsv1.Deployment, timeout, interval time.Duration) error {
	logrus.Debug("Validate Stork hostNetwork")

	t := func() (interface{}, bool, error) {
		pods, err := appops.Instance().GetDeploymentPods(storkDeployment)
		if err != nil {
			return nil, false, err
		}

		// Setting hostNetworkValue to false if hostNetwork is nil, since its a *bool and we need to compare it to bool
		var hostNetworkValue bool
		if hostNetwork == nil {
			hostNetworkValue = false
		} else {
			hostNetworkValue = *hostNetwork
		}

		for _, pod := range pods {
			if pod.Spec.HostNetwork != hostNetworkValue {
				return nil, true, fmt.Errorf("failed to validate Stork hostNetwork inside Stork pod [%s]: expected: %v, actual: %v", pod.Name, hostNetworkValue, pod.Spec.HostNetwork)
			}
			logrus.Debugf("Value for hostNetwork inside Stork pod [%s]: epxected: %v, actual: %v", pod.Name, hostNetworkValue, pod.Spec.HostNetwork)
		}
		return nil, false, nil
	}

	if _, err := task.DoRetryWithTimeout(t, timeout, interval); err != nil {
		return err
	}

	return nil
}

func validateCSI(pxImageList map[string]string, cluster *corev1.StorageCluster, timeout, interval time.Duration) error {
	csi, _ := strconv.ParseBool(cluster.Spec.FeatureGates["CSI"])
	pxCsiDp := &appsv1.Deployment{}
	pxCsiDp.Name = "px-csi-ext"
	pxCsiDp.Namespace = cluster.Namespace

	if csi {
		logrus.Debug("CSI is enabled in StorageCluster")
		if err := validateCsiContainerInPxPods(cluster.Namespace, csi, timeout, interval); err != nil {
			return err
		}

		// Validate CSI container image inside Portworx OCI Monitor pods
		if err := validatePortworxOciMonCsiImage(cluster.Namespace, pxImageList); err != nil {
			return err
		}

		// Validate px-csi-ext deployment and pods
		if err := validateDeployment(pxCsiDp, timeout, interval); err != nil {
			return err
		}

		// Validate CSI container images inside px-csi-ext pods
		if err := validateCsiExtImages(cluster.Namespace, pxImageList); err != nil {
			return err
		}
	} else {
		logrus.Debug("CSI is disabled in StorageCluster")
		if err := validateCsiContainerInPxPods(cluster.Namespace, csi, timeout, interval); err != nil {
			return err
		}

		// Validate px-csi-ext deployment doesn't exist
		if err := validateTerminatedDeployment(pxCsiDp, timeout, interval); err != nil {
			return err
		}
	}
	return nil
}

func validateCsiContainerInPxPods(namespace string, csi bool, timeout, interval time.Duration) error {
	logrus.Debug("Validating CSI container inside Portworx OCI Monitor pods")
	listOptions := map[string]string{"name": "portworx"}

	t := func() (interface{}, bool, error) {
		var pxPodsWithCsiContainer []string

		// Get Portworx pods
		pods, err := coreops.Instance().GetPods(namespace, listOptions)
		if err != nil {
			return nil, false, err
		}

		podsReady := 0
		for _, pod := range pods.Items {
			for _, c := range pod.Status.InitContainerStatuses {
				if !c.Ready {
					continue
				}
			}
			containerReady := 0
			for _, c := range pod.Status.ContainerStatuses {
				if c.Ready {
					containerReady++
					continue
				}
			}

			if len(pod.Spec.Containers) == containerReady {
				podsReady++
			}

			for _, container := range pod.Spec.Containers {
				if container.Name == "csi-node-driver-registrar" {
					pxPodsWithCsiContainer = append(pxPodsWithCsiContainer, pod.Name)
					break
				}
			}
		}

		if csi {
			if len(pxPodsWithCsiContainer) != len(pods.Items) {
				return nil, true, fmt.Errorf("failed to validate CSI containers in PX pods: expected %d, got %d, %d/%d Ready pods", len(pods.Items), len(pxPodsWithCsiContainer), podsReady, len(pods.Items))
			}
		} else {
			if len(pxPodsWithCsiContainer) > 0 || len(pods.Items) != podsReady {
				return nil, true, fmt.Errorf("failed to validate CSI container in PX pods: expected: 0, got %d, %d/%d Ready pods", len(pxPodsWithCsiContainer), podsReady, len(pods.Items))
			}
		}
		return nil, false, nil
	}

	if _, err := task.DoRetryWithTimeout(t, timeout, interval); err != nil {
		return err
	}

	return nil
}

func validateDeployment(deployment *appsv1.Deployment, timeout, interval time.Duration) error {
	logrus.Debugf("Validating deployment %s", deployment.Name)
	return appops.Instance().ValidateDeployment(deployment, timeout, interval)
}

func validateTerminatedDeployment(deployment *appsv1.Deployment, timeout, interval time.Duration) error {
	logrus.Debugf("Validating deployment %s is terminated or doesn't exist", deployment.Name)
	return appops.Instance().ValidateTerminatedDeployment(deployment, timeout, interval)
}

func validatePortworxOciMonCsiImage(namespace string, pxImageList map[string]string) error {
	var csiNodeDriverRegistrar string

	logrus.Debug("Validating CSI container images inside Portworx OCI Monitor pods")

	// Get Portworx pods
	listOptions := map[string]string{"name": "portworx"}
	pods, err := coreops.Instance().GetPods(namespace, listOptions)
	if err != nil {
		return err
	}

	// We looking for this image in the container
	if value, ok := pxImageList["csiNodeDriverRegistrar"]; ok {
		csiNodeDriverRegistrar = value
	} else {
		return fmt.Errorf("failed to find image for csiNodeDriverRegistrar")
	}

	// Go through each pod and find all container and match images for each container
	for _, pod := range pods.Items {
		for _, container := range pod.Spec.Containers {
			if container.Name == "csi-node-driver-registrar" {
				if container.Image != csiNodeDriverRegistrar {
					return fmt.Errorf("found container %s, expected image: %s, actual image: %s", container.Name, csiNodeDriverRegistrar, container.Image)
				}
				break
			}
		}
	}

	return nil
}

func validateCsiExtImages(namespace string, pxImageList map[string]string) error {
	var csiProvisionerImage string
	var csiSnapshotterImage string
	var csiResizerImage string

	logrus.Debug("Validating CSI container images inside px-csi-ext pods")

	deployment, err := appops.Instance().GetDeployment("px-csi-ext", namespace)
	if err != nil {
		return err
	}

	pods, err := appops.Instance().GetDeploymentPods(deployment)
	if err != nil {
		return err
	}

	// We looking for these 3 images in 3 containers in the 3 px-csi-ext pods
	if value, ok := pxImageList["csiProvisioner"]; ok {
		csiProvisionerImage = value
	} else {
		return fmt.Errorf("failed to find image for csiProvisioner")
	}

	if value, ok := pxImageList["csiSnapshotter"]; ok {
		csiSnapshotterImage = value
	} else {
		return fmt.Errorf("failed to find image for csiSnapshotter")
	}

	if value, ok := pxImageList["csiResizer"]; ok {
		csiResizerImage = value
	} else {
		return fmt.Errorf("failed to find image for csiResizer")
	}

	// Go through each pod and find all container and match images for each container
	for _, pod := range pods {
		for _, container := range pod.Spec.Containers {
			if container.Name == "csi-external-provisioner" {
				if container.Image != csiProvisionerImage {
					return fmt.Errorf("found container %s, expected image: %s, actual image: %s", container.Name, csiProvisionerImage, container.Image)
				}
			} else if container.Name == "csi-snapshotter" {
				if container.Image != csiSnapshotterImage {
					return fmt.Errorf("found container %s, expected image: %s, actual image: %s", container.Name, csiSnapshotterImage, container.Image)
				}
			} else if container.Name == "csi-resizer" {
				if container.Image != csiResizerImage {
					return fmt.Errorf("found container %s, expected image: %s, actual image: %s", container.Name, csiResizerImage, container.Image)
				}
			}
		}
	}
	return nil
}

func validateImageOnPods(image, namespace string, listOptions map[string]string) error {
	pods, err := coreops.Instance().GetPods(namespace, listOptions)
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		foundImage := false
		for _, container := range pod.Spec.Containers {
			if container.Image == image {
				foundImage = true
				break
			}
		}

		if !foundImage {
			return fmt.Errorf("failed to validade image %s on pod: %v",
				image, pod)
		}
	}
	return nil
}

func validateImageTag(tag, namespace string, listOptions map[string]string) error {
	pods, err := coreops.Instance().GetPods(namespace, listOptions)
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		for _, container := range pod.Spec.Containers {
			imageSplit := strings.Split(container.Image, ":")
			imageTag := ""
			if len(imageSplit) == 2 {
				imageTag = imageSplit[1]
			}
			if imageTag != tag {
				return fmt.Errorf("failed to validade image tag on pod %s container %s, Expected: %s Got: %s",
					pod.Name, container.Name, tag, imageTag)
			}
		}
	}
	return nil
}

func validateMonitoring(pxImageList map[string]string, cluster *corev1.StorageCluster, timeout, interval time.Duration) error {
	if cluster.Spec.Monitoring != nil &&
		((cluster.Spec.Monitoring.EnableMetrics != nil && *cluster.Spec.Monitoring.EnableMetrics) ||
			(cluster.Spec.Monitoring.Prometheus != nil && cluster.Spec.Monitoring.Prometheus.ExportMetrics)) {
		if cluster.Spec.Monitoring.Prometheus != nil && cluster.Spec.Monitoring.Prometheus.Enabled {
			dep := appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "px-prometheus-operator",
					Namespace: cluster.Namespace,
				},
			}
			if err := appops.Instance().ValidateDeployment(&dep, timeout, interval); err != nil {
				return err
			}

			st := appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "prometheus-px-prometheus",
					Namespace: cluster.Namespace,
				},
			}
			if err := appops.Instance().ValidateStatefulSet(&st, timeout); err != nil {
				return err
			}
		}

		t := func() (interface{}, bool, error) {
			_, err := prometheusops.Instance().GetPrometheusRule("portworx", cluster.Namespace)
			if err != nil {
				return nil, true, err
			}
			return nil, false, nil
		}
		if _, err := task.DoRetryWithTimeout(t, timeout, interval); err != nil {
			return err
		}

		t = func() (interface{}, bool, error) {
			_, err := prometheusops.Instance().GetServiceMonitor("portworx", cluster.Namespace)
			if err != nil {
				return nil, true, err
			}
			return nil, false, nil
		}
		if _, err := task.DoRetryWithTimeout(t, timeout, interval); err != nil {
			return err
		}
	}

	err := ValidateTelemetry(pxImageList, cluster, timeout, interval)
	if err != nil {
		return err
	}

	return nil
}

// ValidateTelemetry validates telemetry component is running as expected
func ValidateTelemetry(pxImageList map[string]string, cluster *corev1.StorageCluster, timeout, interval time.Duration) error {
	if cluster.Spec.Monitoring != nil && cluster.Spec.Monitoring.Telemetry.Enabled {
		// wait for the deployment to become online
		dep := appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "px-metrics-collector",
				Namespace: cluster.Namespace,
			},
		}
		if err := appops.Instance().ValidateDeployment(&dep, timeout, interval); err != nil {
			return err
		}

		// Verify telemetry config map
		_, err := coreops.Instance().GetConfigMap("px-telemetry-config", cluster.Namespace)
		if err != nil {
			return err
		}

		// Verify collector config map
		_, err = coreops.Instance().GetConfigMap("px-collector-config", cluster.Namespace)
		if err != nil {
			return err
		}

		// Verify collector proxy config map
		_, err = coreops.Instance().GetConfigMap("px-collector-proxy-config", cluster.Namespace)
		if err != nil {
			return err
		}

		// Verify collector service account
		_, err = coreops.Instance().GetServiceAccount("px-metrics-collector", cluster.Namespace)
		if err != nil {
			return err
		}

		// TODO: uncomment following test code after spec-gen is updated.
		//// Verify collector image
		//imageName, ok := pxImageList["metricsCollector"]
		//if !ok {
		//	return fmt.Errorf("failed to find image for metrics collector")
		//}
		//
		//imageName = util.GetImageURN(cluster, imageName)
		//
		//deployment, err := appops.Instance().GetDeployment("px-metrics-collector", cluster.Namespace)
		//if err != nil {
		//	return err
		//}
		//
		//if deployment.Spec.Template.Spec.Containers[0].Image != imageName {
		//	return fmt.Errorf("collector image mismatch, image: %s, expected: %s",
		//		deployment.Spec.Template.Spec.Containers[0].Image,
		//		imageName)
		//}
		//
		//// Verify collector proxy image
		//imageName, ok = pxImageList["metricsCollectorProxy"]
		//if !ok {
		//	return fmt.Errorf("failed to find image for metrics collector")
		//}
		//
		//imageName = util.GetImageURN(cluster, imageName)
		//if deployment.Spec.Template.Spec.Containers[1].Image != imageName {
		//	return fmt.Errorf("collector proxy image mismatch, image: %s, expected: %s",
		//		deployment.Spec.Template.Spec.Containers[1].Image,
		//		imageName)
		//}
	}

	return nil
}

func isPVCControllerEnabled(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations["portworx.io/pvc-controller"])
	if err == nil {
		return enabled
	}

	// If portworx is disabled, then do not run pvc controller unless explicitly told to.
	if !isPortworxEnabled(cluster) {
		return false
	}

	// Enable PVC controller for managed kubernetes services. Also enable it for openshift,
	// only if Portworx service is not deployed in kube-system namespace.
	if isPKS(cluster) || isEKS(cluster) ||
		isGKE(cluster) || isAKS(cluster) ||
		(isOpenshift(cluster) && cluster.Namespace != "kube-system") {
		return true
	}
	return false
}

func isPortworxEnabled(cluster *corev1.StorageCluster) bool {
	disabled, err := strconv.ParseBool(cluster.Annotations["operator.libopenstorage.org/disable-storage"])
	return err != nil || !disabled
}

func isPKS(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations["portworx.io/is-pks"])
	return err == nil && enabled
}

func isGKE(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations["portworx.io/is-gke"])
	return err == nil && enabled
}

func isAKS(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations["portworx.io/is-aks"])
	return err == nil && enabled
}

func isEKS(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations["portworx.io/is-eks"])
	return err == nil && enabled
}

func isOpenshift(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations["portworx.io/is-openshift"])
	return err == nil && enabled
}

// GetK8SVersion gets and return K8S server version
func GetK8SVersion() (string, error) {
	kbVerRegex := regexp.MustCompile(`^(v\d+\.\d+\.\d+).*`)
	k8sVersion, err := coreops.Instance().GetVersion()
	if err != nil {
		return "", fmt.Errorf("unable to get kubernetes version: %v", err)
	}
	matches := kbVerRegex.FindStringSubmatch(k8sVersion.GitVersion)
	if len(matches) < 2 {
		return "", fmt.Errorf("invalid kubernetes version received: %v", k8sVersion.GitVersion)
	}
	return matches[1], nil
}

// GetImagesFromVersionURL gets images from version URL
func GetImagesFromVersionURL(url, k8sVersion string) (map[string]string, error) {
	imageListMap := make(map[string]string)

	// Construct PX version URL
	pxVersionURL, err := ConstructVersionURL(url, k8sVersion)
	if err != nil {
		return nil, err
	}
	logrus.Infof("Get component images from version URL %s", pxVersionURL)

	resp, err := http.Get(pxVersionURL)
	if err != nil {
		return nil, fmt.Errorf("failed to send GET request to %s, Err: %v", pxVersionURL, err)
	}

	htmlData, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %+v", resp.Body)
	}

	for _, line := range strings.Split(string(htmlData), "\n") {
		if strings.Contains(line, "components") || line == "" {
			continue
		}

		imageNameSplit := strings.Split(strings.TrimSpace(line), ": ")

		if strings.Contains(line, "version") {
			imageListMap["version"] = fmt.Sprintf("portworx/oci-monitor:%s", imageNameSplit[1])
			continue
		}
		imageListMap[imageNameSplit[0]] = imageNameSplit[1]
	}

	return imageListMap, nil
}

// ConstructVersionURL constructs Portworx version URL that contains component images
func ConstructVersionURL(specGenURL, k8sVersion string) (string, error) {
	versionURL := path.Join(specGenURL, fmt.Sprintf("version?kbver=%s", k8sVersion))
	u, err := url.Parse(versionURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse URL [%s], Err: %v", specGenURL, err)
	}

	// TODO Using strings.Replace to replace /// with //, because I coulnd't figure out how not to properly construct this URL without ///
	return strings.Replace(u.String(), "///", "//", -1), nil
}

// ConstructPxReleaseManifestURL constructs Portworx install URL
func ConstructPxReleaseManifestURL(specGenURL string) (string, error) {
	u, err := url.Parse(specGenURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse URL [%s], Err: %v", specGenURL, err)
	}

	u.Path = path.Join(u.Path, "version")
	return u.String(), nil
}

func validateStorageClusterInState(cluster *corev1.StorageCluster, status corev1.ClusterConditionStatus) func() (interface{}, bool, error) {
	return func() (interface{}, bool, error) {
		cluster, err := operatorops.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		if err != nil {
			return nil, true, fmt.Errorf("failed to get StorageCluster %s in %s, Err: %v", cluster.Name, cluster.Namespace, err)
		}
		if cluster.Status.Phase != string(status) {
			if cluster.Status.Phase == "" {
				return nil, true, fmt.Errorf("failed to get cluster status")
			}
			return nil, true, fmt.Errorf("cluster state: %s", cluster.Status.Phase)
		}
		return cluster, false, nil
	}
}

func validateAllStorageNodesInState(namespace string, status corev1.NodeConditionStatus) func() (interface{}, bool, error) {
	return func() (interface{}, bool, error) {
		// Get all StorageNodes
		storageNodeList, err := operatorops.Instance().ListStorageNodes(namespace)
		if err != nil {
			return nil, true, fmt.Errorf("failed to list StorageNodes in %s, Err: %v", namespace, err)
		}

		for _, node := range storageNodeList.Items {
			if node.Status.Phase != string(status) {
				return nil, true, fmt.Errorf("StorageNode %s in %s is in state %v", node.Name, namespace, node.Status.Phase)
			}
		}

		return nil, false, nil
	}
}

// ValidateStorageClusterIsOnline wait for storage cluster to become online.
func ValidateStorageClusterIsOnline(cluster *corev1.StorageCluster, timeout, interval time.Duration) (*corev1.StorageCluster, error) {
	out, err := task.DoRetryWithTimeout(validateStorageClusterInState(cluster, corev1.ClusterOnline), timeout, interval)
	if err != nil {
		return nil, fmt.Errorf("failed to wait for StorageCluster to be ready, Err: %v", err)
	}
	cluster = out.(*corev1.StorageCluster)

	return cluster, nil
}

func validateStorageClusterIsFailed(cluster *corev1.StorageCluster, timeout, interval time.Duration) error {
	_, err := task.DoRetryWithTimeout(validateAllStorageNodesInState(cluster.Namespace, corev1.NodeFailedStatus), timeout, interval)
	if err != nil {
		return fmt.Errorf("failed to wait for StorageNodes to be failed, Err: %v", err)
	}
	return nil
}

// CreateClusterWithTLS is a helper method
func CreateClusterWithTLS(caCertFileName, serverCertFileName, serverKeyFileName *string) *corev1.StorageCluster {
	var apicert *corev1.CertLocation = nil
	if caCertFileName != nil {
		apicert = &corev1.CertLocation{
			FileName: caCertFileName,
		}
	}
	var serverCert *corev1.CertLocation = nil
	if serverCertFileName != nil {
		serverCert = &corev1.CertLocation{
			FileName: serverCertFileName,
		}
	}
	var serverkey *corev1.CertLocation = nil
	if serverKeyFileName != nil {
		serverkey = &corev1.CertLocation{
			FileName: serverKeyFileName,
		}
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Security: &corev1.SecuritySpec{
				Enabled: true,
				Auth: &corev1.AuthSpec{
					Enabled: BoolPtr(false),
				},
				TLS: &corev1.TLSSpec{
					Enabled:    BoolPtr(true),
					RootCA:     apicert,
					ServerCert: serverCert,
					ServerKey:  serverkey,
				},
			},
		},
	}
	return cluster
}

// BoolPtr returns a pointer to provided bool value
func BoolPtr(val bool) *bool {
	return &val
}
