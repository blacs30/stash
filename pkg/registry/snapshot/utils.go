/*
Copyright AppsCode Inc. and Contributors

Licensed under the AppsCode Community License 1.0.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://github.com/appscode/licenses/raw/1.0.0/AppsCode-Community-1.0.0.md

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package snapshot

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"stash.appscode.dev/apimachinery/apis"
	"stash.appscode.dev/apimachinery/apis/repositories"
	stash "stash.appscode.dev/apimachinery/apis/stash/v1alpha1"
	"stash.appscode.dev/apimachinery/pkg/restic"
	"stash.appscode.dev/stash/pkg/util"

	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/klog/v2"
	meta_util "kmodules.xyz/client-go/meta"
)

const ExecStash = "/stash-enterprise"

func (r *REST) GetSnapshotsFromBackned(repository *stash.Repository, snapshotIDs []string, inCluster bool) ([]repositories.Snapshot, error) {
	if repository.Spec.Backend.Local != nil && !inCluster {
		return r.getSnapshotsFromLocalBackend(repository, snapshotIDs)
	}
	return r.getSnapshotsFromBackend(repository, snapshotIDs)
}

func (r *REST) getSnapshotsFromBackend(repository *stash.Repository, snapshotIDs []string) ([]repositories.Snapshot, error) {
	tempDir, err := ioutil.TempDir("", "stash")
	if err != nil {
		return nil, err
	}
	// cleanup whole tempDir dir at the end
	defer os.RemoveAll(tempDir)

	// get source repository secret
	secret, err := r.kubeClient.CoreV1().Secrets(repository.Namespace).Get(context.TODO(), repository.Spec.Backend.StorageSecretName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	// configure restic wrapper
	extraOpt := &util.ExtraOptions{
		StorageSecret: secret,
		EnableCache:   false,
		ScratchDir:    tempDir,
	}
	// configure setupOption
	setupOpt, err := util.SetupOptionsForRepository(*repository, *extraOpt)
	if err != nil {
		return nil, fmt.Errorf("setup option for repository failed, reason: %s", err)
	}
	if repository.LocalNetworkVolume() {
		setupOpt.Bucket = filepath.Join(setupOpt.Bucket, repository.LocalNetworkVolumePath())
	}
	// init restic wrapper
	resticWrapper, err := restic.NewResticWrapper(setupOpt)
	if err != nil {
		return nil, err
	}
	// if repository does not exist in the backend, then nothing to list. Just return.
	if !resticWrapper.RepositoryAlreadyExist() {
		klog.Infof("unable to verify whether repository exist or not in the backend for Repository: %s/%s", repository.Namespace, repository.Name)
		return nil, nil
	}
	// list snapshots, returns all snapshots for empty snapshotIDs
	// if there is no restic repository in the backend, this will return error.
	// in this case, we have to return empty snapshot list.
	results, err := resticWrapper.ListSnapshots(snapshotIDs)
	if err != nil {
		// check if the error is happening because of not having restic repository in the backend.
		if repoNotFound(resticWrapper.GetRepo(), err) {
			return nil, nil
		}
		return nil, err
	}

	snapshots := make([]repositories.Snapshot, 0)
	snapshot := &repositories.Snapshot{}
	for _, result := range results {
		snapshot.Namespace = repository.Namespace
		snapshot.Name = meta_util.NameWithSuffix(repository.Name, result.ID[0:apis.SnapshotIDLength]) // snapshotName = repositoryName-first8CharacterOfSnapshotId
		snapshot.UID = types.UID(result.ID)

		snapshot.Labels = map[string]string{
			"repository": repository.Name,
			"hostname":   result.Hostname,
		}
		if repository.Labels != nil {
			snapshot.Labels = meta_util.OverwriteKeys(snapshot.Labels, repository.Labels)
		}

		snapshot.CreationTimestamp.Time = result.Time
		snapshot.Status.UID = result.UID
		snapshot.Status.Gid = result.Gid
		snapshot.Status.Hostname = result.Hostname
		snapshot.Status.Paths = result.Paths
		snapshot.Status.Tree = result.Tree
		snapshot.Status.Username = result.Username
		snapshot.Status.Tags = result.Tags
		snapshot.Status.Repository = repository.Name

		snapshots = append(snapshots, *snapshot)
	}
	return snapshots, nil
}

func (r *REST) getSnapshotsFromLocalBackend(repository *stash.Repository, snapshotIDs []string) ([]repositories.Snapshot, error) {
	response, err := r.execOnBackendMountingPod(repository, "snapshots", snapshotIDs)
	if err != nil {
		return nil, err
	}

	snapshots := make([]repositories.Snapshot, 0)
	err = json.Unmarshal(response, &snapshots)
	if err != nil {
		return nil, err
	}

	return snapshots, nil
}

func (r *REST) ForgetSnapshotsFromBackend(repository *stash.Repository, snapshotIDs []string, inCluster bool) error {
	if repository.Spec.Backend.Local != nil && !inCluster {
		return r.forgetSnapshotsFromLocalBackend(repository, snapshotIDs)
	}
	return r.forgetSnapshotsFromBackend(repository, snapshotIDs)
}

func (r *REST) forgetSnapshotsFromLocalBackend(repository *stash.Repository, snapshotIDs []string) error {
	_, err := r.execOnBackendMountingPod(repository, "forget", snapshotIDs)
	return err
}

func (r *REST) forgetSnapshotsFromBackend(repository *stash.Repository, snapshotIDs []string) error {
	tempDir, err := ioutil.TempDir("", "stash")
	if err != nil {
		return err
	}
	// cleanup whole tempDir dir at the end
	defer os.RemoveAll(tempDir)

	// get source repository secret
	secret, err := r.kubeClient.CoreV1().Secrets(repository.Namespace).Get(context.TODO(), repository.Spec.Backend.StorageSecretName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	// configure restic wrapper
	extraOpt := util.ExtraOptions{
		StorageSecret: secret,
		EnableCache:   false,
		ScratchDir:    tempDir,
	}
	// configure setupOption
	setupOpt, err := util.SetupOptionsForRepository(*repository, extraOpt)
	if err != nil {
		return fmt.Errorf("setup option for repository failed, reason: %s", err)
	}
	if repository.LocalNetworkVolume() {
		setupOpt.Bucket = filepath.Join(setupOpt.Bucket, repository.LocalNetworkVolumePath())
	}
	// init restic wrapper
	resticWrapper, err := restic.NewResticWrapper(setupOpt)
	if err != nil {
		return err
	}
	// delete snapshots
	_, err = resticWrapper.DeleteSnapshots(snapshotIDs)
	return err
}

func (r *REST) execOnBackendMountingPod(repository *stash.Repository, cmd string, snapshotIDs []string) ([]byte, error) {
	// get the pod that mount this repository as volume
	pod, err := r.getBackendMountingPod(repository)
	if err != nil {
		return nil, err
	}

	command := []string{ExecStash, cmd}
	command = append(command, "--repo-name="+repository.Name)
	command = append(command, "--repo-namespace="+repository.Namespace)

	if snapshotIDs != nil {
		command = append(command, snapshotIDs...)
	}

	response, err := r.execCommandOnPod(pod, command)
	if err != nil {
		return nil, err
	}

	return response, nil
}

func (r *REST) getBackendMountingPod(repo *stash.Repository) (*core.Pod, error) {
	vol, mnt := repo.Spec.Backend.Local.ToVolumeAndMount(repo.Name)
	if repo.LocalNetworkVolume() {
		mnt.MountPath = filepath.Join(mnt.MountPath, repo.LocalNetworkVolumePath())
	}
	// list all the pods
	podList, err := r.kubeClient.CoreV1().Pods(repo.Namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	// return the pod that has the vol and mnt
	for i := range podList.Items {
		if hasVolume(podList.Items[i].Spec.Volumes, vol) {
			for _, c := range podList.Items[i].Spec.Containers {
				if hasVolumeMount(c.VolumeMounts, mnt) {
					return &podList.Items[i], nil
				}
			}
		}
	}

	return nil, fmt.Errorf("no backend mounting pod found for Repository %v", repo.Name)
}

func (r *REST) execCommandOnPod(pod *core.Pod, command []string) ([]byte, error) {
	var (
		execOut bytes.Buffer
		execErr bytes.Buffer
	)

	klog.Infof("Executing command %v on pod %v", command, pod.Name)

	req := r.kubeClient.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod.Name).
		Namespace(pod.Namespace).
		SubResource("exec").
		Timeout(5 * time.Minute)
	req.VersionedParams(&core.PodExecOptions{
		Container: apis.StashContainer,
		Command:   command,
		Stdout:    true,
		Stderr:    true,
	}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(r.config, "POST", req.URL())
	if err != nil {
		return nil, fmt.Errorf("failed to init executor: %v", err)
	}

	err = exec.Stream(remotecommand.StreamOptions{
		Stdout: &execOut,
		Stderr: &execErr,
		Tty:    true,
	})

	if err != nil {
		return nil, fmt.Errorf("could not execute: %v, reason: %s", err, execErr.String())
	}

	return execOut.Bytes(), nil
}

func repoNotFound(repo string, err error) bool {
	repoNotFoundMessage := fmt.Sprintf("exit status 1, reason: %s", repo)

	scanner := bufio.NewScanner(strings.NewReader(err.Error()))
	var line string
	for scanner.Scan() {
		line = scanner.Text()
		if strings.TrimSpace(line) == repoNotFoundMessage {
			return true
		}
	}
	return false
}

func hasVolume(volumes []core.Volume, vol core.Volume) bool {
	for i := range volumes {
		if volumes[i].Name == vol.Name {
			return true
		}
	}
	return false
}

func hasVolumeMount(mounts []core.VolumeMount, mnt core.VolumeMount) bool {
	for i := range mounts {
		if mounts[i].Name == mnt.Name && mounts[i].MountPath == mnt.MountPath {
			return true
		}
	}
	return false
}
