package forgetenant_test

import (
	"fmt"
	"net/http"
	"testing"
)

func TestResourceUserSelection(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(
			w,
			`{"principal":{"id":"user"},"user_id":"user","tenant_id":"tenant","project_id":null,"ownership_state":"active"}`,
		)
	}, 0)
	r := request(t, http.MethodGet, "")
	got, err := client.Resolve(r)
	if err != nil || got.ResourceUserID != nil {
		t.Fatalf("shared: %+v %v", got, err)
	}
	r.Header.Set("X-Resource-User-ID", "user")
	got, err = client.Resolve(r)
	if err != nil || got.UserID != "user" || got.ResourceUserID == nil || *got.ResourceUserID != "user" {
		t.Fatalf("personal: %+v %v", got, err)
	}
	r.Header.Set("X-Resource-User-ID", "other-user")
	_, err = client.Resolve(r)
	expectError(t, err, 403, "ScopeAccessDenied")
	for _, value := range []string{"", " ", "\t", "\n", " user", "user ", "user\t", "\u00a0", "\u00a0user"} {
		r.Header.Set("X-Resource-User-ID", value)
		_, err = client.Resolve(r)
		expectError(t, err, 400, "InvalidScope")
	}
	r.Header.Set("X-Resource-User-ID", "user")
	r.Header.Add("X-Resource-User-ID", "user")
	_, err = client.Resolve(r)
	expectError(t, err, 400, "InvalidScope")
}
