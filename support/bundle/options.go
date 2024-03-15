// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package bundle

import (
	"archive/zip"
	"io"

	"github.com/siderolabs/talos/pkg/machinery/client"
	"k8s.io/client-go/kubernetes"
)

// Option defines a single bundle option.
type Option func(*Options)

// WithTalosClient runs bundle creator with the Talos client.
func WithTalosClient(client *client.Client) Option {
	return func(o *Options) {
		o.TalosClient = client
	}
}

// WithKubernetesClient runs bundle creator with the Kubernetes client.
func WithKubernetesClient(clientset *kubernetes.Clientset) Option {
	return func(o *Options) {
		o.KubernetesClient = clientset
	}
}

// WithLogOutput runs bundle creator with logs output.
func WithLogOutput(writer io.Writer) Option {
	return func(o *Options) {
		o.LogOutput = writer
	}
}

// WithArchiveOutput runs bundle creator with archive output.
func WithArchiveOutput(writer io.Writer) Option {
	return func(o *Options) {
		o.Archive = &archive{
			Archive: zip.NewWriter(writer),
		}
	}
}

// WithArchive runs bundle creator with archive object.
func WithArchive(archive Archive) Option {
	return func(o *Options) {
		o.Archive = archive
	}
}

// WithNumWorkers runs bundle creator with number of workers.
func WithNumWorkers(count int) Option {
	return func(o *Options) {
		o.NumWorkers = count
	}
}

// WithProgressChan runs bundle creator with the progress reporter to the channel.
func WithProgressChan(progress chan Progress) Option {
	return func(o *Options) {
		o.Progress = progress
	}
}

// WithNodes passes the list of nodes to get the data from.
func WithNodes(nodes ...string) Option {
	return func(o *Options) {
		o.Nodes = nodes
	}
}
