// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package resources_test

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/cosi-project/runtime/api/v1alpha1"
	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/resource/meta"
	"github.com/cosi-project/runtime/pkg/state"
	"github.com/cosi-project/runtime/pkg/state/impl/inmem"
	"github.com/cosi-project/runtime/pkg/state/impl/namespaced"
	"github.com/cosi-project/runtime/pkg/state/protobuf/server"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/client"
	"github.com/siderolabs/talos/pkg/machinery/constants"
	"github.com/siderolabs/talos/pkg/machinery/redactable"
	"github.com/siderolabs/talos/pkg/machinery/resources/config"
	"github.com/siderolabs/talos/pkg/machinery/resources/network"
	talosruntime "github.com/siderolabs/talos/pkg/machinery/resources/runtime"
	"github.com/siderolabs/talos/pkg/machinery/resources/siderolink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/siderolabs/go-talos-support/support/collectors/internal/resources"
)

const uniqueToken = "v2:4rC0lTkM:5O9dWKDMCV8Pz9MsqkJ1zA=="

// machineService answers the Version call with the feature flags the test is built around.
type machineService struct {
	machine.UnimplementedMachineServiceServer

	resourceRedaction bool
}

func (svc *machineService) Version(context.Context, *emptypb.Empty) (*machine.VersionResponse, error) {
	return &machine.VersionResponse{
		Messages: []*machine.Version{
			{
				Version:  &machine.VersionInfo{Tag: "v1.15.0"},
				Features: &machine.FeaturesInfo{ResourceRedaction: svc.resourceRedaction},
			},
		},
	}, nil
}

// redactingState stands in for the resource read API of the node: it answers an enforcing request
// the way the node does, by redacting what it can and refusing what it can't.
type redactingState struct {
	state.CoreState
}

func (st *redactingState) List(ctx context.Context, kind resource.Kind, opts ...state.ListOption) (resource.List, error) {
	mode, err := redactable.ModeFromIncomingContext(ctx)
	if err != nil {
		return resource.List{}, status.Error(codes.InvalidArgument, err.Error())
	}

	if mode != redactable.ModeEnforce {
		return st.CoreState.List(ctx, kind, opts...)
	}

	// SiderolinkConfig is sensitive and has nothing left of it once the join token is out, so the
	// node refuses to serve it in the enforcing mode, whoever is asking
	if kind.Type() == siderolink.ConfigType {
		return resource.List{}, status.Error(codes.PermissionDenied,
			"resource type \"SiderolinkConfigs.siderolink.talos.dev\" cannot be redacted")
	}

	list, err := st.CoreState.List(ctx, kind, opts...)
	if err != nil {
		return list, err
	}

	for _, r := range list.Items {
		token, ok := r.(*talosruntime.UniqueMachineToken)
		if !ok {
			continue
		}

		token.TypedSpec().Token = constants.Redacted
		token.Metadata().Annotations().Set(constants.AnnotationRedacted, constants.AnnotationRedactedValue)
	}

	return list, nil
}

// setupNode brings up a node which serves the resource read API and the Version call over a socket.
func setupNode(t *testing.T, resourceRedaction bool) (context.Context, *client.Client) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)

	st := namespaced.NewState(inmem.Build)

	for _, r := range []resource.Resource{
		buildUniqueMachineToken(),
		buildSiderolinkConfig(),
		buildHostnameStatus(),
	} {
		require.NoError(t, st.Create(ctx, r))
	}

	socket := filepath.Join(t.TempDir(), "node.sock")

	listener, err := (&net.ListenConfig{}).Listen(ctx, "unix", socket)
	require.NoError(t, err)

	grpcServer := grpc.NewServer()

	v1alpha1.RegisterStateServer(grpcServer, server.NewState(&redactingState{CoreState: st}))
	machine.RegisterMachineServiceServer(grpcServer, &machineService{resourceRedaction: resourceRedaction})

	go func() {
		grpcServer.Serve(listener) //nolint:errcheck
	}()

	t.Cleanup(grpcServer.Stop)

	c, err := client.New(
		ctx,
		client.WithUnixSocket(socket),
		client.WithGRPCDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())),
	)
	require.NoError(t, err)

	t.Cleanup(func() {
		c.Close() //nolint:errcheck
	})

	return ctx, c
}

