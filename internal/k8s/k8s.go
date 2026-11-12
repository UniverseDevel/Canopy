// Package k8s reads network-operator resources from a Kubernetes cluster with
// the dynamic client (unstructured objects, so CRD changes in v1alpha1 don't
// break the build), and pauses or resumes reconciliation.
package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Group is network-operator's API group.
const Group = "networking.metal.ironcore.dev"

// Annotations used by the operator.
const (
	PausedAnnotation      = Group + "/paused"
	MaintenanceAnnotation = Group + "/maintenance"
)

var deviceGVR = schema.GroupVersionResource{Group: Group, Version: "v1alpha1", Resource: "devices"}

// Client talks to one cluster.
type Client struct {
	Context string
	dyn     dynamic.Interface
	core    kubernetes.Interface
	disc    discovery.DiscoveryInterface
}

// Options select the kubeconfig and context.
type Options struct {
	Kubeconfig string `json:"kubeconfig,omitempty"` // default $KUBECONFIG or ~/.kube/config
	Context    string `json:"context,omitempty"`    // default current context
}

// Contexts lists the contexts in the kubeconfig and the current one.
func Contexts(o Options) ([]string, string, error) {
	cfg, err := loader(o).RawConfig()
	if err != nil {
		return nil, "", apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	var out []string
	for n := range cfg.Contexts {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, cfg.CurrentContext, nil
}

func loader(o Options) clientcmd.ClientConfig {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if o.Kubeconfig != "" {
		rules.ExplicitPath = o.Kubeconfig
	} else if os.Getenv("KUBECONFIG") == "" {
		if home, err := os.UserHomeDir(); err == nil {
			rules.ExplicitPath = filepath.Join(home, ".kube", "config")
		}
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: o.Context})
}

// New connects to the cluster.
func New(o Options) (*Client, error) {
	cc := loader(o)
	rc, err := cc.ClientConfig()
	if err != nil {
		return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, fmt.Errorf("kubeconfig: %w", err)).
			WithHint("pass --kubeconfig or set KUBECONFIG")
	}
	rc.Timeout = 15 * time.Second
	raw, _ := cc.RawConfig()
	c := &Client{Context: raw.CurrentContext}
	if o.Context != "" {
		c.Context = o.Context
	}
	if c.dyn, err = dynamic.NewForConfig(rc); err != nil {
		return nil, apperr.Wrap(apperr.ClassConnection, apperr.CodeUnreachable, err)
	}
	if c.core, err = kubernetes.NewForConfig(rc); err != nil {
		return nil, apperr.Wrap(apperr.ClassConnection, apperr.CodeUnreachable, err)
	}
	c.disc = c.core.Discovery()
	return c, nil
}

func k8sErr(err error) error {
	if err == nil {
		return nil
	}
	e := fmt.Errorf("kubernetes: %w", err)
	switch {
	case apierrors.IsNotFound(err):
		return apperr.Wrap(apperr.ClassUsage, apperr.CodeNotFound, e)
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		return apperr.Wrap(apperr.ClassConnection, apperr.CodeAuth, e).WithHint("check RBAC for your kube user (Canopy reads CRs and the Devices' Secrets)")
	}
	return apperr.Wrap(apperr.ClassConnection, apperr.CodeUnreachable, e)
}

// Condition is a status condition.
type Condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// Device is a Device CR, summarized.
type Device struct {
	Namespace  string            `json:"namespace"`
	Name       string            `json:"name"`
	Provider   string            `json:"provider"`
	Address    string            `json:"address"`
	Phase      string            `json:"phase,omitempty"`
	Paused     bool              `json:"paused"` // spec.paused or the paused annotation
	Ready      string            `json:"ready"`  // status of the Ready condition
	Conditions []Condition       `json:"conditions"`
	SecretRef  string            `json:"secretRef,omitempty"`
	TLS        bool              `json:"tls"`
	Labels     map[string]string `json:"labels,omitempty"`
}

