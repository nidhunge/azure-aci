/*
Copyright (c) Microsoft Corporation.
Licensed under the Apache 2.0 license.
*/
package provider

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	azaciv2 "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerinstance/armcontainerinstance/v2"
	"github.com/cpuguy83/dockercfg"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
	"github.com/virtual-kubelet/azure-aci/pkg/auth"
	testsutil "github.com/virtual-kubelet/azure-aci/pkg/tests"
	"github.com/virtual-kubelet/azure-aci/pkg/util"
	"github.com/virtual-kubelet/virtual-kubelet/node/nodeutil"
	"gotest.tools/assert"

	"github.com/virtual-kubelet/virtual-kubelet/node/api"
	is "gotest.tools/assert/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	fakeResourceGroup = "vk-rg"
	fakeNodeName      = "vk"
	fakeVnetName      = "vnet"
)

var (
	gpuSKU       = azaciv2.GpuSKUP100
	fakeRegion   = getEnv("LOCATION", "westus2")
	creationTime = "2006-01-02 15:04:05.999999999 -0700 MST"
	azConfig     auth.Config
	runningState = "Running"
)

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

// Test make registry credential
func TestMakeRegistryCredential(t *testing.T) {
	server := "server-" + uuid.New().String()
	username := "user-" + uuid.New().String()
	password := "pass-" + uuid.New().String()
	authConfig := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", username, password)))

	tt := []struct {
		name        string
		authConfig  AuthConfig
		shouldFail  bool
		failMessage string
	}{
		{
			"Valid username and password",
			AuthConfig{Username: username, Password: password},
			false,
			"",
		},
		{
			"Username and password in auth",
			AuthConfig{Auth: authConfig},
			false,
			"",
		},
		{
			"No Username",
			AuthConfig{},
			true,
			"no username present in auth config for server",
		},
		{
			"Invalid Auth",
			AuthConfig{Auth: "123"},
			true,
			"error decoding the auth for server",
		},
		{
			"Malformed Auth",
			AuthConfig{Auth: base64.StdEncoding.EncodeToString([]byte("123"))},
			true,
			"malformed auth for server",
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			cred, err := makeRegistryCredential(server, tc.authConfig)

			if tc.shouldFail {
				assert.Check(t, err != nil, "conversion should fail")
				assert.Check(t, strings.Contains(err.Error(), tc.failMessage), "failed message is not expected")
				return
			}

			assert.Check(t, err, "conversion should not fail")
			assert.Check(t, cred != nil, "credential should not be nil")
			assert.Check(t, is.Equal(server, *cred.Server), "server doesn't match")
			assert.Check(t, is.Equal(username, *cred.Username), "username doesn't match")
			assert.Check(t, is.Equal(password, *cred.Password), "password doesn't match")
		})
	}
}

// Test make registry credential from docker config
func TestMakeRegistryCredentialFromDockerConfig(t *testing.T) {
	server := "server-" + uuid.New().String()
	username := "user-" + uuid.New().String()
	password := "pass-" + uuid.New().String()
	authConfig := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", username, password)))

	tt := []struct {
		name        string
		authConfig  dockercfg.AuthConfig
		shouldFail  bool
		failMessage string
	}{
		{
			"Valid username and password",
			dockercfg.AuthConfig{Username: username, Password: password},
			false,
			"",
		},
		{
			"Username and password can be decoded from authConfig",
			dockercfg.AuthConfig{Username: username, Auth: authConfig},
			false,
			"",
		},
		{
			"No Username",
			dockercfg.AuthConfig{},
			true,
			"no username present in auth config for server",
		},
		{
			"Invalid Auth",
			dockercfg.AuthConfig{Username: username, Auth: base64.StdEncoding.EncodeToString([]byte("123"))},
			true,
			"error decoding docker auth",
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			cred, err := makeRegistryCredentialFromDockerConfig(server, tc.authConfig)

			if tc.shouldFail {
				assert.Check(t, err != nil, "conversion should fail")
				assert.Check(t, strings.Contains(err.Error(), tc.failMessage), "failed message is not expected")
				return
			}

			assert.Check(t, err, "conversion should not fail")
			assert.Check(t, cred != nil, "credential should not be nil")
			assert.Check(t, is.Equal(server, *cred.Server), "server doesn't match")
			assert.Check(t, is.Equal(username, *cred.Username), "username doesn't match")
			assert.Check(t, is.Equal(password, *cred.Password), "password doesn't match")
		})
	}
}

// Tests create pod without resource spec
func TestCreatePodWithoutResourceSpec(t *testing.T) {
	podName := "pod-" + uuid.New().String()
	podNamespace := "ns-" + uuid.New().String()
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	aciMocks := createNewACIMock()

	aciMocks.MockCreateContainerGroup = func(ctx context.Context, resourceGroup, podNS, podName string, cg *azaciv2.ContainerGroup) error {
		containers := cg.Properties.Containers
		assert.Check(t, cg != nil, "Container group is nil")
		assert.Check(t, containers != nil, "Containers should not be nil")
		assert.Check(t, is.Equal(1, len(containers)), "1 Container is expected")
		assert.Check(t, is.Equal("nginx", *(containers[0]).Name), "Container nginx is expected")
		assert.Check(t, containers[0].Properties.Resources.Requests != nil, "Container resource requests should not be nil")
		assert.Check(t, is.Equal(1.0, *(containers[0]).Properties.Resources.Requests.CPU), "Request CPU is not expected")
		assert.Check(t, is.Equal(1.5, *(containers[0]).Properties.Resources.Requests.MemoryInGB), "Request Memory is not expected")
		assert.Check(t, is.Nil((containers[0]).Properties.Resources.Limits), "Limits should be nil")

		return nil
	}
	provider, err := createTestProvider(aciMocks, NewMockConfigMapLister(mockCtrl),
		NewMockSecretLister(mockCtrl), NewMockPodLister(mockCtrl))
	if err != nil {
		t.Fatal("failed to create the test provider", err)
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: podNamespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "nginx",
				},
			},
		},
	}

	if err := provider.CreatePod(context.Background(), pod); err != nil {
		t.Fatal("failed to create pod", err)
	}
}

// Tests create pod with Windows as the OS
func TestCreatePodWithWindowsOS(t *testing.T) {
	podName := "pod-" + uuid.New().String()
	podNamespace := "ns-" + uuid.New().String()
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	aciMocks := createNewACIMock()

	err := os.Setenv("PROVIDER_OPERATING_SYSTEM", "Windows")
	if err != nil {
		t.Error(err)
	}

	aciMocks.MockCreateContainerGroup = func(ctx context.Context, resourceGroup, podNS, podName string, cg *azaciv2.ContainerGroup) error {
		containers := cg.Properties.Containers
		assert.Check(t, cg != nil, "Container group is nil")
		assert.Check(t, containers != nil, "Containers should not be nil")
		assert.Check(t, is.Equal(1, len(containers)), "1 Container is expected")
		assert.Check(t, is.Equal("nginx", *(containers[0]).Name), "Container nginx is expected")
		assert.Check(t, containers[0].Properties.Resources.Requests != nil, "Container resource requests should not be nil")
		assert.Check(t, is.Nil((containers[0]).Properties.Resources.Limits), "Limits should be nil")

		return nil
	}
	provider, err := createTestProvider(aciMocks, NewMockConfigMapLister(mockCtrl),
		NewMockSecretLister(mockCtrl), NewMockPodLister(mockCtrl))
	if err != nil {
		t.Fatal("failed to create the test provider", err)
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: podNamespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "nginx",
				},
			},
		},
	}

	if err := provider.CreatePod(context.Background(), pod); err != nil {
		t.Fatal("failed to create pod", err)
	}
}

