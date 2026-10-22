package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestLoaderMetadataDoesNotExposeCredentialsOrGrantProfile(t *testing.T) {
	s, _ := newTestServer(t)
	t.Setenv("OPG3D_FRONTEND_DIR", t.TempDir())
	t.Setenv("OPG3D_SERVER_COUNTRY", "ru")
	r := httptest.NewRecorder()
	s.mux.ServeHTTP(r, httptest.NewRequest("GET", "/api/v1/loader/info", nil))
	if r.Code != 200 {
		t.Fatal(r.Code)
	}
	var v map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v["game_version"] != "23.1.3" || v["protocol"] != float64(1) || v["country"] != "RU" || v["release_available"] != false {
		t.Fatal(v)
	}
	for _, k := range []string{"session", "admin_token", "photon_app_ids", "key"} {
		if v[k] != nil {
			t.Fatal("secret metadata")
		}
	}
	for _, path := range []string{"/api/v1/frontend/manifest", "/api/v1/frontend/abcd/library.so"} {
		r = httptest.NewRecorder()
		s.mux.ServeHTTP(r, httptest.NewRequest("GET", path, nil))
		if r.Code < 400 {
			t.Fatal("unpublished release served")
		}
	}
	t.Setenv("OPG3D_SERVER_COUNTRY", "\r\ninvalid")
	r = httptest.NewRecorder()
	s.mux.ServeHTTP(r, httptest.NewRequest("GET", "/api/v1/loader/info", nil))
	json.Unmarshal(r.Body.Bytes(), &v)
	if v["country"] != "ZZ" {
		t.Fatal(v)
	}
}