func conditions(u *unstructured.Unstructured) []Condition {
	raw, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	out := []Condition{}
	for _, x := range raw {
		m, _ := x.(map[string]any)
		out = append(out, Condition{Type: str(m["type"]), Status: str(m["status"]), Reason: str(m["reason"]), Message: str(m["message"])})
	}
	return out
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func condStatus(cs []Condition, t string) string {
	for _, c := range cs {
		if c.Type == t {
			return c.Status
		}
	}
	return "Unknown"
}

func toDevice(u *unstructured.Unstructured) Device {
	d := Device{Namespace: u.GetNamespace(), Name: u.GetName(), Labels: u.GetLabels(), Conditions: conditions(u)}
	d.Provider, _, _ = unstructured.NestedString(u.Object, "spec", "provider")
	d.Address, _, _ = unstructured.NestedString(u.Object, "spec", "endpoint", "address")
	d.SecretRef, _, _ = unstructured.NestedString(u.Object, "spec", "endpoint", "secretRef", "name")
	_, d.TLS, _ = unstructured.NestedMap(u.Object, "spec", "endpoint", "tls")
	d.Phase, _, _ = unstructured.NestedString(u.Object, "status", "phase")
	sp, _, _ := unstructured.NestedBool(u.Object, "spec", "paused")
	d.Paused = sp || u.GetAnnotations()[PausedAnnotation] != ""
	d.Ready = condStatus(d.Conditions, "Ready")
	return d
}

// Devices lists Device CRs (all namespaces when ns is empty).
func (c *Client) Devices(ctx context.Context, ns string) ([]Device, error) {
	l, err := c.dyn.Resource(deviceGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, k8sErr(err)
	}
	out := []Device{}
	for i := range l.Items {
		out = append(out, toDevice(&l.Items[i]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Namespace+"/"+out[i].Name < out[j].Namespace+"/"+out[j].Name })
	return out, nil
}

// Device returns one Device.
func (c *Client) Device(ctx context.Context, ns, name string) (*unstructured.Unstructured, error) {
	u, err := c.dyn.Resource(deviceGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, k8sErr(err)
	}
	return u, nil
}

// Credentials are what the operator uses to reach a device.
type Credentials struct {
	Address  string
	Username string
	Password string
	CAPEM    []byte
	CertPEM  []byte
	KeyPEM   []byte
	Provider string
}

// DeviceCredentials reads the Device's endpoint, basic-auth Secret and TLS
// Secrets (needs RBAC to read Secrets in the Device's namespace).
func (c *Client) DeviceCredentials(ctx context.Context, ns, name string) (*Credentials, error) {
	u, err := c.Device(ctx, ns, name)
	if err != nil {
		return nil, err
	}
	d := toDevice(u)
	cr := &Credentials{Address: d.Address, Provider: d.Provider}
	if d.SecretRef != "" {
		s, err := c.core.CoreV1().Secrets(ns).Get(ctx, d.SecretRef, metav1.GetOptions{})
		if err != nil {
			return nil, k8sErr(err)
		}
		cr.Username, cr.Password = string(s.Data["username"]), string(s.Data["password"])
	}
	if caName, _, _ := unstructured.NestedString(u.Object, "spec", "endpoint", "tls", "ca", "name"); caName != "" {
		key, _, _ := unstructured.NestedString(u.Object, "spec", "endpoint", "tls", "ca", "key")
		if key == "" {
			key = "ca.crt"
		}
		s, err := c.core.CoreV1().Secrets(ns).Get(ctx, caName, metav1.GetOptions{})
		if err != nil {
			return nil, k8sErr(err)
		}
		cr.CAPEM = s.Data[key]
	}
	if certName, _, _ := unstructured.NestedString(u.Object, "spec", "endpoint", "tls", "certificate", "secretRef", "name"); certName != "" {
		s, err := c.core.CoreV1().Secrets(ns).Get(ctx, certName, metav1.GetOptions{})
		if err != nil {
			return nil, k8sErr(err)
		}
		cr.CertPEM, cr.KeyPEM = s.Data["tls.crt"], s.Data["tls.key"]
	}
	return cr, nil
}

// Resource is a network-operator resource attached to a Device.
type Resource struct {
	Kind       string         `json:"kind"`
	Resource   string         `json:"resource"` // plural, e.g. banners
	Version    string         `json:"version"`
	Namespace  string         `json:"namespace"`
	Name       string         `json:"name"`
	Ready      string         `json:"ready"`
	Paused     bool           `json:"paused"`
	Conditions []Condition    `json:"conditions"`
	Spec       map[string]any `json:"spec,omitempty"`
	Generation int64          `json:"generation"`
	Observed   int64          `json:"observedGeneration"`
}

// ResourceKind is one kind served by the operator's API group.
type ResourceKind struct {
	Kind     string `json:"kind"`
	Resource string `json:"resource"`
	Version  string `json:"version"`
}

// Kinds discovers the operator's resource kinds.
func (c *Client) Kinds() ([]ResourceKind, error) {
	groups, err := c.disc.ServerGroups()
	if err != nil {
		return nil, k8sErr(err)
	}
	var version string
	for _, g := range groups.Groups {
		if g.Name == Group {
			version = g.PreferredVersion.Version
		}
	}
	if version == "" {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "the cluster doesn't serve %s (is network-operator installed?)", Group)
	}
	rl, err := c.disc.ServerResourcesForGroupVersion(Group + "/" + version)
	if err != nil {
		return nil, k8sErr(err)
	}
	var out []ResourceKind
	for _, r := range rl.APIResources {
		if strings.Contains(r.Name, "/") || r.Kind == "Device" {
			continue // subresources
		}
		out = append(out, ResourceKind{Kind: r.Kind, Resource: r.Name, Version: version})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out, nil
}

// Resources lists resources whose spec.deviceRef.name is the device.
func (c *Client) Resources(ctx context.Context, ns, device string) ([]Resource, error) {
	kinds, err := c.Kinds()
	if err != nil {
		return nil, err
	}
	out := []Resource{}
	for _, k := range kinds {
		l, err := c.dyn.Resource(schema.GroupVersionResource{Group: Group, Version: k.Version, Resource: k.Resource}).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			continue // not listable (RBAC); skip
		}
		for i := range l.Items {
			u := &l.Items[i]
			ref, _, _ := unstructured.NestedString(u.Object, "spec", "deviceRef", "name")
			if ref != device {
				continue
			}
			out = append(out, toResource(u, k))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind+out[i].Name < out[j].Kind+out[j].Name })
	return out, nil
}

func toResource(u *unstructured.Unstructured, k ResourceKind) Resource {
	r := Resource{Kind: k.Kind, Resource: k.Resource, Version: k.Version, Namespace: u.GetNamespace(), Name: u.GetName(),
		Conditions: conditions(u), Generation: u.GetGeneration()}
	r.Spec, _, _ = unstructured.NestedMap(u.Object, "spec")
	r.Observed, _, _ = unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	r.Ready = condStatus(r.Conditions, "Ready")
	sp, _, _ := unstructured.NestedBool(u.Object, "spec", "paused")
	r.Paused = sp || u.GetAnnotations()[PausedAnnotation] != ""
	return r
}

// GetResource returns one resource by kind (Kind or plural, case-insensitive).
func (c *Client) GetResource(ctx context.Context, kind, ns, name string) (*Resource, error) {
	k, err := c.kind(kind)
	if err != nil {
		return nil, err
	}
	u, err := c.dyn.Resource(schema.GroupVersionResource{Group: Group, Version: k.Version, Resource: k.Resource}).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, k8sErr(err)
	}
	r := toResource(u, *k)
	return &r, nil
}

