package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/manager"
)

// routeClass is how a writing admin route changes config: as the
// draft door, as an immediate operation that writes no config draft, or as
// a one-item publish.
type routeClass int

const (
	draftDoor routeClass = iota + 1
	immediateOperation
	oneItemPublish
)

// writingRouteClasses names the class of every writing admin route.
var writingRouteClasses = map[string]routeClass{
	"POST /v1/admin/drafts":              draftDoor,
	"POST /v1/admin/drafts/check":        draftDoor,
	"PUT /v1/admin/drafts/{id}":          draftDoor,
	"POST /v1/admin/drafts/{id}/discard": draftDoor,
	"POST /v1/admin/drafts/{id}/revert":  draftDoor,
	"POST /v1/admin/drafts/{id}/publish": draftDoor,
	"POST /v1/admin/drafts/{id}/rebase":  draftDoor,
	"POST /v1/admin/drafts/{id}/contact": draftDoor,

	"POST /v1/admin/apps":                                      oneItemPublish,
	"DELETE /v1/admin/apps/{id}":                               oneItemPublish,
	"POST /v1/admin/roles":                                     oneItemPublish,
	"PATCH /v1/admin/roles/{id}":                               oneItemPublish,
	"DELETE /v1/admin/roles/{id}":                              oneItemPublish,
	"POST /v1/admin/roles/{id}/implications":                   oneItemPublish,
	"DELETE /v1/admin/roles/{id}/implications/{implicationId}": oneItemPublish,
	"POST /v1/admin/apps/{id}/bindings":                        oneItemPublish,
	"DELETE /v1/admin/bindings/{id}":                           oneItemPublish,
	"PUT /v1/admin/policies":                                   oneItemPublish,
	"POST /v1/admin/policies/{name}/activate":                  oneItemPublish,
	"DELETE /v1/admin/policies/{name}":                         oneItemPublish,

	"POST /v1/admin/users":                                      immediateOperation,
	"PATCH /v1/admin/users/{id}":                                immediateOperation,
	"DELETE /v1/admin/users/{id}":                               immediateOperation,
	"DELETE /v1/admin/users/{id}/devices/{deviceId}":            immediateOperation,
	"POST /v1/admin/users/{id}/lock":                            immediateOperation,
	"POST /v1/admin/users/{id}/unlock":                          immediateOperation,
	"PUT /v1/admin/users/{id}/nhi-key":                          immediateOperation,
	"DELETE /v1/admin/users/{id}/nhi-key":                       immediateOperation,
	"POST /v1/admin/assignments":                                immediateOperation,
	"DELETE /v1/admin/assignments/{id}":                         immediateOperation,
	"POST /v1/admin/packs":                                      immediateOperation,
	"DELETE /v1/admin/packs/{id}":                               immediateOperation,
	"POST /v1/admin/packs/{id}/bindings":                        immediateOperation,
	"DELETE /v1/admin/packs/{id}/bindings/{bindingId}":          immediateOperation,
	"POST /v1/admin/sessions/{id}/revoke":                       immediateOperation,
	"POST /v1/admin/sessions/revoke":                            immediateOperation,
	"POST /v1/admin/signing-keys/rotate":                        immediateOperation,
	"POST /v1/admin/signing-keys/client-assertion/rotate":       immediateOperation,
	"POST /v1/admin/signing-keys/client-assertion/{kid}/retire": immediateOperation,
	"POST /v1/admin/approvals/{id}/approve":                     immediateOperation,
	"POST /v1/admin/approvals/{id}/deny":                        immediateOperation,
	"POST /v1/admin/approvals/channels/{name}/test":             immediateOperation,
	"POST /v1/admin/approvers/enroll-token":                     immediateOperation,
	"DELETE /v1/admin/approvers/{id}":                           immediateOperation,
	"POST /v1/admin/api-tokens":                                 immediateOperation,
	"DELETE /v1/admin/api-tokens/{id}":                          immediateOperation,
	"POST /v1/admin/attestation-hashes":                         immediateOperation,
	"DELETE /v1/admin/attestation-hashes/{id}":                  immediateOperation,
	"POST /v1/admin/sinks/{name}/replay":                        immediateOperation,
	"POST /v1/admin/apps/{id}/health":                           immediateOperation,
	"POST /v1/admin/apps/{id}/enable":                           immediateOperation,
	"POST /v1/admin/apps/{id}/disable":                          immediateOperation,
	"POST /v1/admin/apps/{id}/secrets":                          immediateOperation,
	"DELETE /v1/admin/apps/{id}/secrets":                        immediateOperation,
	"DELETE /v1/admin/apps/{id}/secrets/{role}":                 immediateOperation,
	"POST /v1/admin/apps/import":                                immediateOperation,
	"POST /v1/admin/policies/validate":                          immediateOperation,
	"POST /v1/admin/policies/simulate":                          immediateOperation,
}

