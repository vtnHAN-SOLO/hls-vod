package handler

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
)

// The spec must not drift from the code, in either direction:
//
//   - every /api/ response a handler test receives is validated against
//     docs/openapi.yaml (status documented, headers and body match schema);
//   - after a full run, every operation and every documented status code
//     must have been seen at least once, so the spec cannot describe
//     behaviour the code no longer has.
const specPath = "../../docs/openapi.yaml"

var spec struct {
	doc    *openapi3.T
	router routers.Router

	mu   sync.Mutex
	seen map[string]bool // "operationId status"
}

func TestMain(m *testing.M) {
	if err := loadSpec(); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %s: %v\n", specPath, err)
		os.Exit(1)
	}
	code := m.Run()
	if code == 0 && fullRun() {
		if missing := unexercised(); len(missing) > 0 {
			fmt.Fprintf(os.Stderr, "FAIL: %s documents responses no test exercises "+
				"(remove them from the spec or add a test):\n  %s\n", specPath, strings.Join(missing, "\n  "))
			code = 1
		}
	}
	os.Exit(code)
}

func loadSpec() error {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(specPath)
	if err != nil {
		return fmt.Errorf("loading: %w", err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		return fmt.Errorf("invalid OpenAPI document: %w", err)
	}
	r, err := legacy.NewRouter(doc)
	if err != nil {
		return fmt.Errorf("building router: %w", err)
	}
	spec.doc, spec.router, spec.seen = doc, r, map[string]bool{}
	return nil
}

// fullRun is false under -run, where coverage of the spec cannot be judged.
func fullRun() bool {
	f := flag.Lookup("test.run")
	return f == nil || f.Value.String() == ""
}

func unexercised() []string {
	var missing []string
	for _, path := range spec.doc.Paths.InMatchingOrder() {
		for method, op := range spec.doc.Paths.Value(path).Operations() {
			for status := range op.Responses.Map() {
				key := op.OperationID + " " + status
				if !spec.seen[key] {
					missing = append(missing, fmt.Sprintf("%s %s -> %s", method, path, status))
				}
			}
		}
	}
	sort.Strings(missing)
	return missing
}

// checkSpec validates one /api/ response against the spec.
func checkSpec(t *testing.T, req *http.Request, resp *http.Response, body []byte) {
	t.Helper()
	if !strings.HasPrefix(req.URL.Path, "/api/") {
		return
	}

	// A 405 is documented on the operations the path does support: every
	// method its Allow header names that the spec has (HEAD is implied by
	// GET). On /me/sessions/revoke-others that is DELETE, the {id} route,
	// and POST, revoke-others itself.
	if resp.StatusCode == http.StatusMethodNotAllowed {
		checked := 0
		for m := range strings.SplitSeq(resp.Header.Get("Allow"), ",") {
			if m = strings.TrimSpace(m); m != http.MethodHead {
				checkOperation(t, req, m, resp, body)
				checked++
			}
		}
		if checked == 0 {
			t.Fatalf("%s %s: 405 with no documented method in Allow %q", req.Method, req.URL.Path, resp.Header.Get("Allow"))
		}
		return
	}
	checkOperation(t, req, req.Method, resp, body)
}

// checkOperation validates resp against the operation method has on req's
// path, and records it as exercised.
func checkOperation(t *testing.T, req *http.Request, method string, resp *http.Response, body []byte) {
	t.Helper()
	lookup := req.Clone(req.Context())
	lookup.Method = method
	// ServeMux matches escaped segments ("..%2F.." is one {id}); route the
	// spec lookup the same way, not on the decoded path.
	if raw := req.URL.RawPath; raw != "" {
		lookup.URL.Path = raw
	}
	route, params, err := spec.router.FindRoute(lookup)
	if err != nil {
		// Unknown endpoint: no operation to check against, but the body must
		// still be the documented Error shape.
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s: not in the spec (%v) and not a 404", req.Method, req.URL.Path, err)
		}
		checkErrorSchema(t, body)
		return
	}

	in := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request: req, PathParams: params, Route: route,
		},
		Status:  resp.StatusCode,
		Header:  resp.Header,
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	}
	in.SetBodyBytes(body)
	if err := openapi3filter.ValidateResponse(req.Context(), in); err != nil {
		t.Fatalf("%s %s -> %d does not match %s:\n%v\nbody: %s",
			req.Method, req.URL.String(), resp.StatusCode, specPath, err, body)
	}

	spec.mu.Lock()
	spec.seen[fmt.Sprintf("%s %d", route.Operation.OperationID, resp.StatusCode)] = true
	spec.mu.Unlock()
}

func checkErrorSchema(t *testing.T, body []byte) {
	t.Helper()
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("error body is not JSON: %q", body)
	}
	schema := spec.doc.Components.Schemas["Error"].Value
	if err := schema.VisitJSON(v); err != nil {
		t.Fatalf("error body does not match the Error schema: %v\nbody: %s", err, body)
	}
}

// The checker itself must catch drift, or a green run proves nothing.
func TestSpecCheckerCatchesDrift(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		header http.Header
	}{
		{"undocumented field", 200, `{"movie_id":"0b9d6b52-2d2c-4a7b-9e0e-6a4f39a0c1d2","playlist_url":"/media/0b9d6b52-2d2c-4a7b-9e0e-6a4f39a0c1d2/master.m3u8","thumbnails_url":"/media/0b9d6b52-2d2c-4a7b-9e0e-6a4f39a0c1d2/thumbnails.vtt","token":"x"}`, nil},
		{"missing field", 200, `{"movie_id":"0b9d6b52-2d2c-4a7b-9e0e-6a4f39a0c1d2"}`, nil},
		{"undocumented status", 418, `{"error":{"code":"internal","message":"x"}}`, nil},
		{"wrong error code", 409, `{"error":{"code":"teapot","message":"x"}}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
				"http://127.0.0.1/api/v1/movies/0b9d6b52-2d2c-4a7b-9e0e-6a4f39a0c1d2/playback", nil)
			if err != nil {
				t.Fatal(err)
			}
			route, params, err := spec.router.FindRoute(req)
			if err != nil {
				t.Fatal(err)
			}
			h := http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}
			in := &openapi3filter.ResponseValidationInput{
				RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route},
				Status:                 tt.status, Header: h,
				Options: &openapi3filter.Options{IncludeResponseStatus: true},
			}
			in.SetBodyBytes([]byte(tt.body))
			if err := openapi3filter.ValidateResponse(t.Context(), in); err == nil {
				t.Fatal("validator accepted a response that drifted from the spec")
			}
		})
	}
	if !slices.Contains(unexercisedAll(), "GET /api/v1/movies -> 200") {
		t.Fatal("coverage check does not see listMovies 200 when nothing was exercised")
	}
}

// unexercisedAll is unexercised() against an empty record, for the self-test.
func unexercisedAll() []string {
	spec.mu.Lock()
	saved := spec.seen
	spec.seen = map[string]bool{}
	defer func() { spec.seen = saved; spec.mu.Unlock() }()
	return unexercised()
}
