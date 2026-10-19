package workspace

import (
	"context"
	"errors"
	"testing"

	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

type fakeRepository struct {
	payload domainworkspace.Payload
	loadErr error
	saveErr error
	saved   *domainworkspace.Payload
	cleared bool
}

func (f *fakeRepository) Load(context.Context) (domainworkspace.Payload, error) {
	return f.payload, f.loadErr
}

func (f *fakeRepository) Save(_ context.Context, payload domainworkspace.Payload) error {
	f.saved = &payload
	return f.saveErr
}

func (f *fakeRepository) Clear(context.Context) error {
	f.cleared = true
	return nil
}

func TestServiceSanitizesWhatItLoadsAndSaves(t *testing.T) {
	repo := &fakeRepository{payload: domainworkspace.Payload{
		Version: 99,
		Tabs:    []domainworkspace.RestoredTab{{ID: "t", Root: domainworkspace.NewPaneNode("p")}},
	}}
	service := NewService(repo)

	if got := service.Load(context.Background()); len(got.Tabs) != 0 || got.Version != domainworkspace.PayloadVersion {
		t.Fatalf("loaded payload = %+v, want a sanitized empty payload", got)
	}

	valid := domainworkspace.EmptyPayload()
	valid.Tabs = []domainworkspace.RestoredTab{{
		ID:   "t",
		Root: domainworkspace.NewPaneNode("p"),
	}}
	valid.Panes = []domainworkspace.RestoredPane{{
		ID:      "p",
		Session: domainworkspace.SessionDescriptor{Kind: domainworkspace.SessionLocal},
	}}
	if err := service.Save(context.Background(), valid); err != nil {
		t.Fatal(err)
	}
	if repo.saved == nil || len(repo.saved.Tabs) != 1 {
		t.Fatalf("saved = %+v, want the valid workspace", repo.saved)
	}
}

func TestServiceReportsStorageFailuresButLoadFailureIsEmpty(t *testing.T) {
	repo := &fakeRepository{loadErr: errors.New("read failed")}
	service := NewService(repo)
	if got := service.Load(context.Background()); len(got.Tabs) != 0 {
		t.Fatalf("load with an IO error = %+v, want an empty workspace", got)
	}

	repo.saveErr = errors.New("write failed")
	if err := service.Save(context.Background(), domainworkspace.EmptyPayload()); !errors.Is(err, repo.saveErr) {
		t.Fatalf("save error = %v, want the storage failure", err)
	}
}
