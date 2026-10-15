package storagehandoff

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	serrors "github.com/FlyTOmeLight/server-go/errors"
)

type Consumer struct {
	ID     string
	Tokens []string
	Scopes []Scope
}

type handler struct {
	service   BindingService
	consumers []Consumer
}

func NewHandler(service BindingService, consumers []Consumer) (http.Handler, error) {
	if service == nil || len(consumers) == 0 {
		return nil, errors.New("storage internal service and consumers required")
	}
	seen := map[string]bool{}
	ids := map[string]bool{}
	copied := make([]Consumer, len(consumers))
	for i, c := range consumers {
		if !validIdentity(c.ID, 64) || ids[c.ID] || len(c.Tokens) == 0 || len(c.Scopes) == 0 {
			return nil, errors.New("invalid storage consumer configuration")
		}
		ids[c.ID] = true
		copied[i] = Consumer{ID: c.ID, Tokens: append([]string(nil), c.Tokens...)}
		for _, t := range c.Tokens {
			if strings.TrimSpace(t) == "" || strings.ContainsAny(t, "\r\n") || seen[t] {
				return nil, errors.New("invalid or duplicate storage consumer token")
			}
			seen[t] = true
		}
		for _, s := range c.Scopes {
			if !validScope(s) {
				return nil, errors.New("invalid storage consumer scope")
			}
			if s.ProjectID != nil {
				p := *s.ProjectID
				s.ProjectID = &p
			}
			copied[i].Scopes = append(copied[i].Scopes, s)
		}
	}
	return &handler{service: service, consumers: copied}, nil
}

func validScope(s Scope) bool {
	return validIdentity(s.TenantID, 64) && (s.ProjectID == nil || validIdentity(*s.ProjectID, 64))
}

func sameScope(a, b Scope) bool {
	return a.TenantID == b.TenantID &&
		((a.ProjectID == nil && b.ProjectID == nil) || (a.ProjectID != nil && b.ProjectID != nil && *a.ProjectID == *b.ProjectID))
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	fail := func(status int, code string) { writeProblem(w, r, serrors.NewError(status, code, code)) }
	var consumer *Consumer
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		token := strings.TrimPrefix(auth, "Bearer ")
		for i := range h.consumers {
			for _, t := range h.consumers[i].Tokens {
				if subtle.ConstantTimeCompare([]byte(token), []byte(t)) == 1 {
					consumer = &h.consumers[i]
				}
			}
		}
	}
	if consumer == nil {
		fail(401, "Unauthenticated")
		return
	}
	if r.Method != "POST" {
		fail(405, "InvalidRequest")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/internal/v1/storage/")
	if path == r.URL.Path ||
		(path != "reference-files/query" && path != "references/bind" && path != "references/release" && path != "references/query") {
		fail(404, "NotFound")
		return
	}
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		fail(400, "InvalidRequest")
		return
	}
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if e != nil {
		var big *http.MaxBytesError
		if errors.As(e, &big) {
			fail(413, "RequestBodyTooLarge")
		} else {
			fail(400, "InvalidRequest")
		}
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		fail(400, "InvalidRequest")
		return
	}
	var scopeFields map[string]json.RawMessage
	if json.Unmarshal(fields["scope"], &scopeFields) != nil || scopeFields == nil ||
		scopeFields["tenantID"] == nil ||
		scopeFields["projectID"] == nil {
		fail(400, "InvalidRequest")
		return
	}
	var scope Scope
	var ids []string
	var refs []Reference
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if path == "reference-files/query" {
		var q MetadataRequest
		e = decoder.Decode(&q)
		scope, ids = q.Scope, q.FileIDs
		if len(ids) == 0 || len(ids) > 1000 {
			e = errors.New("invalid batch")
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if !validIdentity(id, 36) || seen[id] {
				e = errors.New("invalid ID")
			}
			seen[id] = true
		}
	} else {
		var q ReferencesRequest
		e = decoder.Decode(&q)
		scope, refs = q.Scope, q.References
		if len(refs) == 0 || len(refs) > 1000 {
			e = errors.New("invalid batch")
		}
		seen := map[string]bool{}
		for _, ref := range refs {
			if !validIdentity(ref.ReferenceID, 36) || !validIdentity(ref.FileID, 36) ||
				seen[ref.ReferenceID] {
				e = errors.New("invalid reference")
			}
			seen[ref.ReferenceID] = true
		}
	}
	if e != nil || !validScope(scope) {
		fail(400, "InvalidRequest")
		return
	}
	allowed := false
	for _, s := range consumer.Scopes {
		allowed = allowed || sameScope(scope, s)
	}
	if !allowed {
		fail(403, "ScopeForbidden")
		return
	}
	var out any
	switch path {
	case "reference-files/query":
		out, e = h.service.Metadata(r.Context(), consumer.ID, scope, ids)
	case "references/bind":
		out, e = h.service.Bind(r.Context(), consumer.ID, scope, refs)
	case "references/release":
		out, e = h.service.Release(r.Context(), consumer.ID, scope, refs)
	case "references/query":
		out, e = h.service.Statuses(r.Context(), consumer.ID, scope, refs)
	}
	if e != nil {
		writeProblem(w, r, e)
		return
	}
	w.WriteHeader(200)
	_ = json.NewEncoder(w).Encode(out)
}

func writeProblem(w http.ResponseWriter, r *http.Request, err error) {
	var p *serrors.Error
	if !errors.As(err, &p) {
		p = &serrors.Error{Status: 503, Code: "StorageUnavailable", Message: "storage is unavailable"}
	}
	copy := *p
	p = &copy
	p.SetStatusCode(0)
	p.RequestID = r.Header.Get("X-Request-ID")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

func validIdentity(s string, max int) bool {
	return s != "" && strings.TrimSpace(s) == s && utf8.RuneCountInString(s) <= max &&
		!strings.ContainsAny(s, "\x00\r\n")
}
