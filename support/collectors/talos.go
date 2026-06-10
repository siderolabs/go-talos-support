// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package collectors

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/resource/meta"
	"github.com/cosi-project/runtime/pkg/state"
	"github.com/dustin/go-humanize"
	"github.com/siderolabs/talos/pkg/machinery/api/common"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/client"
	"github.com/siderolabs/talos/pkg/machinery/constants"
	"github.com/siderolabs/talos/pkg/machinery/formatters"
	"github.com/siderolabs/talos/pkg/machinery/version"
	"go.yaml.in/yaml/v4"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/emptypb"
)

func dmesg(ctx context.Context, _ Logger, c *client.Client) Collect {
	return func() ([]byte, error) {
		stream, err := c.Dmesg(ctx, false, false)
		if err != nil {
			return nil, err
		}

		data := []byte{}

		for {
			resp, err := stream.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) || client.StatusCode(err) == codes.Canceled {
					break
				}

				return nil, fmt.Errorf("error reading from stream: %w", err)
			}

			data = append(data, resp.GetBytes()...)
		}

		return data, nil
	}
}

func logs(service string, kubernetes bool) TalosCollect {
	return func(ctx context.Context, log Logger, c *client.Client) Collect {
		return func() ([]byte, error) {
			var (
				namespace string
				driver    common.ContainerDriver
				err       error
			)

			if kubernetes {
				namespace = constants.K8sContainerdNamespace
				driver = common.ContainerDriver_CRI
			} else {
				namespace = constants.SystemContainerdNamespace
				driver = common.ContainerDriver_CONTAINERD
			}

			log("getting %s/%s service logs", namespace, service)

			stream, err := c.Logs(ctx, namespace, driver, service, false, -1)
			if err != nil {
				return nil, err
			}

			data := []byte{}

			for {
				resp, err := stream.Recv()
				if err != nil {
					if errors.Is(err, io.EOF) || client.StatusCode(err) == codes.Canceled {
						break
					}

					return nil, fmt.Errorf("error reading from stream: %w", err)
				}

				data = append(data, resp.GetBytes()...)
			}

			return data, nil
		}
	}
}

func dependencies(ctx context.Context, log Logger, c *client.Client) Collect {
	return func() ([]byte, error) {
		log("inspecting controller runtime")

		resp, err := c.Inspect.ControllerRuntimeDependencies(ctx)
		if err != nil {
			if resp == nil {
				return nil, fmt.Errorf("error getting controller runtime dependencies: %w", err)
			}
		}

		var buf bytes.Buffer

		if err = formatters.RenderGraph(ctx, c, resp, &buf, true); err != nil {
			return nil, err
		}

		return buf.Bytes(), nil
	}
}

func mounts(ctx context.Context, log Logger, c *client.Client) Collect {
	return func() ([]byte, error) {
		log("getting mounts")

		resp, err := c.Mounts(ctx)
		if err != nil {
			if resp == nil {
				return nil, fmt.Errorf("error getting mounts: %w", err)
			}
		}

		var buf bytes.Buffer

		w := tabwriter.NewWriter(&buf, 0, 0, 3, ' ', 0)
		parts := []string{"FILESYSTEM", "SIZE(GB)", "USED(GB)", "AVAILABLE(GB)", "PERCENT USED", "MOUNTED ON"}

		fmt.Fprintln(w, strings.Join(parts, "\t")) //nolint:errcheck

		for _, msg := range resp.Messages {
			for _, r := range msg.Stats {
				percentAvailable := 100.0 - 100.0*(float64(r.Available)/float64(r.Size))

				if math.IsNaN(percentAvailable) {
					continue
				}

				fmt.Fprintf( //nolint:errcheck
					w,
					"%s\t%.02f\t%.02f\t%.02f\t%.02f%%\t%s\n",
					r.Filesystem, float64(r.Size)*1e-9, float64(r.Size-r.Available)*1e-9, float64(r.Available)*1e-9, percentAvailable, r.MountedOn,
				)
			}
		}

		if err = w.Flush(); err != nil {
			return nil, err
		}

		return buf.Bytes(), nil
	}
}

func devices(ctx context.Context, log Logger, c *client.Client) Collect {
	return func() ([]byte, error) {
		log("reading devices")

		r, err := c.Read(ctx, "/proc/bus/pci/devices")
		if err != nil {
			return nil, err
		}

		defer r.Close() //nolint:errcheck

		return io.ReadAll(r)
	}
}

func ioPressure(ctx context.Context, log Logger, c *client.Client) Collect {
	return func() ([]byte, error) {
		log("getting disk stats")

		resp, err := c.MachineClient.DiskStats(ctx, &emptypb.Empty{})

		var filtered any

		filtered, err = client.FilterMessages(resp, err)

		resp, _ = filtered.(*machine.DiskStatsResponse) //nolint:errcheck

		if err != nil {
			return nil, err
		}

		var buf bytes.Buffer

		w := tabwriter.NewWriter(&buf, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "NAME\tIO_TIME\tIO_TIME_WEIGHTED\tDISK_WRITE_SECTORS\tDISK_READ_SECTORS") //nolint:errcheck

		for _, msg := range resp.Messages {
			for _, stat := range msg.Devices {
				fmt.Fprintf( //nolint:errcheck
					w, "%s\t%d\t%d\t%d\t%d\n",
					stat.Name,
					stat.IoTimeMs,
					stat.IoTimeWeightedMs,
					stat.WriteSectors,
					stat.ReadSectors,
				)
			}
		}

		if err = w.Flush(); err != nil {
			return nil, err
		}

		return buf.Bytes(), nil
	}
}