// Tests create pod with resource request only
func TestCreatePodWithResourceRequestOnly(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	aciMocks := createNewACIMock()
	aciMocks.MockCreateContainerGroup = func(ctx context.Context, resourceGroup, podNS, podName string, cg *azaciv2.ContainerGroup) error {
		containers := cg.Properties.Containers
		assert.Check(t, cg != nil, "container group is nil")
		assert.Check(t, containers != nil, "container should not be nil")
		assert.Check(t, is.Equal(1, len(containers)), "only container is expected")
		assert.Check(t, is.Equal("nginx", *(containers[0]).Name), "Container nginx is expected")
		assert.Check(t, containers[0].Properties.Resources.Requests != nil, "Container resource requests should not be nil")
		assert.Check(t, is.Equal(1.98, *(containers[0]).Properties.Resources.Requests.CPU), "Request CPU is not expected")
		assert.Check(t, is.Equal(3.4, *(containers[0]).Properties.Resources.Requests.MemoryInGB), "Request Memory is not expected")
		assert.Check(t, is.Nil(containers[0].Properties.Resources.Limits), "Limits should be nil")

		return nil
	}

	podName := "pod-" + uuid.New().String()
	podNamespace := "ns-" + uuid.New().String()
	ctx := context.Background()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: podNamespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "nginx",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							"cpu":    resource.MustParse("1.981"),
							"memory": resource.MustParse("3.49G"),
						},
					},
				},
			},
		},
	}

	provider, err := createTestProvider(aciMocks, NewMockConfigMapLister(mockCtrl),
		NewMockSecretLister(mockCtrl), NewMockPodLister(mockCtrl))
	if err != nil {
		t.Fatal("failed to create the test provider", err)
	}

	if err := provider.CreatePod(ctx, pod); err != nil {
		t.Fatal("Failed to create pod", err)
	}
}

// Tests create pod with default GPU SKU.
func TestCreatePodWithGPU(t *testing.T) {
	t.Skip("Skipping GPU tests until Location API is fixed")
	podName := "pod-" + uuid.New().String()
	podNamespace := "ns-" + uuid.New().String()
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	aciMocks := createNewACIMock()

	aciMocks.MockCreateContainerGroup = func(ctx context.Context, resourceGroup, podNS, podName string, cg *azaciv2.ContainerGroup) error {
		containers := cg.Properties.Containers
		assert.Check(t, containers != nil, "Containers should not be nil")
		assert.Check(t, is.Equal(1, len(containers)), "1 Container is expected")
		assert.Check(t, is.Equal("nginx", *(containers[0]).Name), "Container nginx is expected")
		assert.Check(t, (containers[0]).Properties.Resources.Requests != nil, "Container resource requests should not be nil")
		assert.Check(t, is.Equal(1.98, *(containers[0]).Properties.Resources.Requests.CPU), "Request CPU is not expected")
		assert.Check(t, is.Equal(3.4, *(containers[0]).Properties.Resources.Requests.MemoryInGB), "Request Memory is not expected")
		assert.Check(t, (containers[0]).Properties.Resources.Requests.Gpu != nil, "Requests GPU is not expected")
		assert.Check(t, is.Equal(int32(10), *(containers[0]).Properties.Resources.Requests.Gpu.Count), "Requests GPU Count is not expected")
		return nil
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: podNamespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "nginx",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							"cpu":    resource.MustParse("1.981"),
							"memory": resource.MustParse("3.49G"),
						},
						Limits: corev1.ResourceList{
							gpuResourceName: resource.MustParse("10"),
						},
					},
				},
			},
		},
	}

	provider, err := createTestProvider(aciMocks, NewMockConfigMapLister(mockCtrl),
		NewMockSecretLister(mockCtrl), NewMockPodLister(mockCtrl))
	if err != nil {
		t.Fatal("failed to create the test provider", err)
	}

	if err := provider.CreatePod(context.Background(), pod); err != nil {
		t.Fatal("Failed to create pod", err)
	}
}

// Tests create pod with GPU SKU in annotation.
func TestCreatePodWithGPUSKU(t *testing.T) {
	t.Skip("Skipping GPU tests until Location API is fixed")

	podName := "pod-" + uuid.New().String()
	podNamespace := "ns-" + uuid.New().String()
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	aciMocks := createNewACIMock()
	aciMocks.MockCreateContainerGroup = func(ctx context.Context, resourceGroup, podNS, podName string, cg *azaciv2.ContainerGroup) error {
		containers := cg.Properties.Containers
		assert.Check(t, cg != nil, "Container group is nil")
		assert.Check(t, containers != nil, "Containers should not be nil")
		assert.Check(t, is.Equal(1, len(containers)), "1 Container is expected")
		assert.Check(t, is.Equal("nginx", *(containers[0]).Name), "Container nginx is expected")
		assert.Check(t, (containers[0]).Properties.Resources.Requests != nil, "Container resource requests should not be nil")
		assert.Check(t, is.Equal(1.98, *(containers[0]).Properties.Resources.Requests.CPU), "Request CPU is not expected")
		assert.Check(t, is.Equal(3.4, *(containers[0]).Properties.Resources.Requests.MemoryInGB), "Request Memory is not expected")
		assert.Check(t, (containers[0]).Properties.Resources.Requests.Gpu != nil, "Requests GPU is not expected")
		assert.Check(t, is.Equal(int32(1), *(containers[0]).Properties.Resources.Requests.Gpu.Count), "Requests GPU Count is not expected")
		assert.Check(t, is.Equal(gpuSKU, (containers[0]).Properties.Resources.Requests.Gpu.SKU), "Requests GPU SKU is not expected")
		assert.Check(t, (containers[0]).Properties.Resources.Limits.Gpu != nil, "Limits GPU is not expected")

		return nil
	}

	provider, err := createTestProvider(aciMocks, NewMockConfigMapLister(mockCtrl),
		NewMockSecretLister(mockCtrl), NewMockPodLister(mockCtrl))
	if err != nil {
		t.Fatal("failed to create the test provider", err)
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: podNamespace,
			Annotations: map[string]string{
				gpuTypeAnnotation: string(gpuSKU),
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "nginx",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							"cpu":    resource.MustParse("1.981"),
							"memory": resource.MustParse("3.49G"),
						},
						Limits: corev1.ResourceList{
							gpuResourceName: resource.MustParse("1"),
						},
					},
				},
			},
		},
	}

	if err := provider.CreatePod(context.Background(), pod); err != nil {
		t.Fatal("Failed to create pod", err)
	}
}

// Tests create pod with both resource request and limit.
func TestCreatePodWithResourceRequestAndLimit(t *testing.T) {
	podName := "pod-" + uuid.New().String()
	podNamespace := "ns-" + uuid.New().String()
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	aciMocks := createNewACIMock()

	aciMocks.MockCreateContainerGroup = func(ctx context.Context, resourceGroup, podNS, podName string, cg *azaciv2.ContainerGroup) error {
		containers := cg.Properties.Containers
		assert.Check(t, cg != nil, "Container group is nil")
		assert.Check(t, containers != nil, "Containers should not be nil")
		assert.Check(t, is.Equal(1, len(containers)), "1 Container is expected")
		assert.Check(t, is.Equal("nginx", *(containers[0]).Name), "Container nginx is expected")
		assert.Check(t, (containers[0]).Properties.Resources.Requests != nil, "Container resource requests should not be nil")
		assert.Check(t, is.Equal(0.99, *(containers[0]).Properties.Resources.Requests.CPU), "Request CPU is not expected")
		assert.Check(t, is.Equal(1.5, *(containers[0]).Properties.Resources.Requests.MemoryInGB), "Request Memory is not expected")
		assert.Check(t, is.Equal(3.999, *(containers[0]).Properties.Resources.Limits.CPU), "Limit CPU is not expected")
		assert.Check(t, is.Equal(8.0, *(containers[0]).Properties.Resources.Limits.MemoryInGB), "Limit Memory is not expected")

		return nil
	}

	pod := testsutil.CreatePodObj(podName, podNamespace)

	provider, err := createTestProvider(aciMocks, NewMockConfigMapLister(mockCtrl),
		NewMockSecretLister(mockCtrl), NewMockPodLister(mockCtrl))
	if err != nil {
		t.Fatal("failed to create the test provider", err)
	}

	if err := provider.CreatePod(context.Background(), pod); err != nil {
		t.Fatal("Failed to create pod", err)
	}
}

