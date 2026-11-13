package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/fixtures"
	"github.com/UniverseDevel/Canopy/internal/k8s"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Samples: network-operator's example resources (config/samples in the
// checkout), applied to a Device the way Tilt's sample buttons do, but aimed at
// the Device chosen in Canopy instead of the sample's leaf1.

// Sample is one resource from the samples, with the Secrets it uses.
type Sample struct {
	ID         string   `json:"id"`     // file:index
	File       string   `json:"file"`   // relative to config/samples
	Vendor     string   `json:"vendor"` // vendor folder (nokia/srlinux), "" for the common ones
	Kind       string   `json:"kind"`
	Name       string   `json:"name"`
	APIVersion string   `json:"apiVersion"`
	Secrets    []string `json:"secrets,omitempty"`
	YAML       string   `json:"yaml"` // the resource and its Secrets, as in the file
	// Applied is set when an object of this kind and name is in the Device's
	// namespace; OtherDevice when that object points at another Device.
	Applied     bool   `json:"applied"`
	OtherDevice string `json:"otherDevice,omitempty"`
	// NotServed: the cluster has no such kind (another provider's, or a CRD
	// that isn't installed), so it can't be applied.
	NotServed bool `json:"notServed,omitempty"`
}

const deviceNameLabel = "networking.metal.ironcore.dev/device-name"

// vendorDir maps a provider (srlinux.nokia.networking.metal.ironcore.dev) to
// its samples folder (nokia/srlinux).
func vendorDir(provider string) string {
	p := strings.Split(provider, ".")
	if len(p) < 2 {
		return ""
	}
	return p[1] + "/" + p[0]
}

func decodeDocs(data []byte) ([]map[string]any, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var out []map[string]any
	for {
		var m map[string]any
		err := dec.Decode(&m)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "YAML: %v", err)
		}
		if m != nil {
			out = append(out, m)
		}
	}
}

func str(m map[string]any, path ...string) string {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[p]
	}
	s, _ := cur.(string)
	return s
}

// refersTo reports whether any string in v equals name (a Secret named in a
// secretRef, passwordRef, ...).
func refersTo(v any, name string) bool {
	switch x := v.(type) {
	case map[string]any:
		for _, e := range x {
			if refersTo(e, name) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if refersTo(e, name) {
				return true
			}
		}
	case string:
		return x == name
	}
	return false
}

// Samples lists the samples for a provider (the common ones and its vendor
// folder) and which are applied in the Device's namespace.
func (s *Service) Samples(ctx context.Context, o OperatorOptions, ns, device, provider string) ([]Sample, string, error) {
	repo, err := fixtures.FindRepo(o.OperatorRepo)
	if err != nil {
		return nil, "", err
	}
	root := filepath.Join(repo, "config", "samples")
	vdir := vendorDir(provider)
	var files []string
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && !strings.HasPrefix(vdir+"/", rel+"/") {
				return filepath.SkipDir // another vendor's samples
			}
			return nil
		}
		if strings.HasSuffix(rel, ".yaml") && filepath.Base(rel) != "kustomization.yaml" {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return nil, "", apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	if len(files) == 0 {
		return nil, "", apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "no samples in %s", root)
	}
	cl, err := s.Operator(o)
	if err != nil {
		return nil, "", err
	}
	var out []Sample
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil {
			continue
		}
		docs, err := decodeDocs(data)
		if err != nil {
			continue
		}
		secrets := map[string]map[string]any{}
		for _, d := range docs {
			if str(d, "kind") == "Secret" {
				secrets[str(d, "metadata", "name")] = d
			}
		}
		for i, d := range docs {
			kind := str(d, "kind")
			if kind == "Secret" || kind == "Device" || kind == "" {
				continue // the Device is the one selected; Secrets go with what uses them
			}
			sm := Sample{ID: f + ":" + strconv.Itoa(i), File: f, Kind: kind, Name: str(d, "metadata", "name"), APIVersion: str(d, "apiVersion")}
			if strings.Contains(f, "/") {
				sm.Vendor = filepath.ToSlash(filepath.Dir(f))
			}
			group := []map[string]any{}
			var names []string
			for n := range secrets {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				if refersTo(d["spec"], n) {
					sm.Secrets = append(sm.Secrets, n)
					group = append(group, secrets[n])
				}
			}
			group = append(group, d)
			var buf bytes.Buffer
			enc := yaml.NewEncoder(&buf)
			enc.SetIndent(2)
			for _, g := range group {
				enc.Encode(g)
			}
			enc.Close()
			sm.YAML = buf.String()
			out = append(out, sm)
		}
	}
	// What is in the cluster: one List per kind (not a Get per sample), several
	// at a time; kinds the cluster doesn't serve are marked, not looked up again.
	type key struct{ apiVersion, kind string }
	var kinds []key
	seen := map[key]bool{}
	for _, sm := range out {
		k := key{sm.APIVersion, sm.Kind}
		if !seen[k] {
			seen[k] = true
			kinds = append(kinds, k)
		}
	}
	type state struct {
		served bool
		refs   map[string]string // name -> spec.deviceRef.name
	}
	states := make([]state, len(kinds))
	k8s.Parallel(len(kinds), 12, func(i int) {
		gvk := schema.FromAPIVersionAndKind(kinds[i].apiVersion, kinds[i].kind)
		items, served, err := cl.ListKind(ctx, gvk, ns)
		st := state{served: served || err != nil, refs: map[string]string{}}
		for _, u := range items {
			ref, _, _ := unstructured.NestedString(u.Object, "spec", "deviceRef", "name")
			st.refs[u.GetName()] = ref
		}
		states[i] = st
	})
	byKind := map[key]state{}
	for i, k := range kinds {
		byKind[k] = states[i]
	}
	for i := range out {
		st := byKind[key{out[i].APIVersion, out[i].Kind}]
		out[i].NotServed = !st.served
		if ref, ok := st.refs[out[i].Name]; ok {
			out[i].Applied = true
			if ref != "" && ref != device {
				out[i].OtherDevice = ref
			}
		}
	}
	return out, root, nil
}

