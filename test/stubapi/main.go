// Command stubapi serves the project's sanitized Skupper v2 fixtures over the
// Kubernetes list endpoints, so the server can be driven end to end by external
// tooling such as MCP Inspector without a real cluster.
//
// It is a test fixture, not a product: it implements only namespaced list
// reads, it authenticates nobody, and it is bound to loopback.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "address to listen on")
	dir := flag.String("fixtures", "internal/kubernetes/testdata", "directory holding the fixture files")
	kubeconfig := flag.String("write-kubeconfig", "", "write a kubeconfig pointing at this server to this path")
	flag.Parse()

	items, err := loadFixtures(*dir)
	if err != nil {
		log.Fatalf("stubapi: %v", err)
	}

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("stubapi: %v", err)
	}
	url := "http://" + listener.Addr().String()

	if *kubeconfig != "" {
		if err := writeKubeconfig(*kubeconfig, url); err != nil {
			log.Fatalf("stubapi: %v", err)
		}
	}

	// Printed on its own line so a shell script can capture it.
	fmt.Println(url)

	log.Fatal(http.Serve(listener, handler(items))) //nolint:gosec // loopback test fixture, no timeouts needed
}

type object map[string]any

func loadFixtures(dir string) ([]object, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	var out []object
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		// The directory is a flag this fixture's own operator sets, and the
		// file name comes from reading that directory.
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name())) //nolint:gosec // fixture loader, operator-supplied directory
		if err != nil {
			return nil, err
		}
		asJSON, err := yaml.YAMLToJSON(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		var list struct {
			Items []object `json:"items"`
		}
		if err := json.Unmarshal(asJSON, &list); err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		out = append(out, list.Items...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no fixtures found in %s", dir)
	}
	return out, nil
}

var kindByResource = map[string]string{
	"sites":      "Site",
	"links":      "Link",
	"listeners":  "Listener",
	"connectors": "Connector",
}

func handler(items []object) http.Handler {
	mux := http.NewServeMux()

	// Discovery, so client-go can negotiate before listing.
	mux.HandleFunc("GET /apis/skupper.io/v2alpha1", func(w http.ResponseWriter, _ *http.Request) {
		resources := make([]object, 0, len(kindByResource))
		for name, kind := range kindByResource {
			resources = append(resources, object{
				"name": name, "kind": kind, "namespaced": true,
				"verbs": []string{"get", "list", "watch"},
			})
		}
		writeJSON(w, object{
			"kind": "APIResourceList", "apiVersion": "v1",
			"groupVersion": "skupper.io/v2alpha1", "resources": resources,
		})
	})

	mux.HandleFunc("GET /apis/skupper.io/v2alpha1/namespaces/{namespace}/{resource}",
		func(w http.ResponseWriter, r *http.Request) {
			namespace := r.PathValue("namespace")
			resource := r.PathValue("resource")
			kind, ok := kindByResource[resource]
			if !ok {
				writeStatus(w, http.StatusNotFound, "NotFound",
					fmt.Sprintf("the server could not find the requested resource %q", resource))
				return
			}
			matched := []object{}
			for _, item := range items {
				if item["kind"] != kind {
					continue
				}
				meta, _ := item["metadata"].(map[string]any)
				if meta == nil || meta["namespace"] != namespace {
					continue
				}
				matched = append(matched, item)
			}
			// %q rather than %s: these come from the request path, and an
			// unescaped newline in a log line is a forged log entry. gosec's
			// taint analysis does not model %q as a sanitiser.
			log.Printf("list %q in namespace %q: %d item(s)", resource, namespace, len(matched)) //nolint:gosec // %q escapes the verb's argument
			writeJSON(w, object{
				"apiVersion": "skupper.io/v2alpha1",
				"kind":       kind + "List",
				"metadata":   object{"resourceVersion": "1"},
				"items":      matched,
			})
		})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("unhandled request %q %q", r.Method, r.URL.Path) //nolint:gosec // %q escapes the verb's argument
		writeStatus(w, http.StatusNotFound, "NotFound", "not found")
	})
	return mux
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("encoding response: %v", err)
	}
}

func writeStatus(w http.ResponseWriter, code int, reason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	writeJSON(w, object{
		"kind": "Status", "apiVersion": "v1", "status": "Failure",
		"reason": reason, "message": message, "code": code,
	})
}

func writeKubeconfig(path, server string) error {
	body := fmt.Sprintf(`# Generated by test/stubapi. Points at a loopback fixture server, not a
# cluster. It carries no credential because the fixture server authenticates
# nobody; never adapt this shape for a real cluster.
apiVersion: v1
kind: Config
clusters:
  - name: stub
    cluster:
      server: %s
contexts:
  - name: west-cluster
    context:
      cluster: stub
      user: stub
  - name: east-cluster
    context:
      cluster: stub
      user: stub
current-context: west-cluster
users:
  - name: stub
    user: {}
`, server)
	return os.WriteFile(path, []byte(body), 0o600)
}
