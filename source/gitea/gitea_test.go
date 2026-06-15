package gitea

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "gitea.dev/sdk"
	st "github.com/golang-migrate/migrate/v4/source/testing"
)

type testRepo struct {
	wantAuth bool
}

func Test(t *testing.T) {
	srv := newTestServer(t, testRepo{wantAuth: true})
	defer srv.Close()

	g := &Gitea{}
	d, err := g.Open(context.Background(), fmt.Sprintf("gitea://user:token@%s/owner/repo/migrations?scheme=http", strings.TrimPrefix(srv.URL, "http://")))
	if err != nil {
		t.Fatal(err)
	}

	st.Test(t, d)
}

func TestAnonymous(t *testing.T) {
	srv := newTestServer(t, testRepo{wantAuth: false})
	defer srv.Close()

	g := &Gitea{}
	d, err := g.Open(context.Background(), fmt.Sprintf("gitea://%s/owner/repo/migrations?scheme=http", strings.TrimPrefix(srv.URL, "http://")))
	if err != nil {
		t.Fatal(err)
	}

	st.Test(t, d)
}

func TestWithInstance(t *testing.T) {
	srv := newTestServer(t, testRepo{wantAuth: false})
	defer srv.Close()

	client, err := sdk.NewClient(srv.URL, sdk.SetHTTPClient(srv.Client()), sdk.SetGiteaVersion("1.26.0"))
	if err != nil {
		t.Fatal(err)
	}

	d, err := WithInstance(context.Background(), client, &Config{Owner: "owner", Repo: "repo", Path: "migrations"})
	if err != nil {
		t.Fatal(err)
	}

	st.Test(t, d)
}

func newTestServer(t *testing.T, repo testRepo) *httptest.Server {
	t.Helper()

	files := map[string]string{
		"migrations/1_init.up.sql":     "select 1;\n",
		"migrations/1_init.down.sql":   "select 1;\n",
		"migrations/3_add.up.sql":      "select 3;\n",
		"migrations/4_add.up.sql":      "select 4;\n",
		"migrations/4_add.down.sql":    "select 4;\n",
		"migrations/5_remove.down.sql": "select 5;\n",
		"migrations/7_more.up.sql":     "select 7;\n",
		"migrations/7_more.down.sql":   "select 7;\n",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/version", func(w http.ResponseWriter, r *http.Request) {
		assertAuth(t, r, repo.wantAuth)
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method %s", r.Method)
		}
		if _, err := io.WriteString(w, `{"version":"1.26.0"}`); err != nil {
			t.Fatal(err)
		}
	})
	mux.HandleFunc("/api/v1/repos/owner/repo", func(w http.ResponseWriter, r *http.Request) {
		assertAuth(t, r, repo.wantAuth)
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method %s", r.Method)
		}
		if _, err := io.WriteString(w, `{"default_branch":"main"}`); err != nil {
			t.Fatal(err)
		}
	})
	mux.HandleFunc("/api/v1/repos/owner/repo/contents/migrations", func(w http.ResponseWriter, r *http.Request) {
		assertAuth(t, r, repo.wantAuth)
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method %s", r.Method)
		}
		entries := make([]map[string]any, 0, len(files))
		for path := range files {
			if !strings.HasPrefix(path, "migrations/") {
				continue
			}
			name := strings.TrimPrefix(path, "migrations/")
			entries = append(entries, map[string]any{
				"name": name,
				"path": path,
				"type": "file",
			})
		}
		_ = json.NewEncoder(w).Encode(entries)
	})
	mux.HandleFunc("/api/v1/repos/owner/repo/raw/", func(w http.ResponseWriter, r *http.Request) {
		assertAuth(t, r, repo.wantAuth)
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method %s", r.Method)
		}

		path := strings.TrimPrefix(r.URL.Path, "/api/v1/repos/owner/repo/raw/")
		path = strings.TrimPrefix(path, "main/")
		path = strings.TrimPrefix(path, "/")
		rawPath := path
		if body, ok := files[rawPath]; ok {
			if _, err := io.WriteString(w, body); err != nil {
				t.Fatal(err)
			}
			return
		}
		http.NotFound(w, r)
	})

	return httptest.NewServer(mux)
}

func assertAuth(t *testing.T, r *http.Request, want bool) {
	t.Helper()

	got := r.Header.Get("Authorization") != ""
	if got != want {
		t.Fatalf("authorization header presence mismatch: got %v want %v", got, want)
	}
}
