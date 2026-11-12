package k8s

import (
	"context"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
)

// Generic object access for any kind (used to run fixtures, whose resources
// can be of any group: operator kinds, provider-specific kinds, Secrets).

var mapperMu sync.Mutex

func (c *Client) resourceFor(gvk schema.GroupVersionKind, ns string) (dynamic.ResourceInterface, error) {
	mapperMu.Lock()
	if c.mapper == nil {
		c.mapper = restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(c.disc))
	}
	m := c.mapper
	mapperMu.Unlock()
	mp, err := m.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, k8sErr(err)
	}
	if mp.Scope.Name() == meta.RESTScopeNameNamespace {
		return c.dyn.Resource(mp.Resource).Namespace(ns), nil
	}
	return c.dyn.Resource(mp.Resource), nil
}

// CreateObject creates any object.
func (c *Client) CreateObject(ctx context.Context, u *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	ri, err := c.resourceFor(u.GroupVersionKind(), u.GetNamespace())
	if err != nil {
		return nil, err
	}
	out, err := ri.Create(ctx, u, metav1.CreateOptions{})
	if err != nil {
		return nil, k8sErr(err)
	}
	return out, nil
}

// GetObject reads any object; nil, nil when it doesn't exist.
func (c *Client) GetObject(ctx context.Context, gvk schema.GroupVersionKind, ns, name string) (*unstructured.Unstructured, error) {
	ri, err := c.resourceFor(gvk, ns)
	if err != nil {
		return nil, err
	}
	u, err := ri.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, k8sErr(err)
	}
	return u, nil
}

// DeleteObject deletes any object (not found is fine).
func (c *Client) DeleteObject(ctx context.Context, gvk schema.GroupVersionKind, ns, name string) error {
	ri, err := c.resourceFor(gvk, ns)
	if err != nil {
		return err
	}
	if err := ri.Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return k8sErr(err)
	}
	return nil
}

// UpdateStatus writes an object's status subresource.
func (c *Client) UpdateStatus(ctx context.Context, u *unstructured.Unstructured) error {
	ri, err := c.resourceFor(u.GroupVersionKind(), u.GetNamespace())
	if err != nil {
		return err
	}
	if _, err := ri.UpdateStatus(ctx, u, metav1.UpdateOptions{}); err != nil {
		return k8sErr(err)
	}
	return nil
}

// CreateNamespace creates a namespace with a generated name.
func (c *Client) CreateNamespace(ctx context.Context, generateName string, labels map[string]string) (string, error) {
	ns, err := c.core.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: generateName, Labels: labels}}, metav1.CreateOptions{})
	if err != nil {
		return "", k8sErr(err)
	}
	return ns.Name, nil
}

// DeleteNamespace deletes a namespace (not found is fine).
func (c *Client) DeleteNamespace(ctx context.Context, name string) error {
	if err := c.core.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return k8sErr(err)
	}
	return nil
}

// ConditionsOf returns an object's status conditions.
func ConditionsOf(u *unstructured.Unstructured) []Condition { return conditions(u) }

// DropFinalizers removes an object's finalizers (to unblock a deletion that
// can't finish).
func (c *Client) DropFinalizers(ctx context.Context, gvk schema.GroupVersionKind, ns, name string) error {
	ri, err := c.resourceFor(gvk, ns)
	if err != nil {
		return err
	}
	if _, err := ri.Patch(ctx, name, types.MergePatchType, []byte(`{"metadata":{"finalizers":null}}`), metav1.PatchOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return k8sErr(err)
	}
	return nil
}
