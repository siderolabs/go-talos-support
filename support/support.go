// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package support provides helpers for Talos cluster support bundle collection.
package support

import (
	"context"

	"github.com/siderolabs/gen/channel"
	"golang.org/x/sync/errgroup"

	"github.com/siderolabs/go-talos-support/support/bundle"
	"github.com/siderolabs/go-talos-support/support/collectors"
)

// CreateSupportBundle generates support bundle using provided collectors.
func CreateSupportBundle(ctx context.Context, options *bundle.Options, cols ...*collectors.Collector) error {
	tasks := make(chan *collectors.Collector)

	totals := calculateTotals(cols...)

	eg, ctx := errgroup.WithContext(ctx)

	collectProgress := options.Progress != nil

	if options.NumWorkers == 0 {
		options.NumWorkers = 1
	}

	for i := 0; i < options.NumWorkers; i++ {
		eg.Go(func() error {
			for {
				select {
				case collector := <-tasks:
					if collector == nil {
						return nil
					}

					err := collector.Run(ctx, options)

					if !collectProgress {
						continue
					}

					progress := bundle.Progress{
						Error:  err,
						Total:  totals[collector.Source()],
						Source: collector.Source(),
						State:  collector.String(),
					}

					if !channel.SendWithContext(ctx, options.Progress, progress) {
						return nil
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		})
	}

	for _, col := range cols {
		tasks <- col
	}

	close(tasks)

	if err := eg.Wait(); err != nil {
		return err
	}

	return options.Archive.Close()
}

func calculateTotals(cols ...*collectors.Collector) map[string]int {
	res := map[string]int{}

	for _, col := range cols {
		res[col.Source()]++
	}

	return res
}
