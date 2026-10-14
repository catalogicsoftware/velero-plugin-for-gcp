package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// kubeagent gives the snapshot location an OAuth2 token file and only the
// "project" key; the volumes and the snapshots are both in that project.
func TestInitWithOAuthTokenUsesTheProjectForVolumes(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile,
		[]byte(`{"access_token":"ya29.token","token_type":"Bearer","expiry":"2099-01-01T00:00:00Z"}`), 0600))

	volumeSnapshotter := newVolumeSnapshotter(logrus.StandardLogger())
	require.NoError(t, volumeSnapshotter.Init(map[string]string{
		"project":         "project-a",
		"credentialsFile": tokenFile,
	}))
	require.Equal(t, "project-a", volumeSnapshotter.volumeProject)
	require.Equal(t, "project-a", volumeSnapshotter.snapshotProject)
	require.NotNil(t, volumeSnapshotter.gce)
}
