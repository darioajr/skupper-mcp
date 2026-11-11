package kubernetes

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// loadFixture reads a v1/List of Skupper resources from testdata and returns
// the items as unstructured objects, decoded exactly as the dynamic client
// would decode them.
func loadFixture(t *testing.T, name string) []unstructured.Unstructured {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	asJSON, err := yaml.YAMLToJSON(raw)
	if err != nil {
		t.Fatalf("converting fixture %s to JSON: %v", name, err)
	}
	var list unstructured.UnstructuredList
	if err := list.UnmarshalJSON(asJSON); err != nil {
		t.Fatalf("decoding fixture %s: %v", name, err)
	}
	return list.Items
}

// fixtureByName returns the single object with the given name, failing the test
// if it is missing, so a renamed fixture surfaces as a clear failure.
func fixtureByName(t *testing.T, objs []unstructured.Unstructured, name string) unstructured.Unstructured {
	t.Helper()
	for _, o := range objs {
		if o.GetName() == name {
			return o
		}
	}
	t.Fatalf("fixture has no object named %q", name)
	return unstructured.Unstructured{}
}
