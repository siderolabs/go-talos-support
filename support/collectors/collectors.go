// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package collectors contains all standard Talos/Kubernetes state collectors used in the support bundle generator.
package collectors

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cosi-project/runtime/pkg/resource/meta"
	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/cosi-project/runtime/pkg/state"
	"github.com/siderolabs/talos/pkg/machinery/api/common"
	"github.com/siderolabs/talos/pkg/machinery/client"
	"github.com/siderolabs/talos/pkg/machinery/constants"
	"k8s.io/client-go/kubernetes"

	"github.com/siderolabs/go-talos-support/support/bundle"
)

// Cluster is the const for the top level cluster collectors.
const Cluster = "cluster"

// Collect defines a single collect call which returns data blob to be written in the file.
type Collect func(ctx context.Context, options *bundle.Options) ([]byte, error)

// Collector unifies implementation of a the data collector with it's path in the archive.
type Collector struct {
	collect         Collect
	source          string
	destinationPath string
}

// NewCollector creates new collector.
func NewCollector(path string, c Collect) *Collector {
	return &Collector{
		source:          Cluster,
		destinationPath: path,
		collect:         c,
	}
}

// Run executes the collector.
func (c *Collector) Run(ctx context.Context, options *bundle.Options) error {
	data, err := c.collect(ctx, options)
	if err != nil {
		return err
	}

	if data == nil {
		return nil
	}

	return options.Archive.Write(c.destinationPath, data)
}

// Source returns collector source name (Talos node name, cluster, etc).
func (c *Collector) Source() string {
	return c.source
}

// String implements fmt.Stringer interface.
func (c *Collector) String() string {
	return fmt.Sprintf("collect %s", filepath.Base(c.destinationPath))
}

// WithFolder appends path prefix to all collectors.
func WithFolder(collectors []*Collector, path string) []*Collector {
	for _, c := range collectors {
		c.destinationPath = filepath.Join(path, c.destinationPath)
	}

	return collectors
}

// WithNode returns collectors which adds Talos node gRPC metadata to the context.
func WithNode(collectors []*Collector, node string) []*Collector {
	for _, c := range collectors {
		collectFunc := c.collect

		c.collect = func(ctx context.Context, options *bundle.Options) ([]byte, error) {
			return collectFunc(client.WithNode(ctx, node), options)
		}

		c.source = node
		c.destinationPath = filepath.Join(node, c.destinationPath)
	}

	return collectors
}

// WithSource returns collectors which custom source name.
func WithSource(collectors []*Collector, source string) []*Collector {
	for _, c := range collectors {
		c.source = source
	}

	return collectors
}

// GetForOptions creates all collectors for the provided bundle options.
func GetForOptions(ctx context.Context, options *bundle.Options) ([]*Collector, error) {
	var collectors []*Collector

	if options.KubernetesClient != nil {
		collectors = append(collectors, WithSource(GetKubernetesCollectors(options.KubernetesClient), Cluster)...)
	}

	if options.TalosClient != nil && len(options.Nodes) > 0 {
		for _, node := range options.Nodes {
			nodeCollectors, err := GetTalosNodeCollectors(client.WithNode(ctx, node), options.TalosClient)
			if err != nil {
				return nil, err
			}

			collectors = append(collectors, WithNode(nodeCollectors, node)...)
		}
	}

	return collectors, nil
}

// GetTalosNodeCollectors creates all collectors that rely on using Talos API.
func GetTalosNodeCollectors(ctx context.Context, client *client.Client) ([]*Collector, error) {
	base := []*Collector{
		NewCollector("dmesg.log", dmesg),
		NewCollector("controller-runtime.log", logs("controller-runtime", false)),
		NewCollector("dns-resolve-cache.log", logs("dns-resolve-cache", false)),
		NewCollector("dependencies.dot", dependencies),
		NewCollector("mounts", mounts),
		NewCollector("devices", devices),
		NewCollector("io", ioPressure),
		NewCollector("processes", processes),
		NewCollector("summary", summary),
	}

	collectors, err := getTalosResources(ctx, client.COSI)
	if err != nil {
		return nil, err
	}

	base = append(base, WithFolder(collectors, "resources")...)

	collectors, err = getKubernetesLogCollectors(ctx, client)
	if err != nil {
		return nil, err
	}

	base = append(base, WithFolder(collectors, "kubernetes-logs")...)

	collectors, err = getServiceLogCollectors(ctx, client)
	if err != nil {
		return nil, err
	}

	base = append(base, WithFolder(collectors, "service-logs")...)

	return base, nil
}

// GetKubernetesCollectors creates all kubernetes API related collectors.
func GetKubernetesCollectors(client *kubernetes.Clientset) []*Collector {
	return []*Collector{
		NewCollector("kubernetesResources/nodes.yaml", kubernetesNodes(client)),
		NewCollector("kubernetesResources/systemPods.yaml", systemPods(client)),
	}
}

func getTalosResources(ctx context.Context, state state.State) ([]*Collector, error) {
	rds, err := safe.StateListAll[*meta.ResourceDefinition](ctx, state)
	if err != nil {
		return nil, err
	}

	var collectors []*Collector

	rds.ForEach(func(res *meta.ResourceDefinition) {
		collectors = append(collectors, NewCollector(
			fmt.Sprintf("%s.yaml", res.Metadata().ID()),
			talosResource(res),
		))
	})

	return collectors, nil
}

func getServiceLogCollectors(ctx context.Context, c *client.Client) ([]*Collector, error) {
	resp, err := c.ServiceList(ctx)
	if err != nil {
		return nil, err
	}

	var collectors []*Collector

	for _, msg := range resp.Messages {
		for _, s := range msg.Services {
			collectors = append(
				collectors,
				NewCollector(fmt.Sprintf("%s.log", s.Id), logs(s.Id, false)),
				NewCollector(fmt.Sprintf("%s.state", s.Id), serviceInfo(s.Id)),
			)
		}
	}

	return collectors, nil
}

func getKubernetesLogCollectors(ctx context.Context, c *client.Client) ([]*Collector, error) {
	namespace := constants.K8sContainerdNamespace
	driver := common.ContainerDriver_CRI

	resp, err := c.Containers(ctx, namespace, driver)
	if err != nil {
		return nil, err
	}

	var collectors []*Collector

	for _, msg := range resp.Messages {
		for _, container := range msg.Containers {
			parts := strings.Split(container.PodId, "/")

			// skip pause containers
			if container.Status == "SANDBOX_READY" {
				continue
			}

			exited := ""

			if container.Pid == 0 {
				exited = "-exited"
			}

			if parts[0] == "kube-system" {
				collectors = append(
					collectors,
					NewCollector(
						fmt.Sprintf("%s/%s%s.log", parts[0], container.Name, exited),
						logs(container.Id, true),
					),
				)
			}
		}
	}

	return collectors, err
}