func (c *Client) kind(kind string) (*ResourceKind, error) {
	if strings.EqualFold(kind, "device") || strings.EqualFold(kind, "devices") {
		return &ResourceKind{Kind: "Device", Resource: "devices", Version: "v1alpha1"}, nil
	}
	kinds, err := c.Kinds()
	if err != nil {
		return nil, err
	}
	for _, k := range kinds {
		if strings.EqualFold(k.Kind, kind) || strings.EqualFold(k.Resource, kind) {
			return &k, nil
		}
	}
	return nil, apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "unknown kind %q in %s", kind, Group)
}

// SetPaused adds or removes the paused annotation on a resource (any kind,
// including Device) with a JSON merge patch, as kubectl net pause does.
func (c *Client) SetPaused(ctx context.Context, kind, ns, name string, paused bool) error {
	k, err := c.kind(kind)
	if err != nil {
		return err
	}
	val := "null"
	if paused {
		val = `"true"`
	}
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%s}}}`, PausedAnnotation, val)
	_, err = c.dyn.Resource(schema.GroupVersionResource{Group: Group, Version: k.Version, Resource: k.Resource}).Namespace(ns).
		Patch(ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	return k8sErr(err)
}

// WaitPaused waits until the resource's Paused condition matches.
func (c *Client) WaitPaused(ctx context.Context, kind, ns, name string, paused bool, timeout time.Duration) (string, error) {
	want := "False"
	if paused {
		want = "True"
	}
	deadline := time.Now().Add(timeout)
	for {
		r, err := c.GetResource(ctx, kind, ns, name)
		if err != nil {
			return "", err
		}
		got := condStatus(r.Conditions, "Paused")
		if got == want || (!paused && got == "Unknown") {
			return got, nil
		}
		if time.Now().After(deadline) {
			return got, apperr.New(apperr.ClassDevice, apperr.CodeTimeout, "%s %s/%s: Paused is %s after %s", kind, ns, name, got, timeout)
		}
		select {
		case <-ctx.Done():
			return got, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// OriginalEndpointAnnotation keeps a Device's endpoint while a proxy is inserted.
const OriginalEndpointAnnotation = "canopy.ironcore.dev/original-endpoint"

// InsertProxy points a Device at a proxy address, keeping the original endpoint
// in an annotation. dropTLS removes spec.endpoint.tls while proxied (for a proxy
// whose certificate the Device's CA doesn't trust).
func (c *Client) InsertProxy(ctx context.Context, ns, name, address string, dropTLS bool) (string, error) {
	u, err := c.Device(ctx, ns, name)
	if err != nil {
		return "", err
	}
	if _, ok := u.GetAnnotations()[OriginalEndpointAnnotation]; ok {
		return "", apperr.New(apperr.ClassUsage, apperr.CodeUsage, "a proxy is already inserted for %s/%s; remove it first", ns, name)
	}
	ep, _, _ := unstructured.NestedMap(u.Object, "spec", "endpoint")
	orig, _ := json.Marshal(ep)
	endpoint := map[string]any{"address": address}
	if dropTLS {
		endpoint["tls"] = nil
	}
	patch := map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{OriginalEndpointAnnotation: string(orig)}},
		"spec":     map[string]any{"endpoint": endpoint},
	}
	b, _ := json.Marshal(patch)
	_, err = c.dyn.Resource(deviceGVR).Namespace(ns).Patch(ctx, name, types.MergePatchType, b, metav1.PatchOptions{})
	return string(orig), k8sErr(err)
}

// RemoveProxy restores the endpoint saved by InsertProxy.
func (c *Client) RemoveProxy(ctx context.Context, ns, name string) (string, error) {
	u, err := c.Device(ctx, ns, name)
	if err != nil {
		return "", err
	}
	orig, ok := u.GetAnnotations()[OriginalEndpointAnnotation]
	if !ok {
		return "", apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "no proxy inserted for %s/%s", ns, name)
	}
	var ep map[string]any
	if err := json.Unmarshal([]byte(orig), &ep); err != nil {
		return "", apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, err)
	}
	if err := unstructured.SetNestedMap(u.Object, ep, "spec", "endpoint"); err != nil {
		return "", apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, err)
	}
	ann := u.GetAnnotations()
	delete(ann, OriginalEndpointAnnotation)
	u.SetAnnotations(ann)
	_, err = c.dyn.Resource(deviceGVR).Namespace(ns).Update(ctx, u, metav1.UpdateOptions{})
	return orig, k8sErr(err)
}
