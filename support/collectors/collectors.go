// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package collectors contains all standard Talos/Kubernetes state collectors used in the support bundle generator.
package collectors

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cosi-project/runtime/pkg/resource/meta"
	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/siderolabs/talos/pkg/machinery/api/common"
	"github.com/siderolabs/talos/pkg/machinery/client"
	"github.com/siderolabs/talos/pkg/machinery/constants"
	"k8s.io/client-go/kubernetes"

	"github.com/siderolabs/go-talos-support/support/bundle"
)

// Cluster is the const for the top level cluster collectors.
const Cluster = "cluster"

// Collect defines a single collect call which returns data blob to be written in the file.
type Collect func() ([]byte, error)

// KubernetesCollect defines a collect function which relies on Kubernetes client.
type KubernetesCollect func(ctx context.Context, log Logger, client *kubernetes.Clientset) Collect

// TalosCollect defines a collect function which relies on Talos client.
type TalosCollect func(ctx context.Context, log Logger, client *client.Client) Collect

// Logger defines a function which logs collector progress.
type Logger func(format string, args ...any)

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
func (c *Collector) Run(archive bundle.Archive) error {
	data, err := c.collect()
	if err != nil {
		return err
	}

	if data == nil {
		return nil
	}

	return archive.Write(c.destinationPath, data)
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

// WithSource returns collectors which custom source name.
func WithSource(collectors []*Collector, source string) []*Collector {
	for _, c := range collectors {
		c.source = source
	}

	return collectors
}

// GetForOptions creates all collectors for the provided bundle options.
func GetForOptions(ctx context.Context, options *bundle.Options) ([]*Collector, error) {
	var (
		collectors []*Collector
		errs       error
	)

	if options.KubernetesClient != nil {
		collectors = append(
			collectors,
			WithSource(
				GetKubernetesCollectors(ctx, options.Log, options.KubernetesClient),
				Cluster,
			)...,
		)
	}

	if options.TalosClientProvider != nil && len(options.Nodes) > 0 {
		for _, node := range options.Nodes {
			nodeCtx, nodeClient, err := options.TalosClientProvider(ctx, node)
			if err != nil {
				errs = errors.Join(errs, fmt.Errorf("error creating Talos client for node %s: %w", node, err))

				continue
			}

			nodeCollectors, err := GetTalosNodeCollectors(nodeCtx, options.Log, nodeClient)
			if err != nil {
				errs = errors.Join(errs, fmt.Errorf("error creating collectors for node %s: %w", node, err))
			}

			collectors = append(collectors, WithFolder(nodeCollectors, node)...)
		}
	}

	return collectors, errs
}

// GetTalosNodeCollectors creates all collectors that rely on using Talos API.
func GetTalosNodeCollectors(ctx context.Context, log Logger, client *client.Client) ([]*Collector, error) {
	var errs error

	talosCollectors := []struct { //nolint:govet
		name    string
		collect TalosCollect
	}{
		{"dmesg.log", dmesg},
		{"controller-runtime.log", logs("controller-runtime", false)},
		{"dns-resolve-cache.log", logs("dns-resolve-cache", false)},
		{"dependencies.dot", dependencies},
		{"mounts", mounts},
		{"devices", devices},
		{"io", ioPressure},
		{"processes", processes},
		{"summary", summary},
	}

	collectors := make([]*Collector, 0, len(talosCollectors))

	for _, talosCollect := range talosCollectors {
		collectors = append(collectors, NewCollector(talosCollect.name, talosCollect.collect(ctx, log, client)))
	}

	resourceCollectors, err := getTalosResources(ctx, log, client)
	if err != nil {
		errs = errors.Join(errs, fmt.Errorf("error creating resource collectors: %w", err))
	}

	collectors = append(collectors, WithFolder(resourceCollectors, "resources")...)

	kubeLogCollectors, err := getKubernetesLogCollectors(ctx, log, client)
	if err != nil {
		errs = errors.Join(errs, fmt.Errorf("error creating kubernetes log collectors: %w", err))
	}

	collectors = append(collectors, WithFolder(kubeLogCollectors, "kubernetes-logs")...)

	serviceLogCollectors, err := getServiceLogCollectors(ctx, log, client)
	if err != nil {
		errs = errors.Join(errs, fmt.Errorf("error creating service log collectors: %w", err))
	}

	collectors = append(collectors, WithFolder(serviceLogCollectors, "service-logs")...)

	return collectors, errs
}

// GetKubernetesCollectors creates all kubernetes API related collectors.
func GetKubernetesCollectors(ctx context.Context, log Logger, client *kubernetes.Clientset) []*Collector {
	return []*Collector{
		NewCollector("kubernetesResources/nodes.yaml", kubernetesNodes(ctx, log, client)),
		NewCollector("kubernetesResources/systemPods.yaml", systemPods(ctx, log, client)),
	}
}

func getTalosResources(ctx context.Context, log Logger, c *client.Client) ([]*Collector, error) {
	rds, err := safe.StateListAll[*meta.ResourceDefinition](ctx, c.COSI)
	if err != nil {
		return nil, err
	}

	var collectors []*Collector

	rds.ForEach(func(res *meta.ResourceDefinition) {
		collectors = append(collectors, NewCollector(
			fmt.Sprintf("%s.yaml", res.Metadata().ID()),
			talosResource(res)(ctx, log, c),
		))
	})

	return collectors, nil
}

func getServiceLogCollectors(ctx context.Context, log Logger, c *client.Client) ([]*Collector, error) {
	resp, err := c.ServiceList(ctx)
	if err != nil {
		return nil, err
	}

	var collectors []*Collector

	for _, msg := range resp.Messages {
		for _, s := range msg.Services {
			collectors = append(
				collectors,
				NewCollector(fmt.Sprintf("%s.log", s.Id), logs(s.Id, false)(ctx, log, c)),
				NewCollector(fmt.Sprintf("%s.state", s.Id), serviceInfo(s.Id)(ctx, log, c)),
			)
		}
	}

	return collectors, nil
}

func getKubernetesLogCollectors(ctx context.Context, log Logger, c *client.Client) ([]*Collector, error) {
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
						fmt.Sprintf("%s/%s%s.log", parts[0], parts[1], exited),
						logs(container.Id, true)(ctx, log, c),
					),
				)
			}
		}
	}

	return collectors, err
}
