package k8s

import (
	"context"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Discovery per group-version, cached for a few minutes. client-go's
// deferred REST mapper re-discovers the whole API every time it meets a kind
// the cluster doesn't serve; on a cluster with many CRDs, and for samples of
// other vendors' kinds, that took seconds each. Asking for just the group
// version, and remembering the answer (including "not served"), doesn't.

const discoveryTTL = 5 * time.Minute

type gvInfo struct {
	at    time.Time
	kinds map[string]kindInfo // by Kind; nil: the group version isn't served
}

type kindInfo struct {
	resource   string
	namespaced bool
}

type discoveryCache struct {
	mu  sync.Mutex
	gvs map[string]gvInfo
}

// lookup finds the resource for a kind; ok is false when the cluster doesn't
// serve it.
func (c *Client) lookup(gvk schema.GroupVersionKind) (kindInfo, bool, error) {
	gv := gvk.GroupVersion().String()
	c.dc.mu.Lock()
	info, hit := c.dc.gvs[gv]
	c.dc.mu.Unlock()
	if !hit || time.Since(info.at) > discoveryTTL {
		rl, err := c.disc.ServerResourcesForGroupVersion(gv)
		info = gvInfo{at: time.Now()}
		switch {
		case apierrors.IsNotFound(err):
			// not served: remembered as such
		case err != nil:
			return kindInfo{}, false, k8sErr(err)
		default:
			info.kinds = map[string]kindInfo{}
			for _, r := range rl.APIResources {
				if len(r.Name) > 0 && !containsSlash(r.Name) {
					info.kinds[r.Kind] = kindInfo{resource: r.Name, namespaced: r.Namespaced}
				}
			}
		}
		c.dc.mu.Lock()
		if c.dc.gvs == nil {
			c.dc.gvs = map[string]gvInfo{}
		}
		c.dc.gvs[gv] = info
		c.dc.mu.Unlock()
	}
	k, ok := info.kinds[gvk.Kind]
	return k, ok, nil
}

func containsSlash(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return true
		}
	}
	return false
}

// forget drops what is known about a group version (after "no such kind", a
// CRD may just have been installed).
func (c *Client) forget(gvk schema.GroupVersionKind) {
	c.dc.mu.Lock()
	delete(c.dc.gvs, gvk.GroupVersion().String())
	c.dc.mu.Unlock()
}

func (c *Client) iface(gvk schema.GroupVersionKind, k kindInfo, ns string) dynamic.ResourceInterface {
	r := c.dyn.Resource(schema.GroupVersionResource{Group: gvk.Group, Version: gvk.Version, Resource: k.resource})
	if k.namespaced {
		return r.Namespace(ns)
	}
	return r
}

// Served reports whether the cluster serves a kind.
func (c *Client) Served(gvk schema.GroupVersionKind) (bool, error) {
	_, ok, err := c.lookup(gvk)
	return ok, err
}

// ListKind lists the objects of a kind in a namespace; served is false when
// the cluster doesn't serve the kind (then nothing is listed).
func (c *Client) ListKind(ctx context.Context, gvk schema.GroupVersionKind, ns string) (items []unstructured.Unstructured, served bool, err error) {
	k, ok, err := c.lookup(gvk)
	if err != nil || !ok {
		return nil, ok, err
	}
	l, err := c.iface(gvk, k, ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, true, k8sErr(err)
	}
	return l.Items, true, nil
}

// parallel runs f for 0..n-1 with at most w at a time.
func parallel(n, w int, f func(i int)) {
	sem := make(chan struct{}, w)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer func() { <-sem; wg.Done() }()
			f(i)
		}(i)
	}
	wg.Wait()
}

// Parallel is parallel for other packages.
func Parallel(n, w int, f func(i int)) { parallel(n, w, f) }
