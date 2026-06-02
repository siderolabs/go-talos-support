// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package encryption_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/siderolabs/go-talos-support/support/bundle/encryption"
)

func TestEncryptDefaultRecipients(t *testing.T) {
	t.Parallel()

	recipients, err := encryption.DefaultRecipients()
	require.NoError(t, err)
	require.NotEmpty(t, recipients, "expected at least one default recipient")

	var buf bytes.Buffer

	w, err := encryption.Encrypt(&buf)
	require.NoError(t, err)

	_, err = w.Write([]byte("hello world"))
	require.NoError(t, err)

	require.NoError(t, w.Close())

	require.NotEmpty(t, buf.Bytes(), "expected encrypted output")
}