func buildUniqueMachineToken() *talosruntime.UniqueMachineToken {
	r := talosruntime.NewUniqueMachineToken()
	r.TypedSpec().Token = uniqueToken

	return r
}

func buildSiderolinkConfig() *siderolink.Config {
	r := siderolink.NewConfig(config.NamespaceName, siderolink.ConfigID)
	r.TypedSpec().APIEndpoint = "https://siderolink.example.com"
	r.TypedSpec().JoinToken = "sUpErSeCrEt"

	return r
}

func buildHostnameStatus() *network.HostnameStatus {
	r := network.NewHostnameStatus(network.NamespaceName, network.HostnameID)
	r.TypedSpec().Hostname = "talos-abc-123"
	r.TypedSpec().Domainname = "example.com"

	return r
}

func dump(t *testing.T, ctx context.Context, c *client.Client, rds meta.ResourceDefinitionSpec, redaction bool) string {
	t.Helper()

	rd, err := meta.NewResourceDefinition(rds)
	require.NoError(t, err)

	data, err := resources.Dump(ctx, c, rd, redaction)
	require.NoError(t, err)

	return string(data)
}

// TestDumpNoRedactionSupport covers a node which doesn't know about redaction: the spec of
// every sensitive resource is kept out of the bundle, which is all the collector ever did.
func TestDumpNoRedactionSupport(t *testing.T) {
	t.Parallel()

	ctx, c := setupNode(t, false)

	supported, err := resources.RedactionSupported(ctx, c)
	require.NoError(t, err)
	assert.False(t, supported)

	dumped := dump(t, ctx, c, talosruntime.UniqueMachineTokenExtension{}.ResourceDefinition(), supported)
	assert.Contains(t, dumped, "spec: <REDACTED>")
	assert.NotContains(t, dumped, uniqueToken)
	assert.Contains(t, dumped, talosruntime.UniqueMachineTokenID, "the metadata of the resource is still collected")

	dumped = dump(t, ctx, c, siderolink.ConfigExtension{}.ResourceDefinition(), supported)
	assert.Contains(t, dumped, "spec: <REDACTED>")
	assert.NotContains(t, dumped, "sUpErSeCrEt")

	dumped = dump(t, ctx, c, network.HostnameStatusExtension{}.ResourceDefinition(), supported)
	assert.Contains(t, dumped, "talos-abc-123", "a resource which is not sensitive is collected in full")
}

// TestDumpRedaction covers a node which redacts: a redactable resource is collected with its
// secrets replaced by the node, and one which can't be redacted falls back to the placeholder.
func TestDumpRedaction(t *testing.T) {
	t.Parallel()

	ctx, c := setupNode(t, true)

	supported, err := resources.RedactionSupported(ctx, c)
	require.NoError(t, err)
	assert.True(t, supported)

	dumped := dump(t, ctx, c, talosruntime.UniqueMachineTokenExtension{}.ResourceDefinition(), supported)
	assert.NotContains(t, dumped, "spec: <REDACTED>", "the spec of a redacted resource is collected")
	assert.NotContains(t, dumped, uniqueToken)
	assert.Contains(t, dumped, constants.Redacted)
	assert.Contains(t, dumped, constants.AnnotationRedacted)

	dumped = dump(t, ctx, c, siderolink.ConfigExtension{}.ResourceDefinition(), supported)
	assert.Contains(t, dumped, "spec: <REDACTED>", "a resource the node refuses to redact keeps the placeholder")
	assert.NotContains(t, dumped, "sUpErSeCrEt")
	assert.Contains(t, dumped, siderolink.ConfigID, "the metadata of the resource is still collected")

	dumped = dump(t, ctx, c, network.HostnameStatusExtension{}.ResourceDefinition(), supported)
	assert.Contains(t, dumped, "talos-abc-123", "a resource which is not sensitive is collected in full")
}
