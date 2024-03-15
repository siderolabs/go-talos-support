// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package collectors

import (
	"bytes"
	"context"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/client-go/kubernetes"

	"github.com/siderolabs/go-talos-support/support/bundle"
)

func kubernetesNodes(client *kubernetes.Clientset) Collect {
	return func(ctx context.Context, options *bundle.Options) ([]byte, error) {
		options.Log("getting kubernetes nodes manifests")

		nodes, err := client.CoreV1().Nodes().List(ctx, v1.ListOptions{})
		if err != nil {
			return nil, err
		}

		return marshalKubernetesResources(nodes)
	}
}

func systemPods(client *kubernetes.Clientset) Collect {
	return func(ctx context.Context, options *bundle.Options) ([]byte, error) {
		options.Log("getting pods manifests in kube-system namespace")

		nodes, err := client.CoreV1().Pods("kube-system").List(ctx, v1.ListOptions{})
		if err != nil {
			return nil, err
		}

		return marshalKubernetesResources(nodes)
	}
}

func marshalKubernetesResources(resource runtime.Object) ([]byte, error) {
	serializer := json.NewSerializerWithOptions(
		json.DefaultMetaFactory, nil, nil,
		json.SerializerOptions{
			Yaml:   true,
			Pretty: true,
			Strict: true,
		},
	)

	var buf bytes.Buffer

	if err := serializer.Encode(resource, &buf); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