func processes(ctx context.Context, log Logger, c *client.Client) Collect {
	return func() ([]byte, error) {
		log("getting processes snapshot")

		resp, err := c.Processes(ctx)
		if err != nil {
			return nil, err
		}

		var buf bytes.Buffer

		w := tabwriter.NewWriter(&buf, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "PID\tSTATE\tTHREADS\tCPU-TIME\tVIRTMEM\tRESMEM\tCOMMAND") //nolint:errcheck

		for _, msg := range resp.Messages {
			procs := msg.Processes

			var args string

			for _, p := range procs {
				switch {
				case p.Executable == "":
					args = p.Command
				case p.Args != "" && strings.Fields(p.Args)[0] == filepath.Base(strings.Fields(p.Executable)[0]):
					args = strings.Replace(p.Args, strings.Fields(p.Args)[0], p.Executable, 1)
				default:
					args = p.Args
				}

				fmt.Fprintf(w, "%6d\t%1s\t%4d\t%8.2f\t%7s\t%7s\t%s\n", //nolint:errcheck
					p.Pid, p.State, p.Threads, p.CpuTime, humanize.Bytes(p.VirtualMemory), humanize.Bytes(p.ResidentMemory), args)
			}
		}

		if err := w.Flush(); err != nil {
			return nil, err
		}

		return buf.Bytes(), nil
	}
}

func summary(ctx context.Context, _ Logger, c *client.Client) Collect {
	return func() ([]byte, error) {
		var buf bytes.Buffer

		fmt.Fprintln(&buf, "Client:")
		version.WriteLongVersionFromExisting(&buf, version.NewVersion())

		resp, err := c.Version(ctx)
		if err != nil {
			return nil, err
		}

		fmt.Fprintln(&buf, "Server:")

		for _, m := range resp.Messages {
			version.WriteLongVersionFromExisting(&buf, m.Version)
		}

		return buf.Bytes(), nil
	}
}

func talosResource(rd *meta.ResourceDefinition) TalosCollect {
	return func(ctx context.Context, log Logger, c *client.Client) Collect {
		return func() ([]byte, error) {
			log("getting talos resource %s/%s", rd.TypedSpec().DefaultNamespace, rd.TypedSpec().Type)

			resources, err := c.COSI.List(
				ctx, resource.NewMetadata(rd.TypedSpec().DefaultNamespace, rd.TypedSpec().Type, "", resource.VersionUndefined),
				state.WithListUnmarshalOptions(state.WithSkipProtobufUnmarshal()),
			)
			if err != nil {
				return nil, err
			}

			var (
				buf      bytes.Buffer
				hasItems bool
			)

			encoder := yaml.NewEncoder(&buf)

			for _, r := range resources.Items {
				data := struct {
					Metadata *resource.Metadata `yaml:"metadata"`
					Spec     any                `yaml:"spec"`
				}{
					Metadata: r.Metadata(),
					Spec:     "<REDACTED>",
				}

				if rd.TypedSpec().Sensitivity != meta.Sensitive {
					data.Spec = r.Spec()
				}

				if err = encoder.Encode(&data); err != nil {
					return nil, err
				}

				hasItems = true
			}

			if !hasItems {
				return nil, nil
			}

			return buf.Bytes(), encoder.Close()
		}
	}
}

func serviceInfo(id string) TalosCollect {
	return func(ctx context.Context, log Logger, c *client.Client) Collect {
		return func() ([]byte, error) {
			services, err := c.ServiceInfo(ctx, id)
			if err != nil {
				if services == nil {
					return nil, fmt.Errorf("error listing services: %w", err)
				}
			}

			var buf bytes.Buffer

			w := tabwriter.NewWriter(&buf, 0, 0, 3, ' ', 0)

			for _, s := range services {
				svc := serviceInfoWrapper{s.Service}
				fmt.Fprintf(w, "ID\t%s\n", svc.Id)                 //nolint:errcheck
				fmt.Fprintf(w, "STATE\t%s\n", svc.State)           //nolint:errcheck
				fmt.Fprintf(w, "HEALTH\t%s\n", svc.HealthStatus()) //nolint:errcheck

				if svc.Health.LastMessage != "" {
					fmt.Fprintf(w, "LAST HEALTH MESSAGE\t%s\n", svc.Health.LastMessage) //nolint:errcheck
				}

				label := "EVENTS"

				for i := range svc.Events.Events {
					event := svc.Events.Events[len(svc.Events.Events)-1-i]

					ts := event.Ts.AsTime()
					fmt.Fprintf(w, "%s\t[%s]: %s (%s ago)\n", label, event.State, event.Msg, time.Since(ts).Round(time.Second)) //nolint:errcheck
					label = ""
				}
			}

			if err := w.Flush(); err != nil {
				return nil, err
			}

			return buf.Bytes(), nil
		}
	}
}

// serviceInfoWrapper helper that allows generating rich service information.
//
// Deprecated: new code should format service information from the multiplexed
// response with the node attached explicitly (see the `service` command).
type serviceInfoWrapper struct {
	*machine.ServiceInfo
}

// LastUpdated derive last updated time from events stream.
func (svc serviceInfoWrapper) LastUpdated() string {
	if len(svc.Events.Events) == 0 {
		return ""
	}

	ts := svc.Events.Events[len(svc.Events.Events)-1].Ts.AsTime()

	return time.Since(ts).Round(time.Second).String()
}

// LastEvent return last service event.
func (svc serviceInfoWrapper) LastEvent() string {
	if len(svc.Events.Events) == 0 {
		return "<none>"
	}

	return svc.Events.Events[len(svc.Events.Events)-1].Msg
}

// HealthStatus service health status.
func (svc serviceInfoWrapper) HealthStatus() string {
	if svc.Health.Unknown {
		return "?"
	}

	if svc.Health.Healthy {
		return "OK"
	}

	return "Fail"
}
