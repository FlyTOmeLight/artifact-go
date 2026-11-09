package lifecycle_test

import (
	"testing"

	"github.com/FlyTOmeLight/artifact-go/internal/lifecycle"
)

func BenchmarkResourceTransitions(b *testing.B) {
	b.Run("file/valid", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			state := "uploading"
			if err := lifecycle.File(&state, "ready"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("file/invalid", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			state := "uploading"
			_ = lifecycle.File(&state, "bound")
		}
	})
	b.Run("reference/valid", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			state := "active"
			if err := lifecycle.Reference(&state, "released"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("version/valid", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			state := "ready"
			if err := lifecycle.Version(&state, "deleting"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("reference_change/commit", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			state := "preparing"
			if err := lifecycle.DecideReferenceChange(&state, "committed"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("reference_attempt/retry", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := lifecycle.ReferenceAttempt("aborted", "att-1", "att-2"); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkTaskTransitions(b *testing.B) {
	b.Run("claim", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			state := "pending"
			if err := lifecycle.Task(&state, lifecycle.ClaimTask); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("complete", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			state := "running"
			if err := lifecycle.Task(&state, lifecycle.CompleteTask); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("fail", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			state := "running"
			if err := lifecycle.Task(&state, lifecycle.FailTask); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("retry", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			state := "running"
			if err := lifecycle.Task(&state, lifecycle.RetryTask); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("recover", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			state := "failed"
			if err := lifecycle.Task(&state, lifecycle.RecoverTask); err != nil {
				b.Fatal(err)
			}
		}
	})
}
