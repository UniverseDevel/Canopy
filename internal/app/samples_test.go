package app

import "testing"

func TestVendorDir(t *testing.T) {
	for p, want := range map[string]string{
		"srlinux.nokia.networking.metal.ironcore.dev": "nokia/srlinux",
		"nx.cisco.networking.metal.ironcore.dev":      "cisco/nx",
		"x": "",
	} {
		if got := vendorDir(p); got != want {
			t.Errorf("vendorDir(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestRetarget(t *testing.T) {
	docs, err := decodeDocs([]byte(`apiVersion: v1
kind: Secret
metadata:
  name: user-password
stringData:
  password: x
---
apiVersion: networking.metal.ironcore.dev/v1alpha1
kind: User
metadata:
  name: user
  labels:
    networking.metal.ironcore.dev/device-name: leaf1
spec:
  deviceRef:
    name: leaf1
  passwordRef:
    name: user-password
  roles: [1, 2]
`))
	if err != nil || len(docs) != 2 {
		t.Fatalf("decode: %v %d", err, len(docs))
	}
	if !refersTo(docs[1]["spec"], "user-password") || refersTo(docs[1]["spec"], "other") {
		t.Error("refersTo")
	}
	objs, err := retarget([]map[string]any{docs[1], docs[0]}, "lab", "srl1")
	if err != nil {
		t.Fatal(err)
	}
	if objs[0].GetKind() != "Secret" {
		t.Error("Secrets should come first")
	}
	u := objs[1]
	if u.GetNamespace() != "lab" || u.GetLabels()[deviceNameLabel] != "srl1" {
		t.Errorf("namespace/label: %s %v", u.GetNamespace(), u.GetLabels())
	}
	if ref, _ := u.Object["spec"].(map[string]any)["deviceRef"].(map[string]any)["name"].(string); ref != "srl1" {
		t.Errorf("deviceRef = %q", ref)
	}
	if objs[0].GetNamespace() != "lab" {
		t.Error("Secret namespace")
	}
}