// TestWritingAdminRoutesHaveOneClass pins that config has no second writer:
// every writing admin route of routeTable is named in writingRouteClasses,
// so a new route that writes config fails here until someone decides its
// class, every named route exists, the drafts routes and only they are the
// draft door, and twelve routes are one-item publishes. Each app and role
// route among them is fired by directCases, which proves it publishes a
// one-item draft, and the policy tests fire the three policy routes.
func TestWritingAdminRoutesHaveOneClass(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, rt := range (&App{}).routeTable() {
		key := rt.method + " " + rt.pattern
		if rt.method == http.MethodGet || !strings.HasPrefix(rt.pattern, "/v1/admin/") {
			continue
		}
		seen[key] = true
		class, named := writingRouteClasses[key]
		switch {
		case !named:
			t.Errorf("%s writes and has no class: make it a draft door, a one-item publish, or name it an immediate operation in writingRouteClasses", key)
		case (class == draftDoor) != strings.HasPrefix(rt.pattern, "/v1/admin/drafts"):
			t.Errorf("%s is classed %d, and only the drafts routes are the draft door", key, class)
		}
	}
	var publishes []string
	for key, class := range writingRouteClasses {
		if !seen[key] {
			t.Errorf("%s is named in writingRouteClasses and routeTable has no such route", key)
		}
		if class == oneItemPublish {
			publishes = append(publishes, key)
		}
	}
	if len(publishes) != 12 {
		t.Errorf("%d one-item publishes, want twelve", len(publishes))
	}
	fired := map[string]bool{}
	for _, tc := range directCases() {
		fired[tc.route] = true
	}
	for _, key := range publishes {
		if !fired[key] && !strings.Contains(key, "/v1/admin/policies") {
			t.Errorf("%s is a one-item publish that directCases never fires", key)
		}
	}
}

// TestDeployAppManifestsPassIntake pins that every App manifest shipped
// under deploy/, the eval seed's servers and the helm samples, installs
// through POST /v1/admin/apps: it parses, and the drafts intake, the
// secret scan included, refuses nothing in it.
func TestDeployAppManifestsPassIntake(t *testing.T) {
	t.Parallel()
	var manifests []string
	root := filepath.Join("..", "..", "deploy")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || (filepath.Ext(path) != ".yaml" && filepath.Ext(path) != ".yml") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		for {
			var n yaml.Node
			if dec.Decode(&n) != nil {
				break
			}
			if appDocument(&n) {
				out, err := yaml.Marshal(&n)
				if err != nil {
					return err
				}
				manifests = append(manifests, path+"\n"+string(out))
			}
			manifests = append(manifests, embeddedApps(path, &n)...)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) < 3 {
		t.Fatalf("found %d App manifests under deploy/, want the seed's two and the helm sample", len(manifests))
	}
	for _, m := range manifests {
		path, doc, _ := strings.Cut(m, "\n")
		mf, err := manager.Parse([]byte(doc))
		if err != nil {
			t.Errorf("%s: an App manifest does not parse: %v", path, err)
			continue
		}
		d := drafts.Draft{Door: drafts.DoorAPI, Items: []drafts.Item{{Kind: drafts.KindApp, Name: mf.Metadata.Name, Op: drafts.OpPut, Doc: doc}}}
		for _, f := range intakeOf(d, drafts.Principal{}) {
			t.Errorf("%s: intake refuses %s with %s %q", path, mf.Metadata.Name, f.Code, findingWords(f))
		}
	}
}

// appDocument reports whether the YAML document n is an App manifest.
func appDocument(n *yaml.Node) bool {
	var head struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
	}
	return n.Decode(&head) == nil && head.Kind == "App" && strings.HasPrefix(head.APIVersion, "straza.dev/")
}

// embeddedApps answers the App manifests that n holds as string values,
// as helm values and a rendered ConfigMap carry them, each after its path
// and a line break.
func embeddedApps(path string, n *yaml.Node) []string {
	var out []string
	if n.Kind == yaml.ScalarNode && strings.Contains(n.Value, "kind: App") {
		var doc yaml.Node
		if yaml.Unmarshal([]byte(n.Value), &doc) == nil && len(doc.Content) > 0 && appDocument(doc.Content[0]) {
			out = append(out, path+"\n"+n.Value)
		}
	}
	for _, c := range n.Content {
		out = append(out, embeddedApps(path, c)...)
	}
	return out
}