// retarget points sample objects at a Device: its namespace, spec.deviceRef
// and the device-name label.
func retarget(docs []map[string]any, ns, device string) ([]*unstructured.Unstructured, error) {
	var out []*unstructured.Unstructured
	for _, d := range docs {
		u, err := toUnstructured(d)
		if err != nil {
			return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
		}
		if u.GetName() == "" || u.GetKind() == "" {
			return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "every object needs a kind and metadata.name")
		}
		u.SetNamespace(ns)
		if u.GetKind() != "Secret" {
			if _, ok, _ := unstructured.NestedMap(u.Object, "spec", "deviceRef"); ok {
				unstructured.SetNestedField(u.Object, device, "spec", "deviceRef", "name")
			}
			if l := u.GetLabels(); l != nil {
				if _, ok := l[deviceNameLabel]; ok {
					l[deviceNameLabel] = device
					u.SetLabels(l)
				}
			}
		}
		out = append(out, u)
	}
	// Secrets first, so what uses them finds them
	sort.SliceStable(out, func(i, j int) bool { return out[i].GetKind() == "Secret" && out[j].GetKind() != "Secret" })
	return out, nil
}

// ApplySamples applies objects (YAML, several documents) to a Device: aimed
// at it, then server-side applied as Canopy (like kubectl apply
// --server-side). The operator then configures the device.
//
// It returns what was applied and the applied objects as YAML (for the
// equivalent kubectl command).
func (s *Service) ApplySamples(ctx context.Context, o OperatorOptions, ns, device, text string) ([]string, string, error) {
	docs, err := decodeDocs([]byte(text))
	if err != nil {
		return nil, "", err
	}
	objs, err := retarget(docs, ns, device)
	if err != nil {
		return nil, "", err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	for _, u := range objs {
		enc.Encode(u.Object)
	}
	enc.Close()
	cl, err := s.Operator(o)
	if err != nil {
		return nil, buf.String(), err
	}
	var done []string
	for _, u := range objs {
		if err := cl.ApplyObject(ctx, u); err != nil {
			return done, buf.String(), err
		}
		done = append(done, u.GetKind()+"/"+u.GetName())
	}
	return done, buf.String(), nil
}

// DeleteSamples deletes the objects of a sample from the Device's namespace
// (the resource first, then its Secrets); the operator removes the
// configuration from the device.
func (s *Service) DeleteSamples(ctx context.Context, o OperatorOptions, ns, text string) ([]string, error) {
	docs, err := decodeDocs([]byte(text))
	if err != nil {
		return nil, err
	}
	objs, err := retarget(docs, ns, "")
	if err != nil {
		return nil, err
	}
	cl, err := s.Operator(o)
	if err != nil {
		return nil, err
	}
	var done []string
	for i := len(objs) - 1; i >= 0; i-- { // reverse: Secrets last
		u := objs[i]
		if err := cl.DeleteObject(ctx, u.GroupVersionKind(), ns, u.GetName()); err != nil {
			return done, err
		}
		done = append(done, u.GetKind()+"/"+u.GetName())
	}
	return done, nil
}
