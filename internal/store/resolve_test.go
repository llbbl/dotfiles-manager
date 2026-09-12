package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/llbbl/dotfiles-manager/internal/config"
)

func newResolveStore(t *testing.T, ids ...string) (*Store, context.Context) {
	t.Helper()
	cfg := &config.Config{State: config.StateConfig{
		URL: "file://" + filepath.Join(t.TempDir(), "state.db"),
	}}
	ctx := context.Background()
	s, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	for _, id := range ids {
		if _, err := s.DB().ExecContext(ctx,
			`INSERT INTO snapshots (id, file_id, path, hash, size, reason, created_at, storage_path)
			 VALUES (?, NULL, '/x/y', 'deadbeef', 1, 'manual', '2024-01-01T00:00:00Z', '/blob')`,
			id); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	return s, ctx
}

func TestResolveID(t *testing.T) {
	const (
		idA = "01HQAAAAAAAAAAAAAAAAAAAAAA"
		idB = "01HQAAAAAAZZZZZZZZZZZZZZZZ"
		idC = "01HQBBBBBBBBBBBBBBBBBBBBBB"
	)

	tests := []struct {
		name    string
		prefix  string
		want    string
		wantErr error
		wantAmb []string
	}{
		{name: "exact", prefix: idC, want: idC},
		{name: "unique prefix", prefix: "01HQB", want: idC},
		{name: "unique long prefix", prefix: "01HQAAAAAAZ", want: idB},
		{name: "ambiguous", prefix: "01HQAAAAAA", wantAmb: []string{idA, idB}},
		{name: "no match", prefix: "ZZZZ", wantErr: ErrIDNotFound},
		{name: "empty prefix", prefix: "", wantErr: ErrIDNotFound},
		{name: "longer than any id", prefix: idC + "XXXX", wantErr: ErrIDNotFound},
		{name: "like wildcard is literal", prefix: "01HQ%", wantErr: ErrIDNotFound},
	}

	s, ctx := newResolveStore(t, idA, idB, idC)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.ResolveID(ctx, "snapshots", tt.prefix)

			if tt.wantAmb != nil {
				var amb *AmbiguousIDError
				if !errors.As(err, &amb) {
					t.Fatalf("err = %v, want *AmbiguousIDError", err)
				}
				if len(amb.Candidates) != len(tt.wantAmb) {
					t.Fatalf("candidates = %v, want %v", amb.Candidates, tt.wantAmb)
				}
				for i, want := range tt.wantAmb {
					if amb.Candidates[i] != want {
						t.Errorf("candidate[%d] = %s, want %s", i, amb.Candidates[i], want)
					}
				}
				return
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveID: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestResolveID_EmptyTable(t *testing.T) {
	s, ctx := newResolveStore(t)
	if _, err := s.ResolveID(ctx, "snapshots", "01HQ"); !errors.Is(err, ErrIDNotFound) {
		t.Fatalf("err = %v, want ErrIDNotFound", err)
	}
}
