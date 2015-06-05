package testutils

import (
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/random"
)

type FakeControllerClient struct {
}

func NewFakeControllerClient() *FakeControllerClient {
	return &FakeControllerClient{}
}

func (f *FakeControllerClient) GetRelease(releaseID string) (*ct.Release, error) {
	return &ct.Release{
		ID:         releaseID,
		ArtifactID: random.UUID(),
		Env:        make(map[string]string),
		Processes:  make(map[string]ct.ProcessType),
	}, nil
}

func (f *FakeControllerClient) GetArtifact(artifactID string) (*ct.Artifact, error) {
	return &ct.Artifact{
		ID:   artifactID,
		Type: "fake",
		URI:  "",
	}, nil
}

func (f *FakeControllerClient) GetFormation(appID, releaseID string) (*ct.Formation, error) {
	return &ct.Formation{
		AppID:     appID,
		ReleaseID: releaseID,
		Processes: map[string]int{"fake": 0},
	}, nil
}