// Tests get pods with empty list.
func TestGetPodsWithEmptyList(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	aciMocks := createNewACIMock()

	aciMocks.MockGetContainerGroupList = func(ctx context.Context, resourceGroup string) ([]*azaciv2.ContainerGroup, error) {
		var result []*azaciv2.ContainerGroup
		return result, nil
	}

	provider, err := createTestProvider(aciMocks, NewMockConfigMapLister(mockCtrl),
		NewMockSecretLister(mockCtrl), NewMockPodLister(mockCtrl))
	if err != nil {
		t.Fatal("failed to create the test provider", err)
	}

	pods, err := provider.GetPods(context.Background())
	if err != nil {
		t.Fatal("Failed to get pods", err)
	}

	assert.Check(t, is.Equal(0, len(pods)), "No pod should be returned")
}

// Tests get pods without requests limit.
func TestGetPodsWithoutResourceRequestsLimits(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	aciMocks := createNewACIMock()

	aciMocks.MockGetContainerGroupList = func(ctx context.Context, resourceGroup string) ([]*azaciv2.ContainerGroup, error) {
		cgName := "default-nginx"
		node := fakeNodeName
		provisioning := "Creating"
		var cg = &azaciv2.ContainerGroup{
			ID:   &cgName,
			Name: &cgName,
			Tags: map[string]*string{
				"CreationTimestamp": &creationTime,
				"PodName":           &cgName,
				"Namespace":         &cgName,
				"NodeName":          &node,
				"UID":               &cgName,
			},
			Properties: &azaciv2.ContainerGroupPropertiesProperties{
				ProvisioningState: &provisioning,
				Containers:        testsutil.CreateACIContainersListObj(runningState, "Initializing", testsutil.CgCreationTime.Add(time.Second*2), testsutil.CgCreationTime.Add(time.Second*3), true, false, false),
				InstanceView: &azaciv2.ContainerGroupPropertiesInstanceView{
					State: &runningState,
				},
			},
		}
		var result []*azaciv2.ContainerGroup
		result = append(result, cg)
		return result, nil
	}
	aciMocks.MockGetContainerGroupInfo =
		func(ctx context.Context, resourceGroup, namespace, name, nodeName string) (*azaciv2.ContainerGroup, error) {
			node := fakeNodeName
			provisioning := "Creating"
			return &azaciv2.ContainerGroup{
				ID:   &cgName,
				Name: &cgName,
				Tags: map[string]*string{
					"CreationTimestamp": &creationTime,
					"PodName":           &cgName,
					"Namespace":         &cgName,
					"NodeName":          &node,
					"UID":               &cgName,
				},
				Properties: &azaciv2.ContainerGroupPropertiesProperties{
					ProvisioningState: &provisioning,
					Containers:        testsutil.CreateACIContainersListObj(runningState, "Initializing", testsutil.CgCreationTime.Add(time.Second*2), testsutil.CgCreationTime.Add(time.Second*3), true, false, false),
					InstanceView: &azaciv2.ContainerGroupPropertiesInstanceView{
						State: &runningState,
					},
				},
			}, nil
		}

	provider, err := createTestProvider(aciMocks, NewMockConfigMapLister(mockCtrl),
		NewMockSecretLister(mockCtrl), NewMockPodLister(mockCtrl))
	if err != nil {
		t.Fatal("failed to create the test provider", err)
	}

	pods, err := provider.GetPods(context.Background())
	if err != nil {
		t.Fatal("Failed to get pods", err)
	}

	assert.Check(t, pods != nil, "Response pods should not be nil")
	assert.Check(t, is.Equal(0, len(pods)), "No pod should be returned")

}

// Tests get pod without requests limit.
func TestGetPodWithoutResourceRequestsLimits(t *testing.T) {
	podName := "pod-" + uuid.New().String()
	podNamespace := "ns-" + uuid.New().String()
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	podLister := NewMockPodLister(mockCtrl)

	mockPodsNamespaceLister := NewMockPodNamespaceLister(mockCtrl)
	podLister.EXPECT().Pods(podNamespace).Return(mockPodsNamespaceLister)
	mockPodsNamespaceLister.EXPECT().Get(podName).
		Return(testsutil.CreatePodObj(podName, podNamespace), nil)

	aciMocks := createNewACIMock()
	aciMocks.MockGetContainerGroupInfo =
		func(ctx context.Context, resourceGroup, namespace, name, nodeName string) (*azaciv2.ContainerGroup, error) {
			return testsutil.CreateContainerGroupObj(podName, podNamespace, "Succeeded",
				testsutil.CreateACIContainersListObj(runningState, "Initializing",
					testsutil.CgCreationTime.Add(time.Second*2),
					testsutil.CgCreationTime.Add(time.Second*3),
					true, true, true), "Succeeded"), nil
		}

	aciMocks.MockGetContainerGroupList = func(ctx context.Context, resourceGroup string) ([]*azaciv2.ContainerGroup, error) {
		cg := testsutil.CreateContainerGroupObj(podName, podNamespace, "Succeeded",
			testsutil.CreateACIContainersListObj(runningState, "Initializing",
				testsutil.CgCreationTime.Add(time.Second*2),
				testsutil.CgCreationTime.Add(time.Second*3),
				false, false, false), "Succeeded")

		var result []*azaciv2.ContainerGroup
		result = append(result, cg)
		return result, nil
	}
	provider, err := createTestProvider(aciMocks, NewMockConfigMapLister(mockCtrl),
		NewMockSecretLister(mockCtrl), podLister)
	if err != nil {
		t.Fatal("failed to create the test provider", err)
	}

	pod, err := provider.GetPod(context.Background(), podNamespace, podName)
	if err != nil {
		t.Fatal("Failed to get pod", err)
	}

	assert.Equal(t, ptrQuantity(resource.MustParse("0.99")).Value(), pod.Spec.Containers[0].Resources.Requests.Cpu().Value(), "Containers[0].Properties.Resources.Requests.CPU doesn't match")
	assert.Equal(t, ptrQuantity(resource.MustParse("1.5G")).Value(), pod.Spec.Containers[0].Resources.Requests.Memory().Value(), "Containers[0].Properties.Resources.Requests.Memory doesn't match")
}

func TestPodToACISecretEnvVar(t *testing.T) {

	testKey := "testVar"
	testVal := "testVal"

	e := corev1.EnvVar{
		Name:  testKey,
		Value: testVal,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{},
		},
	}
	aciEnvVar := getACIEnvVar(e)

	if aciEnvVar.Value != nil {
		t.Fatalf("ACI Env Variable Value should be empty for a secret")
	}

	if *aciEnvVar.Name != testKey {
		t.Fatalf("ACI Env Variable Name does not match expected Name")
	}

	if *aciEnvVar.SecureValue != testVal {
		t.Fatalf("ACI Env Variable Secure Value does not match expected value")
	}
}

func TestPodToACIEnvVar(t *testing.T) {

	testKey := "testVar"
	testVal := "testVal"

	e := corev1.EnvVar{
		Name:      testKey,
		Value:     testVal,
		ValueFrom: &corev1.EnvVarSource{},
	}
	aciEnvVar := getACIEnvVar(e)

	if aciEnvVar.SecureValue != nil {
		t.Fatalf("ACI Env Variable Secure Value should be empty for non-secret variables")
	}

	if *aciEnvVar.Name != testKey {
		t.Fatalf("ACI Env Variable Name does not match expected Name")
	}

	if *aciEnvVar.Value != testVal {
		t.Fatalf("ACI Env Variable Value does not match expected value")
	}
}

func setAuthConfig() error {
	err := azConfig.SetAuthConfig(context.TODO())
	if err != nil {
		return err
	}
	return nil
}

func createNewACIMock() *MockACIProvider {
	return NewMockACIProvider(func(ctx context.Context, region string) ([]*azaciv2.Capabilities, error) {
		gpu := "P100"
		capability := &azaciv2.Capabilities{
			Location: &region,
			Gpu:      &gpu,
		}
		var result []*azaciv2.Capabilities
		result = append(result, capability)
		return result, nil
	})
}

