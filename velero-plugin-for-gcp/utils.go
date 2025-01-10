package main

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"google.golang.org/api/compute/v1"

	corev1api "k8s.io/api/core/v1"
	kerror "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type PvcInfo struct {
	Namespace              string `json:"namespace,omitempty"`
	DataTransferSnapshotId string `json:"data_transfer_snapshot_id,omitempty"`
	SnapshotId             string `json:"snapshot_id,omitempty"`
	VolumeId               string `json:"volume_id,omitempty"`
	Name                   string `json:"name,omitempty"`
	PvName                 string `json:"pv_name,omitempty"`
	SnapshotType           string `json:"snapshot_type,omitempty"`
}

type PvcSnapshotProgressData struct {
	JobId            string   `json:"job_id,omitempty"`
	Message          string   `json:"message,omitempty"`
	State            string   `json:"state,omitempty"`
	SnapshotProgress int32    `json:"snapshot_progress,omitempty"`
	Pvc              *PvcInfo `json:"pvc,omitempty"`
}

type VolumeDescription struct {
	PvName                string `json:"kubernetes.io/created-for/pv/name"`
	PvcName               string `json:"kubernetes.io/created-for/pvc/name"`
	PvcNamespace          string `json:"kubernetes.io/created-for/pvc/namespace"`
	VeleroBackup          string `json:"velero.io/backup"`
	VeleroPv              string `json:"velero.io/pv"`
	VeleroStorageLocation string `json:"velero.io/storage-location"`
}

const (
	TimeFormat         = "2006-01-06 15:04:05 UTC: "
	CloudCasaNamespace = "cloudcasa-io"
	// Name of ConfigMap used to to report progress of snapshot
	snapshotProgressUpdateConfigMapNamePrefix = "cloudcasa-io-snapshot-updater-gcp-"
)

