// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package support_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/siderolabs/go-talos-support/support"
	"github.com/siderolabs/go-talos-support/support/bundle"
	"github.com/siderolabs/go-talos-support/support/collectors"
)

type testArchive struct {
	files   map[string][]byte
	filesMu sync.Mutex
}

func (a *testArchive) Write(path string, data []byte) error {
	a.filesMu.Lock()
	defer a.filesMu.Unlock()

	if a.files == nil {
		a.files = map[string][]byte{}
	}

	a.files[path] = data

	return nil
}

func (a *testArchive) Close() error {
	return nil
}

func TestCollect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	archive := &testArchive{}

	require := require.New(t)

	cols := []*collectors.Collector{
		collectors.NewCollector("1", func(context.Context, *bundle.Options) ([]byte, error) {
			return []byte("something"), nil
		}),
		collectors.NewCollector("1", func(context.Context, *bundle.Options) ([]byte, error) {
			return []byte("something"), nil
		}),
	}

	cols = append(cols,
		collectors.WithNode(
			[]*collectors.Collector{
				collectors.NewCollector("1", func(context.Context, *bundle.Options) ([]byte, error) {
					return []byte("another"), nil
				}),
			}, "n1",
		)...)

	options := bundle.NewOptions(
		bundle.WithArchive(archive),
		bundle.WithNumWorkers(5),
	)

	require.NoError(support.CreateSupportBundle(ctx, options, cols...))

	require.EqualValues("something", archive.files["1"])
	require.EqualValues("another", archive.files["n1/1"])
}

func TestCollectWithProgress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	archive := &testArchive{}

	require := require.New(t)

	cols := make([]*collectors.Collector, 0, 1000)

	group := 0

	for i := 0; i < cap(cols); i++ {
		if i%10 == 0 {
			group++
		}

		col := []*collectors.Collector{
			collectors.NewCollector(fmt.Sprintf("%d", i), func(context.Context, *bundle.Options) ([]byte, error) {
				return []byte("something"), nil
			}),
		}

		cols = append(cols, collectors.WithSource(col, fmt.Sprintf("%d", group))...)
	}

	progress := make(chan bundle.Progress, 1000)

	options := bundle.NewOptions(
		bundle.WithArchive(archive),
		bundle.WithNumWorkers(5),
		bundle.WithProgressChan(progress),
	)

	require.NoError(support.CreateSupportBundle(ctx, options, cols...))

	require.Len(archive.files, len(cols))

	for i := 0; i < len(cols); i++ {
		assert.Contains(t, archive.files, fmt.Sprintf("%d", i))
	}

	count := 0

	finalValues := map[string]int{}

outer:
	for {
		select {
		case p := <-progress:
			assert.Equal(t, p.Total, 10)
			count++

			finalValues[p.Source]++

			if count == 1000 {
				break outer
			}
		case <-ctx.Done():
			require.NoError(ctx.Err())
		}
	}

	for s, fv := range finalValues {
		assert.Equal(t, 10, fv, "failed for source %s", s)
	}
}