func createTestProvider(aciMocks *MockACIProvider, configMapMocker *MockConfigMapLister, secretMocker *MockSecretLister, podMocker *MockPodLister) (*ACIProvider, error) {
	ctx := context.TODO()

	err := setAuthConfig()
	if err != nil {
		return nil, err
	}

	err = os.Setenv("ACI_VNET_NAME", fakeVnetName)
	if err != nil {
		return nil, err
	}
	err = os.Setenv("ACI_VNET_RESOURCE_GROUP", fakeResourceGroup)
	if err != nil {
		return nil, err
	}
	err = os.Setenv("ACI_RESOURCE_GROUP", fakeResourceGroup)
	if err != nil {
		return nil, err
	}
	err = os.Setenv("ACI_REGION", fakeRegion)
	if err != nil {
		return nil, err
	}

	cfg := nodeutil.ProviderConfig{
		ConfigMaps: configMapMocker,
		Secrets:    secretMocker,
		Pods:       podMocker,
	}

	cfg.Node = &corev1.Node{}

	operatingSystem, osTypeSet := os.LookupEnv("PROVIDER_OPERATING_SYSTEM")

	if !osTypeSet {
		operatingSystem = "Linux"
	}

	cfg.Node.Name = fakeNodeName
	cfg.Node.Status.NodeInfo.OperatingSystem = operatingSystem

	provider, err := NewACIProvider(ctx, "example.toml", azConfig, aciMocks, cfg, fakeNodeName, operatingSystem, "0.0.0.0", 10250, "cluster.local")
	if err != nil {
		return nil, err
	}

	return provider, nil
}

func ptrQuantity(q resource.Quantity) *resource.Quantity {
	return &q
}

func TestConfigureNode(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "virtual-kubelet",
			Labels: map[string]string{
				"type":                   "virtual-kubelet",
				"kubernetes.io/role":     "agent",
				"kubernetes.io/hostname": "virtual-kubelet",
			},
		},
		Spec: corev1.NodeSpec{},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				Architecture:   "amd64",
				KubeletVersion: "1.26.0",
			},
		},
	}
	aciMocks := createNewACIMock()
	provider, err := createTestProvider(aciMocks, NewMockConfigMapLister(mockCtrl),
		NewMockSecretLister(mockCtrl), NewMockPodLister(mockCtrl))
	if err != nil {
		t.Fatal("failed to create the test provider", err)
	}

	provider.ConfigureNode(context.TODO(), node)
	assert.Equal(t, "true", node.ObjectMeta.Labels["alpha.service-controller.kubernetes.io/exclude-balancer"], "exclude-balancer label doesn't match")
	assert.Equal(t, "true", node.ObjectMeta.Labels["node.kubernetes.io/exclude-from-external-load-balancers"], "exclude-from-external-load-balancers label doesn't match")
	assert.Equal(t, "false", node.ObjectMeta.Labels["kubernetes.azure.com/managed"], "kubernetes.azure.com/managed label doesn't match")
}

func TestCreatePodWithNamedLivenessProbe(t *testing.T) {
	podName := "pod-" + uuid.New().String()
	podNamespace := "ns-" + uuid.New().String()
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	aciMocks := createNewACIMock()

	aciMocks.MockCreateContainerGroup = func(ctx context.Context, resourceGroup, podNS, podName string, cg *azaciv2.ContainerGroup) error {
		containers := cg.Properties.Containers

		assert.Check(t, (containers)[0].Properties.LivenessProbe != nil, "Liveness probe expected")
		assert.Check(t, is.Equal(int32(10), *(containers)[0].Properties.LivenessProbe.InitialDelaySeconds), "Initial Probe Delay doesn't match")
		assert.Check(t, is.Equal(int32(5), *(containers)[0].Properties.LivenessProbe.PeriodSeconds), "Probe Period doesn't match")
		assert.Check(t, is.Equal(int32(60), *(containers)[0].Properties.LivenessProbe.TimeoutSeconds), "Probe Timeout doesn't match")
		assert.Check(t, is.Equal(int32(3), *(containers)[0].Properties.LivenessProbe.SuccessThreshold), "Probe Success Threshold doesn't match")
		assert.Check(t, is.Equal(int32(5), *(containers)[0].Properties.LivenessProbe.FailureThreshold), "Probe Failure Threshold doesn't match")
		assert.Check(t, (cg.Properties.Containers)[0].Properties.LivenessProbe.HTTPGet != nil, "Expected an HTTP Get Probe")
		assert.Check(t, is.Equal(int32(8080), *(containers)[0].Properties.LivenessProbe.HTTPGet.Port), "Expected Port to be 8080")
		return nil
	}

	pod := testsutil.CreatePodObj(podName, podNamespace)

	provider, err := createTestProvider(aciMocks, NewMockConfigMapLister(mockCtrl),
		NewMockSecretLister(mockCtrl), NewMockPodLister(mockCtrl))
	if err != nil {
		t.Fatal("failed to create the test provider", err)
	}

	if err := provider.CreatePod(context.Background(), pod); err != nil {
		t.Fatal("Failed to create pod", err)
	}
}

func TestCreatePodWithLivenessProbe(t *testing.T) {
	podName := "pod-" + uuid.New().String()
	podNamespace := "ns-" + uuid.New().String()
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	aciMocks := createNewACIMock()
	aciMocks.MockCreateContainerGroup = func(ctx context.Context, resourceGroup, podNS, podName string, cg *azaciv2.ContainerGroup) error {
		containers := cg.Properties.Containers
		assert.Check(t, cg != nil, "Container group is nil")
		assert.Check(t, containers != nil, "Containers should not be nil")
		assert.Check(t, is.Equal(1, len(containers)), "1 Container is expected")
		assert.Check(t, is.Equal("nginx", *(containers[0]).Name), "Container nginx is expected")
		assert.Check(t, (containers)[0].Properties.LivenessProbe != nil, "Liveness probe expected")
		assert.Check(t, is.Equal(int32(10), *(containers)[0].Properties.LivenessProbe.InitialDelaySeconds), "Initial Probe Delay doesn't match")
		assert.Check(t, is.Equal(int32(5), *(containers)[0].Properties.LivenessProbe.PeriodSeconds), "Probe Period doesn't match")
		assert.Check(t, is.Equal(int32(60), *(containers)[0].Properties.LivenessProbe.TimeoutSeconds), "Probe Timeout doesn't match")
		assert.Check(t, is.Equal(int32(3), *(containers)[0].Properties.LivenessProbe.SuccessThreshold), "Probe Success Threshold doesn't match")
		assert.Check(t, is.Equal(int32(5), *(containers)[0].Properties.LivenessProbe.FailureThreshold), "Probe Failure Threshold doesn't match")
		assert.Check(t, (containers)[0].Properties.LivenessProbe.HTTPGet != nil, "Expected an HTTP Get Probe")

		return nil
	}

	pod := testsutil.CreatePodObj(podName, podNamespace)

	provider, err := createTestProvider(aciMocks, NewMockConfigMapLister(mockCtrl),
		NewMockSecretLister(mockCtrl), NewMockPodLister(mockCtrl))
	if err != nil {
		t.Fatal("failed to create the test provider", err)
	}

	if err := provider.CreatePod(context.Background(), pod); err != nil {
		t.Fatal("Failed to create pod", err)
	}
}

