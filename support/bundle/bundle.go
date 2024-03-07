// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package bundle describes support cmd input options.
package bundle

import (
	"archive/zip"
	"fmt"
	"io"
	"sync"

	"github.com/siderolabs/talos/pkg/machinery/client"
	"k8s.io/client-go/kubernetes"
)

// Options defines GetSupportBundle options.
type Options struct {
	TalosClient      *client.Client
	KubernetesClient *kubernetes.Clientset
	Archive          Archive
	LogOutput        io.Writer
	Progress         chan Progress
	Nodes            []string

	NumWorkers int
}

// NewOptions creates new Options.
func NewOptions(opts ...Option) *Options {
	var options Options

	for _, o := range opts {
		o(&options)
	}

	return &options
}

// Progress reports current bundle collection progress.
type Progress struct {
	Error  error
	Source string
	State  string
	Total  int
}

// Archive defines archive writer interface.
type Archive interface {
	Write(path string, contents []byte) error
	Close() error
}

// archive wraps archive writer in a thread safe implementation.
type archive struct {
	Archive   *zip.Writer
	archiveMu sync.Mutex
}

// Write creates a file in the archive.
func (a *archive) Write(path string, contents []byte) error {
	a.archiveMu.Lock()
	defer a.archiveMu.Unlock()

	file, err := a.Archive.Create(path)
	if err != nil {
		return err
	}

	_, err = file.Write(contents)
	if err != nil {
		return err
	}

	return nil
}

func (a *archive) Close() error {
	return a.Archive.Close()
}

// Log writes the line to logger or to stdout if no logger was provided.
func (options *Options) Log(line string, args ...interface{}) {
	if options.LogOutput != nil {
		options.LogOutput.Write([]byte(fmt.Sprintf(line, args...))) //nolint:errcheck

		return
	}

	fmt.Printf(line+"\n", args...)
}
