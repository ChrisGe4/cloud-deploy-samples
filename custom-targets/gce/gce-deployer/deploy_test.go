// Copyright 2024 Google LLC

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at

//     https://www.apache.org/licenses/LICENSE-2.0

// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"testing"

	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/proto"
)

// gceMIGGetter is an interface for mocking the GCE client.
type gceMIGGetter interface {
	GetMIG(ctx context.Context, params *migParams, migName string) (*computepb.InstanceGroupManager, error)
}

type mockGceClient struct {
	GceClient
	getMIG func(ctx context.Context, params *migParams, migName string) (*computepb.InstanceGroupManager, error)
}


func (m *mockGceClient) GetMIG(ctx context.Context, params *migParams, migName string) (*computepb.InstanceGroupManager, error) {
	return m.getMIG(ctx, params, migName)
}

func TestGenerateBackendServiceBackends(t *testing.T) {
	fakeMIGName := "test-mig"
	fakeSelfLink := "projects/p/regions/r/instanceGroupManagers/test-mig"
	mockClient := &mockGceClient{
		getMIG: func(ctx context.Context, params *migParams, migName string) (*computepb.InstanceGroupManager, error) {
			return &computepb.InstanceGroupManager{
				Name:     proto.String(fakeMIGName),
				SelfLink: proto.String(fakeSelfLink),
			}, nil
		},
	}
	beTemplate := &computepb.Backend{
		Description: proto.String("test backend"),
	}

	got, err := generateBackendServiceBackends(context.Background(), mockClient, &migParams{}, fakeMIGName, beTemplate)
	if err != nil {
		t.Fatalf("generateBackendServiceBackends() returned error: %v", err)
	}

	want := []*computepb.Backend{
		{
			Description: proto.String("test backend"),
			Group:       proto.String(fakeSelfLink),
		},
	}

	if diff := cmp.Diff(want, got, cmp.Comparer(proto.Equal)); diff != "" {
		t.Errorf("generateBackendServiceBackends() returned diff (-want +got):\n%s", diff)
	}
}