func TestGetProbe(t *testing.T) {
	cases := []struct {
		description     string
		podProbe        *corev1.Probe
		podPorts        []corev1.ContainerPort
		expectedCGProbe *azaciv2.ContainerProbe
		expectedError   error
	}{
		{
			description:     "has_no_probe",
			podProbe:        testsutil.CreatePodProbeObj(false, false),
			podPorts:        nil,
			expectedCGProbe: nil,
			expectedError:   fmt.Errorf("probe must specify one of \"exec\" and \"httpGet\""),
		}, {
			description:     "has_httpGet_and_exec",
			podProbe:        testsutil.CreatePodProbeObj(true, true),
			podPorts:        nil,
			expectedCGProbe: nil,
			expectedError:   fmt.Errorf("probe may not specify more than one of \"exec\" and \"httpGet\""),
		}, {
			description:     "has_httpGet_wrong_port_info",
			podProbe:        testsutil.CreatePodProbeObj(true, false),
			podPorts:        testsutil.CreateContainerPortObj("https", 8888),
			expectedCGProbe: nil,
			expectedError:   fmt.Errorf("unable to find named port: %s", "http"),
		}, {
			description:     "has_exec_with_port_info",
			podProbe:        testsutil.CreatePodProbeObj(false, true),
			podPorts:        testsutil.CreateContainerPortObj("http", 8080),
			expectedCGProbe: testsutil.CreateCGProbeObj(false, true),
			expectedError:   nil,
		},
		{
			description:     "has_exec_without_port_info",
			podProbe:        testsutil.CreatePodProbeObj(false, true),
			podPorts:        nil,
			expectedCGProbe: testsutil.CreateCGProbeObj(false, true),
			expectedError:   nil,
		},
		{
			description:     "has_httpGet_with_port_info",
			podProbe:        testsutil.CreatePodProbeObj(true, false),
			podPorts:        testsutil.CreateContainerPortObj("http", 8080),
			expectedCGProbe: testsutil.CreateCGProbeObj(true, false),
			expectedError:   nil,
		},
		{
			description:     "has_httpGet_without_port_info",
			podProbe:        testsutil.CreatePodProbeObj(true, false),
			podPorts:        nil,
			expectedCGProbe: nil,
			expectedError:   fmt.Errorf("unable to find named port: %s", "http"),
		},
		{
			description:     "has_httpGet_with_wrong_port_info",
			podProbe:        testsutil.CreatePodProbeObj(true, false),
			podPorts:        testsutil.CreateContainerPortObj("https", 8080),
			expectedCGProbe: nil,
			expectedError:   fmt.Errorf("unable to find named port: %s", "http"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.description, func(t *testing.T) {

			cgProbe, err := getProbe(tc.podProbe, tc.podPorts)

			if tc.expectedCGProbe != nil {
				assert.DeepEqual(t, tc.expectedCGProbe, cgProbe)
			}
			if tc.expectedError == nil {
				assert.NilError(t, tc.expectedError, err)
			} else {
				assert.Equal(t, tc.expectedError.Error(), err.Error())
			}
		})
	}
}

func TestCreatePodWithReadinessProbe(t *testing.T) {
	podName := "pod-" + uuid.New().String()
	podNamespace := "ns-" + uuid.New().String()
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	aciMocks := createNewACIMock()

	aciMocks.MockCreateContainerGroup = func(ctx context.Context, resourceGroup, podNS, podName string, cg *azaciv2.ContainerGroup) error {
		containers := cg.Properties.Containers
		assert.Check(t, cg != nil, "Container group is nil")
		assert.Check(t, containers != nil, "Containers should not be nil")
		assert.Check(t, is.Equal(1, len(containers)), "1 Container is expected")
		assert.Check(t, is.Equal("nginx", *(containers[0]).Name), "Container nginx is expected")
		assert.Check(t, (containers)[0].Properties.ReadinessProbe != nil, "Readiness probe expected")
		assert.Check(t, is.Equal(int32(10), *(containers)[0].Properties.ReadinessProbe.InitialDelaySeconds), "Initial Probe Delay doesn't match")
		assert.Check(t, is.Equal(int32(5), *(containers)[0].Properties.ReadinessProbe.PeriodSeconds), "Probe Period doesn't match")
		assert.Check(t, is.Equal(int32(60), *(containers)[0].Properties.ReadinessProbe.TimeoutSeconds), "Probe Timeout doesn't match")
		assert.Check(t, is.Equal(int32(3), *(containers)[0].Properties.ReadinessProbe.SuccessThreshold), "Probe Success Threshold doesn't match")
		assert.Check(t, is.Equal(int32(5), *(containers)[0].Properties.ReadinessProbe.FailureThreshold), "Probe Failure Threshold doesn't match")
		assert.Check(t, (containers)[0].Properties.ReadinessProbe.HTTPGet != nil, "Expected an HTTP Get Probe")

		return nil
	}

	pod := testsutil.CreatePodObj(podName, podNamespace)

	provider, err := createTestProvider(aciMocks, NewMockConfigMapLister(mockCtrl),
		NewMockSecretLister(mockCtrl), NewMockPodLister(mockCtrl))
	if err != nil {
		t.Fatal("failed to create the test provider", err)
	}

	if err := provider.CreatePod(context.Background(), pod); err != nil {
		t.Fatal("Failed to create pod", err)
	}
}

func TestCreatedPodWithContainerPort(t *testing.T) {
	port4040 := int32(4040)
	port5050 := int32(5050)
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cases := []struct {
		description   string
		containerList []corev1.Container
	}{
		{
			description: "Container with port and other without port",
			containerList: []corev1.Container{
				{
					Name: "container1",
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: port5050,
						},
					},
				},
				{
					Name: "container2",
				},
			},
		},
		{
			description: "Two containers with multiple same ports",
			containerList: []corev1.Container{
				{
					Name: "container1",
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: 80,
						},
						{
							ContainerPort: 443,
						},
					},
				},
				{
					Name: "container2",
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: port4040,
						},
					},
				},
			},
		},
		{
			description: "Two containers with different ports",
			containerList: []corev1.Container{
				{
					Name: "container1",
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: 5050,
						},
					},
				},
				{
					Name: "container2",
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: port4040,
						},
					},
				},
			},
		},
		{
			description: "Two containers with the same port",
			containerList: []corev1.Container{
				{
					Name: "container1",
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: 5050,
						},
					},
				},
				{
					Name: "container2",
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: 5050,
						},
					},
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.description, func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podName,
					Namespace: podNamespace,
				},
				Spec: corev1.PodSpec{},
			}
			pod.Spec.Containers = tc.containerList

			aciMocks := createNewACIMock()
			aciMocks.MockCreateContainerGroup = func(ctx context.Context, resourceGroup, podNS, podName string, cg *azaciv2.ContainerGroup) error {
				containers := cg.Properties.Containers
				container1Ports := containers[0].Properties.Ports
				container2Ports := containers[1].Properties.Ports
				assert.Check(t, cg != nil, "Container group is nil")
				assert.Check(t, containers != nil, "Containers should not be nil")
				assert.Check(t, is.Equal(2, len(containers)), "2 Containers is expected")
				assert.Check(t, is.Equal(len(container1Ports), len(tc.containerList[0].Ports)))
				assert.Check(t, is.Equal(len(container2Ports), len(tc.containerList[1].Ports)))
				for i := range tc.containerList[0].Ports {
					assert.Equal(t, tc.containerList[0].Ports[i].ContainerPort, *(container1Ports[i]).Port, "Container ports are not equal")
				}
				for i := range tc.containerList[1].Ports {
					assert.Equal(t, tc.containerList[0].Ports[i].ContainerPort, *(container1Ports[i]).Port, "Container ports are not equal")
				}
				return nil
			}

			provider, err := createTestProvider(aciMocks, NewMockConfigMapLister(mockCtrl),
				NewMockSecretLister(mockCtrl), NewMockPodLister(mockCtrl))
			if err != nil {
				t.Fatal("Unable to create test provider", err)
			}

			err = provider.CreatePod(context.Background(), pod)
			assert.Check(t, err == nil, "Not expected to return error")
		})
	}
}

