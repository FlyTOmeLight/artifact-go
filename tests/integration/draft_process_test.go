package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func TestTwoProcessesEditDraft(t *testing.T) {
	for _, driver := range []string{db.DriverPostgreSQL, db.DriverMySQL} {
		t.Run(driver, func(t *testing.T) {
			cfg := fixture.Config(t, driver)
			left, right := publishProcess(t, cfg), publishProcess(t, cfg)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			ctx = api.WithRequestIdentity(ctx, api.RequestIdentity{BearerToken: "process-fixture"})
			a := create(t, left, ctx, fmt.Sprintf("draft-process-%d", time.Now().UnixNano()), "model")
			d, e := left.CreateDraft(
				ctx,
				&api.CreateDraftRequest{
					ArtifactPath: api.ArtifactPath{ArtifactID: a.ID},
					RequestKey:   "create",
				},
			)
			if e != nil {
				t.Fatal(e)
			}
			p := api.DraftPath{ArtifactPath: api.ArtifactPath{ArtifactID: a.ID}, DraftID: d.ID}
			start := make(chan struct{})
			result := make(chan error, 2)
			for i, c := range []*api.Client{left, right} {
				go func() {
					<-start
					_, err := c.UpdateDraft(
						ctx,
						&api.UpdateDraftRequest{
							DraftPath:        p,
							RequestKey:       fmt.Sprintf("save-%d", i),
							ExpectedRevision: revision(1),
							Payload:          json.RawMessage(fmt.Sprintf(`{"v":%d}`, i)),
							Annotations:      json.RawMessage(`{}`),
							Files:            []api.FileReference{},
						},
					)
					result <- err
				}()
			}
			close(start)
			wins := 0
			for range 2 {
				if e := <-result; e == nil {
					wins++
				} else {
					expectError(t, e, 409, api.CodeRevisionConflict)
				}
			}
			if wins != 1 {
				t.Fatalf("wins %d", wins)
			}
			current, e := right.GetDraft(ctx, &api.GetDraftRequest{DraftPath: p})
			if e != nil || current.Revision != 2 {
				t.Fatalf("current %+v %v", current, e)
			}
		})
	}
}
