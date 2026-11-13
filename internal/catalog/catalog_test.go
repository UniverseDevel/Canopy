package catalog

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

const ocIf = `module openconfig-interfaces {
  yang-version "1";
  namespace "http://openconfig.net/yang/interfaces";
  prefix "oc-if";
  import openconfig-extensions { prefix oc-ext; }
  description "Model for managing network interfaces and subinterfaces. More text.";
  oc-ext:openconfig-version "3.7.1";
  revision "2024-01-01" { description "x"; }
  revision "2023-01-01" { description "y"; }
}`

const dev = `module acme-deviations {
  namespace "urn:acme:dev"; prefix ad;
  import openconfig-interfaces { prefix oc-if; }
  deviation /oc-if:interfaces/oc-if:interface/oc-if:config/oc-if:mtu { deviate not-supported; }
}`

func TestIndexAndSearch(t *testing.T) {
	dir := t.TempDir()
	models := filepath.Join(dir, "models")
	os.MkdirAll(filepath.Join(models, "sub"), 0o755)
	os.WriteFile(filepath.Join(models, "openconfig-interfaces.yang"), []byte(ocIf), 0o644)
	os.WriteFile(filepath.Join(models, "sub", "acme-deviations.yang"), []byte(dev), 0o644)
	c, err := Open(filepath.Join(dir, "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	n, err := c.IndexDirs(ctx, "openconfig", "v5.0.0", []string{models})
	if err != nil || n != 2 {
		t.Fatalf("index %d %v", n, err)
	}
	if err := c.IndexNames("openconfig", "v4.0.0", []string{"release/models/openconfig-interfaces@2022-05-01.yang", "README.md"}); err != nil {
		t.Fatal(err)
	}
	// reindexing replaces, no duplicates
	if _, err := c.IndexDirs(ctx, "openconfig", "v5.0.0", []string{models}); err != nil {
		t.Fatal(err)
	}

	got, err := c.Search(ctx, Query{Text: "oc interf"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		// "oc" doesn't start a token of openconfig-interfaces; the prefix oc-if does
		t.Logf("oc interf: %d", len(got))
	}
	got, _ = c.Search(ctx, Query{Text: "openconfig interf"})
	if len(got) != 2 {
		t.Fatalf("search: %+v", got)
	}
	var full Entry
	for _, e := range got {
		if e.Version == "v5.0.0" {
			full = e
		}
	}
	if full.Revision != "2024-01-01" || full.OCVersion != "3.7.1" || full.Prefix != "oc-if" || full.Kind != "module" ||
		full.Description != "Model for managing network interfaces and subinterfaces." || !full.Enriched {
		t.Errorf("entry %+v", full)
	}
	if got, _ := c.Search(ctx, Query{Text: "subinterfaces"}); len(got) != 1 {
		t.Errorf("description search: %+v", got)
	}
	in, _ := c.Containing(ctx, "openconfig-interfaces")
	if len(in) != 2 {
		t.Errorf("containing %+v", in)
	}
	d, _ := c.Search(ctx, Query{Module: "acme-deviations"})
	if len(d) != 1 || len(d[0].Deviates) != 1 || d[0].Deviates[0] != "openconfig-interfaces" || d[0].Path != "models/sub/acme-deviations.yang" {
		t.Errorf("deviations %+v", d)
	}
	rs, _ := c.Releases()
	if len(rs) != 2 || rs[1].Files != 2 || !rs[1].Enriched || rs[0].Enriched {
		t.Errorf("releases %+v", rs)
	}
	if r, _ := c.Release("openconfig", "v4.0.0"); r == nil || r.Files != 1 {
		t.Errorf("release %+v", r)
	}
}