func TestGetPodWithContainerID(t *testing.T) {
	podName := "pod-" + uuid.New().String()
	podNamespace := "ns-" + uuid.New().String()
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	podLister := NewMockPodLister(mockCtrl)

	mockPodsNamespaceLister := NewMockPodNamespaceLister(mockCtrl)
	podLister.EXPECT().Pods(podNamespace).Return(mockPodsNamespaceLister)
	mockPodsNamespaceLister.EXPECT().Get(podName).
		Return(testsutil.CreatePodObj(podName, podNamespace), nil)

	err := azConfig.SetAuthConfig(context.TODO())
	if err != nil {
		t.Fatal("failed to get auth configuration", err)
	}

	aciMocks := createNewACIMock()
	cgID := ""
	aciMocks.MockGetContainerGroupInfo = func(ctx context.Context, resourceGroup, namespace, name, nodeName string) (*azaciv2.ContainerGroup, error) {

		cg := testsutil.CreateContainerGroupObj(podName, podNamespace, "Succeeded",
			testsutil.CreateACIContainersListObj(runningState, "Initializing", testsutil.CgCreationTime.Add(time.Second*2), testsutil.CgCreationTime.Add(time.Second*3), true, true, true), "Succeeded")
		cgID = *cg.ID
		return cg, nil
	}

	provider, err := createTestProvider(aciMocks, NewMockConfigMapLister(mockCtrl),
		NewMockSecretLister(mockCtrl), podLister)
	if err != nil {
		t.Fatal("failed to create the test provider", err)
	}

	pod, err := provider.GetPod(context.Background(), podNamespace, podName)
	if err != nil {
		t.Fatal("Failed to get pod", err)
	}

	assert.Check(t, &pod != nil, "Response pod should not be nil")
	assert.Check(t, is.Equal(1, len(pod.Status.ContainerStatuses)), "1 container status is expected")
	assert.Check(t, is.Equal(testsutil.TestContainerName, pod.Status.ContainerStatuses[0].Name), "Container name in the container status doesn't match")
	assert.Check(t, is.Equal(testsutil.TestImageNginx, pod.Status.ContainerStatuses[0].Image), "Container image in the container status doesn't match")
	assert.Check(t, is.Equal(util.GetContainerID(&cgID, &testsutil.TestContainerName), pod.Status.ContainerStatuses[0].ContainerID), "Container ID in the container status is not expected")
}

