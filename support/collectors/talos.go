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
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/resource/meta"
	"github.com/dustin/go-humanize"
	"github.com/siderolabs/talos/pkg/machinery/api/common"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/client"
	"github.com/siderolabs/talos/pkg/machinery/constants"
	"github.com/siderolabs/talos/pkg/machinery/formatters"
	"github.com/siderolabs/talos/pkg/machinery/version"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/emptypb"
	"gopkg.in/yaml.v3"

	"github.com/siderolabs/go-talos-support/support/bundle"
)

func dmesg(ctx context.Context, options *bundle.Options) ([]byte, error) {
	stream, err := options.TalosClient.Dmesg(ctx, false, false)
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

		if resp.Metadata != nil {
			if resp.Metadata.Error != "" {
				fmt.Fprintf(os.Stderr, "%s\n", resp.Metadata.Error)
			}
		}

		data = append(data, resp.GetBytes()...)
	}

	return data, nil
}

func logs(service string, kubernetes bool) Collect {
	return func(ctx context.Context, options *bundle.Options) ([]byte, error) {
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

		options.Log("getting %s/%s service logs", namespace, service)

		stream, err := options.TalosClient.Logs(ctx, namespace, driver, service, false, -1)
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

			if resp.Metadata != nil {
				if resp.Metadata.Error != "" {
					fmt.Fprintf(os.Stderr, "%s\n", resp.Metadata.Error)
				}
			}

			data = append(data, resp.GetBytes()...)
		}

		return data, nil
	}
}

func dependencies(ctx context.Context, options *bundle.Options) ([]byte, error) {
	options.Log("inspecting controller runtime")

	resp, err := options.TalosClient.Inspect.ControllerRuntimeDependencies(ctx)
	if err != nil {
		if resp == nil {
			return nil, fmt.Errorf("error getting controller runtime dependencies: %w", err)
		}
	}

	var buf bytes.Buffer

	if err = formatters.RenderGraph(ctx, options.TalosClient, resp, &buf, true); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func mounts(ctx context.Context, options *bundle.Options) ([]byte, error) {
	options.Log("getting mounts")

	resp, err := options.TalosClient.Mounts(ctx)
	if err != nil {
		if resp == nil {
			return nil, fmt.Errorf("error getting interfaces: %w", err)
		}
	}

	var buf bytes.Buffer

	if err = formatters.RenderMounts(resp, &buf, nil); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func devices(ctx context.Context, options *bundle.Options) ([]byte, error) {
	options.Log("reading devices")

	r, err := options.TalosClient.Read(ctx, "/proc/bus/pci/devices")
	if err != nil {
		return nil, err
	}

	defer r.Close() //nolint:errcheck

	return io.ReadAll(r)
}

func ioPressure(ctx context.Context, options *bundle.Options) ([]byte, error) {
	options.Log("getting disk stats")

	resp, err := options.TalosClient.MachineClient.DiskStats(ctx, &emptypb.Empty{})

	var filtered interface{}
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
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\n", //nolint:errcheck
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

func processes(ctx context.Context, options *bundle.Options) ([]byte, error) {
	options.Log("getting processes snapshot")

	resp, err := options.TalosClient.Processes(ctx)
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

func summary(ctx context.Context, options *bundle.Options) ([]byte, error) {
	var buf bytes.Buffer

	fmt.Fprintln(&buf, "Client:")
	version.WriteLongVersionFromExisting(&buf, version.NewVersion())

	resp, err := options.TalosClient.Version(ctx)
	if err != nil {
		return nil, err
	}

	fmt.Fprintln(&buf, "Server:")

	for _, m := range resp.Messages {
		version.WriteLongVersionFromExisting(&buf, m.Version)
	}

	return buf.Bytes(), nil
}

func talosResource(rd *meta.ResourceDefinition) Collect {
	return func(ctx context.Context, options *bundle.Options) ([]byte, error) {
		options.Log("getting talos resource %s/%s", rd.TypedSpec().DefaultNamespace, rd.TypedSpec().Type)

		resources, err := options.TalosClient.COSI.List(ctx, resource.NewMetadata(rd.TypedSpec().DefaultNamespace, rd.TypedSpec().Type, "", resource.VersionUndefined))
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
				Spec     interface{}        `yaml:"spec"`
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

func serviceInfo(id string) Collect {
	return func(ctx context.Context, options *bundle.Options) ([]byte, error) {
		services, err := options.TalosClient.ServiceInfo(ctx, id)
		if err != nil {
			if services == nil {
				return nil, fmt.Errorf("error listing services: %w", err)
			}
		}

		var buf bytes.Buffer

		if err := formatters.RenderServicesInfo(services, &buf, "", false); err != nil {
			return nil, err
		}

		return buf.Bytes(), nil
	}
}
