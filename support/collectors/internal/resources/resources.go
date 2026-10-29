// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package resources dumps the Talos resources into the support bundle, keeping their secrets out of it.
package resources

import (
	"bytes"
	"context"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/resource/meta"
	"github.com/cosi-project/runtime/pkg/state"
	"github.com/siderolabs/talos/pkg/machinery/client"
	"github.com/siderolabs/talos/pkg/machinery/redactable"
	"go.yaml.in/yaml/v4"
	"google.golang.org/grpc/codes"
)

// RedactionSupported reports whether the node redacts the secrets out of the resource specs it
// serves, which is what tells the collector whether it can dump a sensitive resource at all.
//
// Talos before 1.15 has no such thing, and the feature flag it reports is the answer rather than a
// version comparison, as the client has no way to tell a backport from a release which never had it.
func RedactionSupported(ctx context.Context, c *client.Client) (bool, error) {
	resp, err := c.Version(ctx)
	if err != nil {
		return false, err
	}

	if len(resp.GetMessages()) == 0 {
		return false, nil
	}

	for _, m := range resp.GetMessages() {
		if !m.GetFeatures().GetResourceRedaction() {
			return false, nil
		}
	}

	return true, nil
}

// Dump collects a single resource type into the bundle.
//
// A support bundle is handed around, so it may not carry the secrets of the machine configuration.
// Which is why, before redaction existed, the spec of every resource of a sensitive type was replaced
// with a placeholder here: the bundle kept the metadata, so the reader still saw that the resource was
// there, and lost everything the spec had to say about it.
//
// A node which supports redaction does better: it is asked to redact whatever it can, whoever is
// asking, and what comes back either had its secrets replaced or had none to begin with, so it is
// dumped as it is. The sensitivity of the type is not consulted at all, as the node has already
// decided what is safe to hand out, per field, rather than per type.
//
// A sensitive type which the node can't redact -- one with nothing meaningful left of it once the
// secrets are out -- is refused in the enforcing mode, and that refusal is what brings the collector
// back to the placeholder it wrote before.
func Dump(ctx context.Context, c *client.Client, rd *meta.ResourceDefinition, redaction bool) ([]byte, error) {
	if redaction {
		resources, err := list(redactable.WithMode(ctx, redactable.ModeEnforce), c, rd)
		if err == nil {
			return encode(resources, true)
		}

		if client.StatusCode(err) != codes.PermissionDenied {
			return nil, err
		}
	}

	resources, err := list(ctx, c, rd)
	if err != nil {
		return nil, err
	}

	return encode(resources, rd.TypedSpec().Sensitivity != meta.Sensitive)
}

func list(ctx context.Context, c *client.Client, rd *meta.ResourceDefinition) (resource.List, error) {
	return c.COSI.List(
		ctx, resource.NewMetadata(rd.TypedSpec().DefaultNamespace, rd.TypedSpec().Type, "", resource.VersionUndefined),
		state.WithListUnmarshalOptions(state.WithSkipProtobufUnmarshal()),
	)
}

// encode marshals the resources into the bundle: the metadata of each one, followed by its spec, or
// by a placeholder when the spec is to be kept out of the bundle.
func encode(resources resource.List, withSpecs bool) ([]byte, error) {
	if len(resources.Items) == 0 {
		return nil, nil
	}

	var buf bytes.Buffer

	encoder := yaml.NewEncoder(&buf)

	for _, r := range resources.Items {
		data := struct {
			Metadata *resource.Metadata `yaml:"metadata"`
			Spec     any                `yaml:"spec"`
		}{
			Metadata: r.Metadata(),
			Spec:     "<REDACTED>",
		}

		if withSpecs {
			data.Spec = r.Spec()
		}

		if err := encoder.Encode(&data); err != nil {
			return nil, err
		}
	}

	if err := encoder.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