func TestFilterWindowsServiceAccountSecretVolume(t *testing.T) {
	cgName := "pod-" + uuid.New().String()
	cgNamespace := "ns-" + uuid.New().String()
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	volName := "fakeVolume"
	volMountName1 := "fakeVolumeMount1"
	volMountPath1 := "/mnt/azure"
	volMountName2 := "fakeVolumeMount2"
	serviceAccountSecretMountPath := "/var/run/secrets/kubernetes.io/serviceaccount"

	fakeVolumes := []*azaciv2.Volume{
		{
			Name:     &volName,
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
		{
			Name:     &volMountName2,
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		}}
	nonServiceAccountSecretVolumeMount := []*azaciv2.VolumeMount{
		{
			Name:      &volMountName1,
			MountPath: &volMountPath1,
		}}
	serviceAccountSecretVolumeMount := []*azaciv2.VolumeMount{
		{
			Name:      &volMountName2,
			MountPath: &serviceAccountSecretMountPath,
		}}

	cases := []struct {
		description  string
		os           string
		containers   []*azaciv2.Container
		shouldFilter bool
	}{
		{
			description: "Container without service account secret mount path",
			os:          "Windows",
			containers: []*azaciv2.Container{
				{
					Name: &volMountName1,
					Properties: &azaciv2.ContainerProperties{
						VolumeMounts: nonServiceAccountSecretVolumeMount,
					},
				},
			},
			shouldFilter: false,
		},
		{
			description: "Container with service account secret mount path",
			os:          "Windows",
			containers: []*azaciv2.Container{
				{
					Name: &volMountName2,
					Properties: &azaciv2.ContainerProperties{
						VolumeMounts: serviceAccountSecretVolumeMount,
					},
				},
			},
			shouldFilter: true,
		},
		{
			description: "Container with service account secret mount path but os is not windows",
			os:          "Linux",
			containers: []*azaciv2.Container{
				{
					Name: &volMountName2,
					Properties: &azaciv2.ContainerProperties{
						VolumeMounts: serviceAccountSecretVolumeMount,
					},
				},
			},
			shouldFilter: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.description, func(t *testing.T) {
			cg := testsutil.CreateContainerGroupObj(cgName, cgNamespace, "Succeeded", tc.containers, "Succeeded")
			cg.Properties.Volumes = fakeVolumes

			assert.Check(t, cg != nil, "Container group is not nil")
			assert.Check(t, cg.Properties.Containers != nil, "Containers should not be nil")
			assert.Check(t, is.Equal(1, len(cg.Properties.Containers)), "1 Container is expected")

			filterWindowsServiceAccountSecretVolume(context.Background(), tc.os, cg)

			if tc.shouldFilter {
				assert.Check(t, is.Equal(0, len(cg.Properties.Containers[0].Properties.VolumeMounts)), "should filter out volume mounts with service account secret volume name")
				assert.Check(t, is.Equal(1, len(cg.Properties.Volumes)), "should filter out volume with service account secret volume name")
			} else {
				assert.Check(t, is.Equal(1, len(cg.Properties.Containers[0].Properties.VolumeMounts)), "volume mount should remain the same")
				assert.Check(t, is.Equal(2, len(cg.Properties.Volumes)), "volume should remain the same")
			}
		})
	}
}

func TestDeleteContainerGroup(t *testing.T) {
	podName1 := "pod-" + uuid.New().String()
	podName2 := "pod-" + uuid.New().String()
	podNamespace := "ns-" + uuid.New().String()

	podNames := []string{podName1, podName2}
	fakePods := testsutil.CreatePodsList(podNames, podNamespace)

	cases := []struct {
		description           string
		podName               string
		cgDeleteExpectedError error
		hasValidPodsTracker   bool
	}{
		{
			description:           "successfully deletes container group and updates pod status",
			podName:               podName1,
			cgDeleteExpectedError: nil,
			hasValidPodsTracker:   true,
		},
		{
			description:           "successfully deletes container group but fails to update pod status",
			podName:               "fakePod",
			cgDeleteExpectedError: nil,
			hasValidPodsTracker:   false,
		},
		{
			description:           "fails to delete container group",
			podName:               podName2,
			cgDeleteExpectedError: errors.New("failed to delete container group"),
			hasValidPodsTracker:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.description, func(t *testing.T) {
			mockCtrl := gomock.NewController(t)
			defer mockCtrl.Finish()

			aciMocks := createNewACIMock()
			podLister := NewMockPodLister(mockCtrl)

			aciMocks.MockDeleteContainerGroup = func(ctx context.Context, resourceGroup, cgName string) error {
				return tc.cgDeleteExpectedError
			}

			provider, err := createTestProvider(aciMocks, NewMockConfigMapLister(mockCtrl),
				NewMockSecretLister(mockCtrl), podLister)
			if err != nil {
				t.Fatal("failed to create the test provider", err)
			}

			if tc.hasValidPodsTracker {
				podsTracker := &PodsTracker{
					pods: podLister,
					updateCb: func(updatedPod *corev1.Pod) {
						for index, pod := range fakePods {
							if updatedPod.Name == pod.Name && updatedPod.Namespace == pod.Namespace {
								fakePods[index] = updatedPod
								break
							}
						}
					},
				}
				podLister.EXPECT().List(gomock.Any()).Return(fakePods, nil)

				provider.tracker = podsTracker
			}

			err = provider.deleteContainerGroup(context.Background(), podNamespace, tc.podName)

			if tc.cgDeleteExpectedError == nil {
				assert.NilError(t, tc.cgDeleteExpectedError, err)
			} else {
				assert.Equal(t, tc.cgDeleteExpectedError.Error(), err.Error())
			}

			for _, pod := range fakePods {
				if pod.Name == tc.podName {
					for i := range pod.Status.ContainerStatuses {
						if tc.hasValidPodsTracker && tc.cgDeleteExpectedError == nil {
							assert.Check(t, pod.Status.ContainerStatuses[i].State.Terminated != nil, "Container should be terminated")
							assert.Check(t, is.Nil((pod.Status.ContainerStatuses[i].State.Running)), "Container should not be running")
							assert.Check(t, is.Equal((pod.Status.ContainerStatuses[i].State.Terminated.ExitCode), containerExitCodePodDeleted), "Status exit code should be set to pod deleted")
							assert.Check(t, is.Equal((pod.Status.ContainerStatuses[i].State.Terminated.Reason), statusReasonPodDeleted), "Status reason should be set to pod deleted")
							assert.Check(t, is.Equal((pod.Status.ContainerStatuses[i].State.Terminated.Message), statusMessagePodDeleted), "Status message code should be set to pod deleted")
						} else {
							assert.Check(t, pod.Status.ContainerStatuses[i].State.Running != nil, "Container should be running")
							assert.Check(t, is.Nil((pod.Status.ContainerStatuses[i].State.Terminated)), "Container should not be terminated")
						}
					}

				}
			}
		})
	}
}

func TestGetPodStatus(t *testing.T) {
	podName := "pod-" + uuid.New().String()
	podNamespace := "ns-" + uuid.New().String()
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	containersList := testsutil.CreateACIContainersListObj(runningState, "Initializing",
		testsutil.CgCreationTime.Add(time.Second*2), testsutil.CgCreationTime.Add(time.Second*3),
		true, true, true)

	invalidContainersList := testsutil.CreateACIContainersListObj(runningState, "Initializing",
		testsutil.CgCreationTime.Add(time.Second*2), testsutil.CgCreationTime.Add(time.Second*3),
		true, true, true)
	invalidContainersList[0].Properties = nil

	validContainerGroup := testsutil.CreateContainerGroupObj(podName, podNamespace, "Succeeded", containersList, "Succeeded")

	cgEmptyContainerList := testsutil.CreateContainerGroupObj(podName, podNamespace, "Succeeded", nil, "Succeeded")

	cgInvalidContainerList := testsutil.CreateContainerGroupObj(podName, podNamespace, "Succeeded", invalidContainersList, "Succeeded")

	aciMocks := createNewACIMock()
	provider, err := createTestProvider(aciMocks, NewMockConfigMapLister(mockCtrl),
		NewMockSecretLister(mockCtrl), NewMockPodLister(mockCtrl))
	if err != nil {
		t.Fatal("failed to create the test provider", err)
	}

	cases := []struct {
		description   string
		cgInfo        *azaciv2.ContainerGroup
		expectedError error
	}{
		{
			description:   "successfully gets pod status",
			cgInfo:        validContainerGroup,
			expectedError: nil,
		},
		{
			description:   "fails to get container group info",
			cgInfo:        nil,
			expectedError: errors.New("failed to retrieve container group"),
		},
		{
			description:   "fails to validate container group info",
			cgInfo:        cgEmptyContainerList,
			expectedError: fmt.Errorf("containers list cannot be nil for container group %s", *cgEmptyContainerList.Name),
		},
		{
			description:   "fails to get pod status",
			cgInfo:        cgInvalidContainerList,
			expectedError: fmt.Errorf("container %s properties cannot be nil", *invalidContainersList[0].Name),
		},
	}

	for _, tc := range cases {
		t.Run(tc.description, func(t *testing.T) {
			aciMocks.MockGetContainerGroupInfo =
				func(ctx context.Context, resourceGroup, namespace, name, nodeName string) (*azaciv2.ContainerGroup, error) {
					if tc.cgInfo == nil {
						return nil, tc.expectedError
					}
					return tc.cgInfo, nil
				}

			podStatus, err := provider.GetPodStatus(context.Background(), podNamespace, podName)

			if tc.expectedError != nil {
				assert.Equal(t, tc.expectedError.Error(), err.Error(), "Error messages should match")
				assert.Check(t, is.Nil((podStatus)), "podStatus should be nil")
			} else {
				assert.Check(t, podStatus != nil, "podStatus should not be nil")
				assert.Check(t, podStatus.Conditions != nil, "podStatus conditions should be set")
				assert.Check(t, podStatus.StartTime != nil, "podStatus start time should be set")
				assert.Check(t, podStatus.ContainerStatuses != nil, "podStatus container statuses should be set")
				assert.Check(t, is.Equal(podStatus.HostIP, provider.internalIP), "podStatus host IP should match")
				assert.Check(t, is.Equal(len(podStatus.Conditions), 3), "3 pod conditions should be present")
			}
		})
	}
}

func TestGetImagePullSecretsWithDockerCfgSecret(t *testing.T) {
	podName := "pod-" + uuid.New().String()
	podNamespace := "ns-" + uuid.New().String()
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	aciMocks := createNewACIMock()

	pod := testsutil.CreatePodObj(podName, podNamespace)

	invalidSecretNoDockerCfg := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fakeSecret",
			Namespace: podNamespace,
		},
		Type: corev1.SecretTypeDockercfg,
	}

	invalidAuthConfig := `{
		"repoData": {}
	}`
	invalidSecretWithDockerCfg := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fakeSecret",
			Namespace: podNamespace,
		},
		Type: corev1.SecretTypeDockercfg,
		Data: map[string][]byte{
			corev1.DockerConfigKey: []byte(invalidAuthConfig),
		},
	}

	validAuthConfig := `{
		"repoData": {
			"username": "fakeUserName",
			"password": "fakePassword"
		}
	}`
	validSecretWithDockerCfg := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fakeSecret",
			Namespace: podNamespace,
		},
		Type: corev1.SecretTypeDockercfg,
		Data: map[string][]byte{
			corev1.DockerConfigKey: []byte(validAuthConfig),
		},
	}

	cases := []struct {
		description     string
		callSecretMocks func(secretMock *MockSecretLister)
		expectedError   error
	}{
		{
			description: "Secret is of type SecretTypeDockerCfg",
			callSecretMocks: func(secretMock *MockSecretLister) {
				mockSecretNamespaceLister := NewMockSecretNamespaceLister(mockCtrl)
				secretMock.EXPECT().Secrets(podNamespace).Return(mockSecretNamespaceLister)
				mockSecretNamespaceLister.EXPECT().Get(pod.Spec.ImagePullSecrets[0].Name).Return(&validSecretWithDockerCfg, nil)
			},
			expectedError: nil,
		},
		{
			description: "SecretTypeDockerCfg contains invalid authConfig",
			callSecretMocks: func(secretMock *MockSecretLister) {
				mockSecretNamespaceLister := NewMockSecretNamespaceLister(mockCtrl)
				secretMock.EXPECT().Secrets(podNamespace).Return(mockSecretNamespaceLister)
				mockSecretNamespaceLister.EXPECT().Get(pod.Spec.ImagePullSecrets[0].Name).Return(&invalidSecretWithDockerCfg, nil)
			},
			expectedError: fmt.Errorf("no username present in auth config for server: repoData"),
		},
		{
			description: "pod contains imagePullSecrets that cannot be retrieved",
			callSecretMocks: func(secretMock *MockSecretLister) {
				mockSecretNamespaceLister := NewMockSecretNamespaceLister(mockCtrl)
				secretMock.EXPECT().Secrets(podNamespace).Return(mockSecretNamespaceLister)
				mockSecretNamespaceLister.EXPECT().Get(pod.Spec.ImagePullSecrets[0].Name).Return(nil, nil)
			},
			expectedError: errors.New("error getting image pull secret"),
		},
		{
			description: "Secret type is SecretTypeDockerCfg but no docker config is present",
			callSecretMocks: func(secretMock *MockSecretLister) {
				mockSecretNamespaceLister := NewMockSecretNamespaceLister(mockCtrl)
				secretMock.EXPECT().Secrets(podNamespace).Return(mockSecretNamespaceLister)
				mockSecretNamespaceLister.EXPECT().Get(pod.Spec.ImagePullSecrets[0].Name).Return(&invalidSecretNoDockerCfg, nil)
			},
			expectedError: errors.New("no dockercfg present in secret"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.description, func(t *testing.T) {
			pod.Spec.ImagePullSecrets = []corev1.LocalObjectReference{
				{
					Name: "fakeSecret",
				},
			}

			mockSecretLister := NewMockSecretLister(mockCtrl)
			tc.callSecretMocks(mockSecretLister)

			provider, err := createTestProvider(aciMocks, NewMockConfigMapLister(mockCtrl),
				mockSecretLister, NewMockPodLister(mockCtrl))
			if err != nil {
				t.Fatal("failed to create the test provider", err)
			}

			ips, err := provider.getImagePullSecrets(pod)

			if tc.expectedError == nil {
				assert.NilError(t, tc.expectedError, err)
				assert.Check(t, ips != nil, "imagePullSecrets should not be nil")
				assert.Check(t, is.Equal(len(ips), 1), "1 image pull secret should be present")
			} else {
				assert.Equal(t, tc.expectedError.Error(), err.Error())
			}
		})
	}
}

