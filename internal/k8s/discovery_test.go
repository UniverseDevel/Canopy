package k8s

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakedisc "k8s.io/client-go/discovery/fake"
	fakedyn "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func obj(kind, ns, name, device string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": Group + "/v1alpha1", "kind": kind,
		"metadata": map[string]any{"name": name, "namespace": ns},
		"spec":     map[string]any{"deviceRef": map[string]any{"name": device}},
	}}
	return u
}

func fakeClient(t *testing.T) (*Client, *fakedisc.FakeDiscovery) {
	t.Helper()
	gv := Group + "/v1alpha1"
	disc := &fakedisc.FakeDiscovery{Fake: &clienttesting.Fake{}}
	disc.Resources = []*metav1.APIResourceList{{
		GroupVersion: gv,
		APIResources: []metav1.APIResource{
			{Name: "banners", Kind: "Banner", Namespaced: true},
			{Name: "banners/status", Kind: "Banner", Namespaced: true},
			{Name: "interfaces", Kind: "Interface", Namespaced: true},
			{Name: "devices", Kind: "Device", Namespaced: true},
		},
	}}
	// ServerGroups in the fake is built from Resources
	lists := map[schema.GroupVersionResource]string{
		{Group: Group, Version: "v1alpha1", Resource: "banners"}:    "BannerList",
		{Group: Group, Version: "v1alpha1", Resource: "interfaces"}: "InterfaceList",
		{Group: Group, Version: "v1alpha1", Resource: "devices"}:    "DeviceList",
	}
	dyn := fakedyn.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists,
		obj("Banner", "lab", "banner", "leaf1"), obj("Banner", "lab", "other", "leaf2"), obj("Interface", "lab", "lo0", "leaf1"))
	return &Client{dyn: dyn, disc: disc}, disc
}

func TestLookupCachesNotServed(t *testing.T) {
	c, disc := fakeClient(t)
	missing := schema.GroupVersionKind{Group: "nx.cisco.networking.metal.ironcore.dev", Version: "v1alpha1", Kind: "System"}
	for i := 0; i < 3; i++ {
		ok, err := c.Served(missing)
		if err != nil || ok {
			t.Fatalf("Served(missing) = %v, %v", ok, err)
		}
	}
	if n := len(disc.Actions()); n != 1 {
		t.Errorf("a kind that isn't served should be looked up once, not %d times", n)
	}
	banner := schema.GroupVersionKind{Group: Group, Version: "v1alpha1", Kind: "Banner"}
	items, served, err := c.ListKind(context.Background(), banner, "lab")
	if err != nil || !served || len(items) != 2 {
		t.Fatalf("ListKind = %d items, %v, %v", len(items), served, err)
	}
}

func TestResourcesParallelAndCached(t *testing.T) {
	c, disc := fakeClient(t)
	rs, err := c.Resources(context.Background(), "lab", "leaf1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].Kind != "Banner" || rs[1].Kind != "Interface" {
		t.Fatalf("resources = %+v", rs)
	}
	before := len(disc.Actions())
	if _, err := c.Resources(context.Background(), "lab", "leaf1"); err != nil {
		t.Fatal(err)
	}
	if len(disc.Actions()) != before {
		t.Error("the operator's kinds should be discovered once, then cached")
	}
}
