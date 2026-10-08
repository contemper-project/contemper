package source_test

import (
	"encoding/base64"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/contemper-project/contemper/internal/source"
)

// authRegistry is an in-process registry that records, per request path,
// whether the request carried an Authorization header. Its /v2/ endpoint
// always asks for Basic credentials, and repositories with a "private"
// path component refuse requests that carry none.
type authRegistry struct {
	host string

	mu   sync.Mutex
	seen map[string][]bool // request path -> has Authorization, per request
}

func newAuthRegistry(t *testing.T) *authRegistry {
	t.Helper()
	inner := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	ar := &authRegistry{seen: map[string][]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hasAuth := r.Header.Get("Authorization") != ""
		ar.mu.Lock()
		ar.seen[r.URL.Path] = append(ar.seen[r.URL.Path], hasAuth)
		ar.mu.Unlock()
		private := strings.Contains(r.URL.Path, "/private/")
		if !hasAuth && (r.URL.Path == "/v2/" || private) {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ar.host = u.Host
	return ar
}

// push stores a test image at repoTag, authenticating explicitly so the
// keychain configured by useKeychain is not involved.
func (ar *authRegistry) push(t *testing.T, repoTag string) string {
	t.Helper()
	ref := ar.host + "/" + repoTag
	tag, err := name.NewTag(ref, name.WeakValidation)
	if err != nil {
		t.Fatal(err)
	}
	basic := remote.WithAuth(staticBasic{})
	if err := remote.Write(tag, testImage(t, "amd64"), basic); err != nil {
		t.Fatalf("pushing %s: %v", ref, err)
	}
	return ref
}

// reset forgets every request recorded so far.
func (ar *authRegistry) reset() {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	ar.seen = map[string][]bool{}
}

// withAuthFor returns the recorded request paths under prefix that
// carried an Authorization header, and how many requests there were.
func (ar *authRegistry) withAuthFor(prefix string) (authed []string, total int) {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	for p, hs := range ar.seen {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		for _, h := range hs {
			total++
			if h {
				authed = append(authed, p)
			}
		}
	}
	return authed, total
}

// useKeychain gives the default keychain credentials for the registry,
// through a Docker config in a temporary directory.
func (ar *authRegistry) useKeychain(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	auth := base64.StdEncoding.EncodeToString([]byte("user:secret"))
	cfg := `{"auths":{"` + ar.host + `":{"auth":"` + auth + `"}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", dir)
}

// fetchAll loads ref and reads its config and every layer, so all the
// requests a real pull makes have been issued.
func fetchAll(t *testing.T, ref source.Ref) error {
	t.Helper()
	img, err := source.Load(t.Context(), ref, v1.Platform{OS: "linux", Architecture: "amd64"})
	if err != nil {
		return err
	}
	defer img.Close()
	if _, err := img.Image.ConfigFile(); err != nil {
		return err
	}
	layers, err := img.Image.Layers()
	if err != nil {
		return err
	}
	for _, l := range layers {
		rc, err := l.Compressed()
		if err != nil {
			return err
		}
		_, err = io.Copy(io.Discard, rc)
		_ = rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func TestVariantOutsideNamespaceIsFetchedAnonymously(t *testing.T) {
	ar := newAuthRegistry(t)
	// The realistic derived case: a provider's support image whose
	// labels point at the published variants in another namespace.
	parent := source.Ref{Kind: source.KindRegistry, Value: ar.host + "/provider/incus-support:v1"}
	published := ar.push(t, "contemper-project/incus-support-openrc:v1")
	own := ar.push(t, "provider/my-variant:v1")
	private := ar.push(t, "contemper-project/private/variant:v1")
	privateOwn := ar.push(t, "provider/private/variant:v1")

	ar.useKeychain(t)
	ar.reset()

	pull := func(raw string) error {
		t.Helper()
		ref, err := source.ParseVariantRef(parent, raw)
		if err != nil {
			t.Fatalf("ParseVariantRef(%q): %v", raw, err)
		}
		return fetchAll(t, ref)
	}

	// Another namespace: loads, and no request carries credentials even
	// though the keychain has them for this very host.
	if err := pull(published); err != nil {
		t.Fatalf("outside-namespace variant: %v", err)
	}
	if authed, total := ar.withAuthFor("/v2/contemper-project/"); len(authed) != 0 || total == 0 {
		t.Errorf("outside-namespace variant: %d requests, authenticated: %v; want some, none authenticated", total, authed)
	}

	// A private image there fails with an error that says why.
	err := pull(private)
	if err == nil {
		t.Fatal("private image outside the namespace was pulled")
	}
	if !strings.Contains(err.Error(), "without credentials") || !strings.Contains(err.Error(), "namespace") {
		t.Errorf("error %q should say the variant is outside the namespace and was fetched without credentials", err)
	}
	if authed, _ := ar.withAuthFor("/v2/contemper-project/"); len(authed) != 0 {
		t.Errorf("private outside-namespace image saw authenticated requests: %v", authed)
	}

	// The same namespace keeps the user's credentials, so a private
	// variant next to a private support image works.
	ar.reset()
	if err := pull(own); err != nil {
		t.Fatalf("same-namespace variant: %v", err)
	}
	if authed, _ := ar.withAuthFor("/v2/provider/"); len(authed) == 0 {
		t.Error("same-namespace variant was fetched without credentials")
	}
	if err := pull(privateOwn); err != nil {
		t.Errorf("private same-namespace variant: %v", err)
	}
}

// staticBasic is an authenticator with fixed credentials.
type staticBasic struct{}

func (staticBasic) Authorization() (*authn.AuthConfig, error) {
	return &authn.AuthConfig{Username: "user", Password: "secret"}, nil
}

func TestIndexedVariantOutsideNamespaceIsAnonymous(t *testing.T) {
	ar := newAuthRegistry(t)
	amd64 := v1.Platform{OS: "linux", Architecture: "amd64"}
	idx := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{
		Add: testImage(t, "amd64"),
		Descriptor: v1.Descriptor{
			Platform:    &amd64,
			Annotations: map[string]string{"k": "v"},
		},
	})
	refStr := ar.host + "/contemper-project/indexed:v1"
	tag, err := name.NewTag(refStr, name.WeakValidation)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(tag, idx, remote.WithAuth(staticBasic{})); err != nil {
		t.Fatal(err)
	}

	ar.useKeychain(t)
	ar.reset()

	parent := source.Ref{Kind: source.KindRegistry, Value: ar.host + "/provider/support:v1"}
	ref, err := source.ParseVariantRef(parent, refStr)
	if err != nil {
		t.Fatal(err)
	}
	if !ref.Anonymous {
		t.Fatal("variant should be anonymous")
	}
	if anns, err := source.IndexAnnotations(t.Context(), ref, amd64); err != nil || anns["k"] != "v" {
		t.Errorf("IndexAnnotations = %v, %v", anns, err)
	}
	if ps, err := source.Platforms(t.Context(), ref); err != nil || len(ps) != 1 {
		t.Errorf("Platforms = %v, %v", ps, err)
	}
	if err := fetchAll(t, ref); err != nil {
		t.Fatalf("Load through the index: %v", err)
	}
	authed, total := ar.withAuthFor("/v2/contemper-project/")
	if len(authed) != 0 || total == 0 {
		t.Errorf("%d requests, authenticated: %v; want some, none authenticated", total, authed)
	}
}