func TestGetImagePullSecretsWithDockerConfigJSONSecret(t *testing.T) {
	podName := "pod-" + uuid.New().String()
	podNamespace := "ns-" + uuid.New().String()
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	aciMocks := createNewACIMock()

	pod := testsutil.CreatePodObj(podName, podNamespace)

	invalidSecretType := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fakeSecret",
			Namespace: podNamespace,
		},
		Type: "fakeType",
	}

	invalidSecretNoDockerConfigJsonKey := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fakeSecret",
			Namespace: podNamespace,
		},
		Type: corev1.SecretTypeDockerConfigJson,
	}

	invalidCfgJson := `{
		"repoData": {
			"auths": {}
		}
	}`
	invalidSecretMalformedCfgJson := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fakeSecret",
			Namespace: podNamespace,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(invalidCfgJson),
		},
	}

	validCfgJson := `{
		"auths": {
			"repoData": {
				"username": "fakeUserName",
				"password": "fakePassword"
			}
		}
	}`
	validSecretCfgJson := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fakeSecret",
			Namespace: podNamespace,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(validCfgJson),
		},
	}

	cases := []struct {
		description     string
		callSecretMocks func(secretMock *MockSecretLister)
		expectedError   error
	}{
		{
			description: "Secret is of type SecretTypeDockerConfigJSON with a valid docker config",
			callSecretMocks: func(secretMock *MockSecretLister) {
				mockSecretNamespaceLister := NewMockSecretNamespaceLister(mockCtrl)
				secretMock.EXPECT().Secrets(podNamespace).Return(mockSecretNamespaceLister)
				mockSecretNamespaceLister.EXPECT().Get(pod.Spec.ImagePullSecrets[0].Name).Return(&validSecretCfgJson, nil)
			},
			expectedError: nil,
		},
		{
			description: "Secret data has malformed docker config",
			callSecretMocks: func(secretMock *MockSecretLister) {
				mockSecretNamespaceLister := NewMockSecretNamespaceLister(mockCtrl)
				secretMock.EXPECT().Secrets(podNamespace).Return(mockSecretNamespaceLister)
				mockSecretNamespaceLister.EXPECT().Get(pod.Spec.ImagePullSecrets[0].Name).Return(&invalidSecretMalformedCfgJson, nil)
			},
			expectedError: errors.New("malformed dockerconfigjson in secret"),
		},
		{
			description: "Secret type is SecretTypeDockerConfigJSON but no docker config JSON key is present",
			callSecretMocks: func(secretMock *MockSecretLister) {
				mockSecretNamespaceLister := NewMockSecretNamespaceLister(mockCtrl)
				secretMock.EXPECT().Secrets(podNamespace).Return(mockSecretNamespaceLister)
				mockSecretNamespaceLister.EXPECT().Get(pod.Spec.ImagePullSecrets[0].Name).Return(&invalidSecretNoDockerConfigJsonKey, nil)
			},
			expectedError: errors.New("no dockerconfigjson present in secret"),
		},
		{
			description: "Secret type is not valid",
			callSecretMocks: func(secretMock *MockSecretLister) {
				mockSecretNamespaceLister := NewMockSecretNamespaceLister(mockCtrl)
				secretMock.EXPECT().Secrets(podNamespace).Return(mockSecretNamespaceLister)
				mockSecretNamespaceLister.EXPECT().Get(pod.Spec.ImagePullSecrets[0].Name).Return(&invalidSecretType, nil)
			},
			expectedError: errors.New("image pull secret type is not one of kubernetes.io/dockercfg or kubernetes.io/dockerconfigjson"),
		},
		{
			description: "pod contains imagePullSecrets that cannot be found",
			callSecretMocks: func(secretMock *MockSecretLister) {
				mockSecretNamespaceLister := NewMockSecretNamespaceLister(mockCtrl)
				secretMock.EXPECT().Secrets(podNamespace).Return(mockSecretNamespaceLister)
				mockSecretNamespaceLister.EXPECT().Get(pod.Spec.ImagePullSecrets[0].Name).Return(nil, errors.New("secret not found"))
			},
			expectedError: errors.New("secret not found"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.description, func(t *testing.T) {
			pod.Spec.ImagePullSecrets = []corev1.LocalObjectReference{
				{
					Name: "fakeSecret",
				},
			}

			mockSecretLister := NewMockSecretLister(mockCtrl)
			tc.callSecretMocks(mockSecretLister)

			provider, err := createTestProvider(aciMocks, NewMockConfigMapLister(mockCtrl),
				mockSecretLister, NewMockPodLister(mockCtrl))
			if err != nil {
				t.Fatal("failed to create the test provider", err)
			}

			ips, err := provider.getImagePullSecrets(pod)

			if tc.expectedError == nil {
				assert.NilError(t, tc.expectedError, err)
				assert.Check(t, ips != nil, "imagePullSecrets should not be nil")
				assert.Check(t, is.Equal(len(ips), 1), "1 image pull secret should be present")
			} else {
				assert.Equal(t, tc.expectedError.Error(), err.Error())
			}
		})
	}
}

func TestGetContainerLogs(t *testing.T) {

	podName := "pod-" + uuid.New().String()
	podNamespace := "ns-" + uuid.New().String()
	containerName := "fake_container_name"
	fakeLogContent := "fake_log_content\n"
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	containersList := testsutil.CreateACIContainersListObj(runningState, "Initializing",
		testsutil.CgCreationTime.Add(time.Second*2), testsutil.CgCreationTime.Add(time.Second*3),
		true, true, true)

	cgInfo := testsutil.CreateContainerGroupObj(podName, podNamespace, "Succeeded", containersList, "Succeeded")

	aciMocks := createNewACIMock()

	aciMocks.MockGetContainerGroupInfo = func(ctx context.Context, resourceGroup, namespace, name, nodeName string) (*azaciv2.ContainerGroup, error) {

		return cgInfo, nil
	}

	provider, err := createTestProvider(aciMocks, NewMockConfigMapLister(mockCtrl),
		NewMockSecretLister(mockCtrl), NewMockPodLister(mockCtrl))
	if err != nil {
		t.Fatal("failed to create the test provider", err)
	}

	cases := []struct {
		description    string
		logContent     *string
		expectedOutput *string
	}{
		{
			description:    "ListLogs api call returned valid log content",
			logContent:     &fakeLogContent,
			expectedOutput: &fakeLogContent,
		},
		{
			description:    "ListLogs api call returned nil",
			logContent:     nil,
			expectedOutput: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.description, func(t *testing.T) {

			aciMocks.MockListLogs =
				func(ctx context.Context, resourceGroup, cgName, containerName string, opts api.ContainerLogOpts) (*string, error) {

					return tc.logContent, nil
				}

			var opts api.ContainerLogOpts

			containerLogsRC, _ := provider.GetContainerLogs(context.Background(), podNamespace, podName, containerName, opts)

			if tc.expectedOutput == nil {
				assert.Check(t, containerLogsRC == nil, "Container Logs Read Closer should be nil for nil Container Logs content")
			} else {
				defer containerLogsRC.Close()

				reader := bufio.NewReader(containerLogsRC)
				containerLogsContent, err := reader.ReadString('\n')
				if err != nil && err != io.EOF {
					t.Fatal("Failed to read string from Read Closer", err)
				}
				assert.Equal(t, *tc.expectedOutput, containerLogsContent, "ContainerLogs content should match the expected output")
			}

		})
	}

}
