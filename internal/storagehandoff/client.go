package storagehandoff

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type ClientConfig struct{ Endpoint, Token string }

type Client struct {
	endpoint, token string
	http            *http.Client
}

func NewClient(c ClientConfig) (*Client, error) {
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") {
		return nil, errors.New("invalid storage handoff endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	local := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return nil, errors.New("storage handoff requires TLS outside loopback")
	}
	if strings.TrimSpace(c.Token) == "" || strings.ContainsAny(c.Token, "\r\n") {
		return nil, errors.New("storage handoff token is required")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: time.Second}).DialContext
	return &Client{
		endpoint: strings.TrimRight(c.Endpoint, "/"),
		token:    c.Token,
		http: &http.Client{
			Transport:     tr,
			Timeout:       5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) Metadata(ctx context.Context, s Scope, ids []string) (out []FileMetadata, err error) {
	err = c.call(ctx, "reference-files/query", MetadataRequest{Scope: s, FileIDs: ids}, &out, func() bool {
		if len(out) != len(ids) {
			return false
		}
		for i, v := range out {
			if v.FileID != ids[i] || v.OriginalExpiresAt.IsZero() || !fileState(v.State) ||
				(v.Size != nil && *v.Size < 0) ||
				((v.State == "ready" || v.State == "bound") && v.Size == nil) {
				return false
			}
		}
		return true
	})
	return
}

func (c *Client) Bind(ctx context.Context, s Scope, refs []Reference) (out []BindResult, err error) {
	err = c.call(ctx, "references/bind", ReferencesRequest{Scope: s, References: refs}, &out, func() bool {
		if len(out) != len(refs) {
			return false
		}
		for i, v := range out {
			if v.ReferenceID != refs[i].ReferenceID || v.FileID != refs[i].FileID || v.State != "active" ||
				v.File.Size == nil ||
				*v.File.Size < 0 ||
				v.File.OriginalExpiresAt.IsZero() {
				return false
			}
		}
		return true
	})
	return
}

func (c *Client) Release(ctx context.Context, s Scope, refs []Reference) ([]Status, error) {
	return c.status(ctx, "references/release", s, refs, true)
}

func (c *Client) Statuses(ctx context.Context, s Scope, refs []Reference) ([]Status, error) {
	return c.status(ctx, "references/query", s, refs, false)
}

func (c *Client) status(
	ctx context.Context,
	path string,
	s Scope,
	refs []Reference,
	released bool,
) (out []Status, err error) {
	err = c.call(ctx, path, ReferencesRequest{Scope: s, References: refs}, &out, func() bool {
		if len(out) != len(refs) {
			return false
		}
		for i, v := range out {
			if v.ReferenceID != refs[i].ReferenceID || v.FileID != refs[i].FileID {
				return false
			}
			switch v.State {
			case "absent", "active":
				if released || v.ReleaseComplete || v.ReleaseRequiresDeletion != nil {
					return false
				}
			case "released":
				if v.ReleaseRequiresDeletion == nil ||
					(!*v.ReleaseRequiresDeletion && !v.ReleaseComplete) {
					return false
				}
			default:
				return false
			}
		}
		return true
	})
	return
}

func fileState(s string) bool {
	return s == "uploading" || s == "ready" || s == "bound" || s == "deleting" || s == "deleted"
}

func (c *Client) call(ctx context.Context, path string, input, output any, valid func() bool) error {
	body, e := json.Marshal(input)
	if e != nil {
		return e
	}
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		req, e := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			c.endpoint+"/internal/v1/storage/"+path,
			bytes.NewReader(body),
		)
		if e != nil {
			return e
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")
		res, e := c.http.Do(req)
		delay := time.Duration(200) * time.Millisecond
		if attempt > 0 {
			delay = 500 * time.Millisecond
		}
		delay += time.Duration(rand.IntN(101)) * time.Millisecond
		if e != nil {
			last = &Error{Status: 503, Code: "StorageOutcomeUnknown", OutcomeUnknown: true, Cause: e}
		} else {
			raw, readErr := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
			res.Body.Close()
			if res.StatusCode == 200 && readErr == nil && len(raw) <= 8<<20 {
				d := json.NewDecoder(bytes.NewReader(raw))
				d.DisallowUnknownFields()
				var extra any
				if validResponseShape(path, raw) && d.Decode(output) == nil &&
					d.Decode(&extra) == io.EOF &&
					valid() {
					return nil
				}
			}
			code := "StorageOutcomeUnknown"
			var problem struct {
				Code string `json:"code"`
			}
			problemValid := json.Unmarshal(raw, &problem) == nil && problem.Code != "" && readErr == nil &&
				len(raw) <= 8<<20
			unknown := !problemValid || res.StatusCode >= 500 || res.StatusCode < 400
			if problemValid && res.StatusCode != 200 {
				code = problem.Code
			}
			last = &Error{Status: res.StatusCode, Code: code, OutcomeUnknown: unknown, Cause: readErr}
			if !unknown && res.StatusCode >= 400 && res.StatusCode < 500 && res.StatusCode != 429 {
				return last
			}
			if res.StatusCode == 429 {
				if seconds, e := strconv.Atoi(res.Header.Get("Retry-After")); e == nil && seconds >= 0 {
					if seconds > int((1<<63-1)/int64(time.Second)) {
						return last
					}
					delay = max(delay, time.Duration(seconds)*time.Second)
				} else if date, e := http.ParseTime(res.Header.Get("Retry-After")); e == nil {
					delay = max(delay, time.Until(date))
				}
			}
		}
		if ctx.Err() != nil {
			return &Error{
				Status:         503,
				Code:           "StorageOutcomeUnknown",
				OutcomeUnknown: true,
				Cause:          ctx.Err(),
			}
		}
		deadline, ok := ctx.Deadline()
		if attempt == 2 || !ok || time.Until(deadline) <= delay {
			return last
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return &Error{
				Status:         503,
				Code:           "StorageOutcomeUnknown",
				OutcomeUnknown: true,
				Cause:          ctx.Err(),
			}
		case <-timer.C:
		}
	}
	return last
}

// Presence checks distinguish omitted zero-valued receipt fields from explicit values.
func validResponseShape(path string, raw []byte) bool {
	var rows []map[string]json.RawMessage
	if json.Unmarshal(raw, &rows) != nil || rows == nil {
		return false
	}
	for _, row := range rows {
		fields := []string{"referenceID", "fileID", "state", "releaseComplete", "releaseRequiresDeletion"}
		if path == "reference-files/query" {
			fields = []string{
				"fileID",
				"state",
				"size",
				"contentType",
				"declaredChecksum",
				"originalExpiresAt",
			}
		}
		if path == "references/bind" {
			fields = []string{"referenceID", "fileID", "state", "file"}
			var file map[string]json.RawMessage
			if json.Unmarshal(row["file"], &file) != nil || file == nil {
				return false
			}
			for _, k := range []string{"size", "contentType", "declaredChecksum", "originalExpiresAt"} {
				if file[k] == nil {
					return false
				}
			}
		}
		for _, k := range fields {
			if row[k] == nil {
				return false
			}
			if k != "releaseRequiresDeletion" && k != "size" && k != "declaredChecksum" &&
				bytes.Equal(bytes.TrimSpace(row[k]), []byte("null")) {
				return false
			}
		}
	}
	return true
}