// UpdateSnapshotProgress updates the ConfigMap in order to relay the snapshot
// progrtess to the agent.
func (vs *VolumeSnapshotter) UpdateSnapshotProgress(diskInfo *compute.Disk, tags map[string]string, snapshot *compute.Snapshot, snapshotStateMessage string) error {
	vs.log.Info("Update Snapshot Progress - Starting to relay Google Persistent Disk Volume snapshot progress to KubeAgent")

	// Read PVC information from the Google Persistent Disk Volume description
	var pvc = PvcInfo{}
	pvc.PvName = tags["velero.io/pv"]
	vs.log.Infof("Update Snapshot Progress - PV Name: %s", pvc.PvName)

	volumeDescription := VolumeDescription{}
	if err := json.Unmarshal([]byte(diskInfo.Description), &volumeDescription); err != nil {
		vs.log.Errorf("Failed to decode Google Persistent Disk description into a struct: %w. Description: %s", err, diskInfo.Description)
		return err
	}

	pvc.Name = volumeDescription.PvcName
	pvc.Namespace = volumeDescription.PvcNamespace
	pvc.SnapshotType = "NATIVE"
	pvc.SnapshotId = strconv.FormatUint(snapshot.Id, 10)
	pvc.VolumeId = strconv.FormatUint(diskInfo.Id, 10)

	// Gather information to report progress to the agent
	var progress = PvcSnapshotProgressData{}
	progress.JobId = tags["velero.io/backup"]
	vs.log.Infof("Update Snapshot Progress - JobID: %s", progress.JobId)
	currentTimeString := time.Now().UTC().Format(TimeFormat)
	progress.State = snapshot.Status
	progress.Message = currentTimeString + " " + snapshotStateMessage
	if snapshot.DiskSizeGb > 0 {
		storageBytesInGb := int64(float64(snapshot.StorageBytes) / (1 << 30))
		progress.SnapshotProgress = int32(storageBytesInGb / snapshot.DiskSizeGb)
	}
	if snapshot.Status == "READY" {
		progress.SnapshotProgress = 100
	}
	progress.Pvc = &pvc
	vs.log.Infof("Update Snapshot Progress - Progress Payload: %s", progress)

	// Prepare the paylod to be embedded into the ConfigMap
	requestData := make(map[string][]byte)
	marshalledProgressData, err := json.Marshal(progress)
	if err != nil {
		vs.log.Errorf("Failed to marshal progress while creating the Google Persistent Disk Volume snapshot progress ConfigMap: %s", err.Error())
		return err
	}
	requestData["snapshot_progress_payload"] = marshalledProgressData
	vs.log.Info("Update Snapshot Progress - Marsahlled the JSON payload")

	progressConfigMapName := snapshotProgressUpdateConfigMapNamePrefix + progress.JobId
	progressConfigMap := corev1api.ConfigMap{
		TypeMeta: v1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: v1.ObjectMeta{
			Name:      progressConfigMapName,
			Namespace: CloudCasaNamespace,
		},
		BinaryData: requestData,
	}
	vs.log.Info("Update Snapshot Progress - Created the ConfigMap object")

	// Preapare cluster credentials in order to create/update snapshot progress ConfigMap
	config, err := rest.InClusterConfig()
	if err != nil {
		vs.log.Errorf("Failed to create in-cluster config: %s", err.Error())
		return err

	}
	vs.log.Info("Update Snapshot Progress - Created in-cluster config")

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		vs.log.Errorf("Failed to create clientset: %s", err.Error())
		return err
	}
	vs.log.Info("Update Snapshot Progress - Created clientset")

	// Create or update the snapshot progress ConfigMap
	var mcm *corev1api.ConfigMap
	_, err = clientset.CoreV1().ConfigMaps(CloudCasaNamespace).Get(context.TODO(), progressConfigMapName, v1.GetOptions{})
	if kerror.IsNotFound(err) {
		mcm, err = clientset.CoreV1().ConfigMaps(CloudCasaNamespace).Create(context.TODO(), &progressConfigMap, v1.CreateOptions{})
		if err != nil {
			vs.log.Errorf("Failed to create ConfigMap to report Google Persistent Disk Volume snapshot progress: %s", err.Error())
			return err

		}
		vs.log.Infof("Created ConfigMap to report Google Persistent Disk Volume snapshot progress - ConfigMap Name: %s", mcm.GetName())
	} else {
		mcm, err = clientset.CoreV1().ConfigMaps(CloudCasaNamespace).Update(context.TODO(), &progressConfigMap, v1.UpdateOptions{})
		if err != nil {
			vs.log.Errorf("Failed to update ConfigMap to report Google Persistent Disk Volume snapshot progress: %s", err.Error())
			return err
		}
		vs.log.Infof("Updated ConfigMap to report Google Persistent Disk Volume snapshot progress - ConfigMap Name: %s", mcm.GetName())
	}
	vs.log.Info("Finished relaying Google Persistent Disk Volume snapshot progress to KubeAgent")
	return nil
}

// DeleteSnapshotProgressConfigMap deletes the ConfigMap used to report snapshot progress
func (vs *VolumeSnapshotter) DeleteSnapshotProgressConfigMap(tags map[string]string) {
	jobId := tags["velero.io/backup"]
	progressConfigMapName := snapshotProgressUpdateConfigMapNamePrefix + jobId
	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		vs.log.Error(errors.Wrap(err, "Failed to create in-cluster config"))
	}
	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		vs.log.Error(errors.Wrap(err, "Failed to create in-cluster clientset"))
	}
	err = clientset.CoreV1().ConfigMaps(CloudCasaNamespace).Delete(context.TODO(), progressConfigMapName, v1.DeleteOptions{})
	if err != nil {
		vs.log.Error(errors.Wrap(err, "Failed to delete ConfigMap used to report snapshot progress"))
	} else {
		vs.log.Infof("Deleted ConfigMap used to report snapshot progress - ConfigMap Name: %s", progressConfigMapName)
	}
}
