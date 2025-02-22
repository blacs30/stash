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

package hooks

import (
	"context"
	"fmt"
	"path/filepath"

	"stash.appscode.dev/apimachinery/apis"
	"stash.appscode.dev/apimachinery/apis/stash/v1beta1"
	"stash.appscode.dev/stash/test/e2e/framework"
	. "stash.appscode.dev/stash/test/e2e/matcher"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	app_util "kmodules.xyz/client-go/apps/v1"
	probev1 "kmodules.xyz/prober/api/v1"
)

var _ = Describe("PostRestore Hook", func() {
	var f *framework.Invocation

	BeforeEach(func() {
		f = framework.NewInvocation()
	})

	JustAfterEach(func() {
		f.PrintDebugInfoOnFailure()
	})

	AfterEach(func() {
		err := f.CleanupTestResources()
		Expect(err).NotTo(HaveOccurred())
	})

	Context("ExecAction", func() {
		Context("Sidecar Model", func() {
			Context("Success Test", func() {
				It("should execute the postRestore hook successfully", func() {
					// Deploy a StatefulSet with prober client. Here, we are using a StatefulSet because we need a stable address
					// for pod where http request will be sent.
					statefulset, err := f.DeployStatefulSetWithProbeClient(framework.ProberDemoPodPrefix)
					Expect(err).NotTo(HaveOccurred())

					// Read data at empty state
					emptyData, err := f.ReadSampleDataFromFromWorkload(statefulset.ObjectMeta, apis.KindStatefulSet)
					Expect(err).NotTo(HaveOccurred())

					// Generate Sample Data
					sampleData, err := f.GenerateSampleData(statefulset.ObjectMeta, apis.KindStatefulSet)
					Expect(err).NotTo(HaveOccurred())
					Expect(sampleData).ShouldNot(BeSameAs(emptyData))

					// Setup a Minio Repository
					repo, err := f.SetupMinioRepository()
					Expect(err).NotTo(HaveOccurred())
					f.AppendToCleanupList(repo)

					// Setup workload Backup
					backupConfig, err := f.SetupWorkloadBackup(statefulset.ObjectMeta, repo, apis.KindStatefulSet)
					Expect(err).NotTo(HaveOccurred())

					// Take an Instant Backup of the Sample Data
					backupSession, err := f.TakeInstantBackup(backupConfig.ObjectMeta, v1beta1.BackupInvokerRef{
						Name: backupConfig.Name,
						Kind: v1beta1.ResourceKindBackupConfiguration,
					})
					Expect(err).NotTo(HaveOccurred())

					By("Verifying that BackupSession has succeeded")
					completedBS, err := f.StashClient.StashV1beta1().BackupSessions(backupSession.Namespace).Get(context.TODO(), backupSession.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					Expect(completedBS.Status.Phase).Should(Equal(v1beta1.BackupSessionSucceeded))

					// Simulate disaster scenario. Remove the old data. Then add a demo corrupted file.
					// This corrupted file will be deleted in postRestore hook.
					By("Modifying source data")
					pod, err := f.GetPod(statefulset.ObjectMeta)
					Expect(err).NotTo(HaveOccurred())
					_, err = f.ExecOnPod(pod, "/bin/sh", "-c", fmt.Sprintf("rm -rf %s/*", framework.TestSourceDataMountPath))
					Expect(err).NotTo(HaveOccurred())
					_, err = f.ExecOnPod(pod, "touch", filepath.Join(framework.TestSourceDataMountPath, "corrupted-data.txt"))
					Expect(err).NotTo(HaveOccurred())

					// Restore the backed up data
					// Remove the corrupted data in postRestore hook.
					By("Restoring the backed up data in the original StatefulSet")
					restoreSession, err := f.SetupRestoreProcess(statefulset.ObjectMeta, repo, apis.KindStatefulSet, framework.SourceVolume, func(restore *v1beta1.RestoreSession) {
						restore.Spec.Hooks = &v1beta1.RestoreHooks{
							PostRestore: &probev1.Handler{
								Exec: &core.ExecAction{
									Command: []string{"/bin/sh", "-c", fmt.Sprintf("rm %s/corrupted-data.txt", framework.TestSourceDataMountPath)},
								},
								ContainerName: apis.StashInitContainer,
							},
						}
					})
					Expect(err).NotTo(HaveOccurred())

					By("Verifying that RestoreSession succeeded")
					completedRS, err := f.StashClient.StashV1beta1().RestoreSessions(restoreSession.Namespace).Get(context.TODO(), restoreSession.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					Expect(completedRS.Status.Phase).Should(Equal(v1beta1.RestoreSucceeded))

					restoredData := f.RestoredData(statefulset.ObjectMeta, apis.KindStatefulSet)
					By("Verifying that the original data has been restored and corrupted file has been removed")
					Expect(restoredData).Should(BeSameAs(sampleData))
				})

				It("should execute the postRestore hook even when the restore process failed", func() {
					// Deploy a StatefulSet with prober client. Here, we are using a StatefulSet because we need a stable address
					// for pod where http request will be sent.
					statefulset, err := f.DeployStatefulSetWithProbeClient(framework.ProberDemoPodPrefix)
					Expect(err).NotTo(HaveOccurred())

					// Read data at empty state
					emptyData, err := f.ReadSampleDataFromFromWorkload(statefulset.ObjectMeta, apis.KindStatefulSet)
					Expect(err).NotTo(HaveOccurred())

					// Generate Sample Data
					sampleData, err := f.GenerateSampleData(statefulset.ObjectMeta, apis.KindStatefulSet)
					Expect(err).NotTo(HaveOccurred())
					Expect(sampleData).ShouldNot(BeSameAs(emptyData))

					// Setup a Minio Repository
					repo, err := f.SetupMinioRepository()
					Expect(err).NotTo(HaveOccurred())
					f.AppendToCleanupList(repo)

					// Setup workload Backup
					backupConfig, err := f.SetupWorkloadBackup(statefulset.ObjectMeta, repo, apis.KindStatefulSet)
					Expect(err).NotTo(HaveOccurred())

					// Take an Instant Backup of the Sample Data
					backupSession, err := f.TakeInstantBackup(backupConfig.ObjectMeta, v1beta1.BackupInvokerRef{
						Name: backupConfig.Name,
						Kind: v1beta1.ResourceKindBackupConfiguration,
					})
					Expect(err).NotTo(HaveOccurred())

					By("Verifying that BackupSession has succeeded")
					completedBS, err := f.StashClient.StashV1beta1().BackupSessions(backupSession.Namespace).Get(context.TODO(), backupSession.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					Expect(completedBS.Status.Phase).Should(Equal(v1beta1.BackupSessionSucceeded))

					// Simulate disaster scenario. Remove the old data. Then add a demo corrupted file.
					// This corrupted file will be deleted in postRestore hook.
					By("Modifying source data")
					pod, err := f.GetPod(statefulset.ObjectMeta)
					Expect(err).NotTo(HaveOccurred())
					_, err = f.ExecOnPod(pod, "/bin/sh", "-c", fmt.Sprintf("rm -rf %s/*", framework.TestSourceDataMountPath))
					Expect(err).NotTo(HaveOccurred())
					_, err = f.ExecOnPod(pod, "touch", filepath.Join(framework.TestSourceDataMountPath, "corrupted-data.txt"))
					Expect(err).NotTo(HaveOccurred())

					// Restore the backed up data
					// Try to restore a directory that hasn't been backed up. This will force the restore process to fail.
					// Remove the corrupted data in postRestore hook.
					By("Restoring the backed up data in the original StatefulSet")
					restoreSession, err := f.SetupRestoreProcess(statefulset.ObjectMeta, repo, apis.KindStatefulSet, framework.SourceVolume, func(restore *v1beta1.RestoreSession) {
						restore.Spec.Hooks = &v1beta1.RestoreHooks{
							PostRestore: &probev1.Handler{
								Exec: &core.ExecAction{
									Command: []string{"/bin/sh", "-c", fmt.Sprintf("rm %s/corrupted-data.txt", framework.TestSourceDataMountPath)},
								},
								ContainerName: apis.StashInitContainer,
							},
						}
						restore.Spec.Target.Rules = []v1beta1.Rule{
							{
								Paths: []string{"/unknown/directory"},
							},
						}
					})
					Expect(err).NotTo(HaveOccurred())

					By("Verifying that RestoreSession has failed")
					completedRS, err := f.StashClient.StashV1beta1().RestoreSessions(restoreSession.Namespace).Get(context.TODO(), restoreSession.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					Expect(completedRS.Status.Phase).Should(Equal(v1beta1.RestoreFailed))

					// Delete RestoreSession so that the StatefulSet can start normally
					By("Deleting RestoreSession")
					err = f.DeleteRestoreSession(restoreSession.ObjectMeta)
					Expect(err).NotTo(HaveOccurred())
					// delete failed pod so that StatefulSet can start
					err = f.DeletePod(pod.ObjectMeta)
					Expect(err).NotTo(HaveOccurred())
					err = app_util.WaitUntilStatefulSetReady(context.TODO(), f.KubeClient, statefulset.ObjectMeta)
					Expect(err).NotTo(HaveOccurred())

					restoredData, err := f.ReadSampleDataFromFromWorkload(statefulset.ObjectMeta, apis.KindStatefulSet)
					Expect(err).NotTo(HaveOccurred())
					By("Verifying that the corrupted file has been removed")
					Expect(restoredData).Should(BeSameAs(emptyData))
				})
			})

			Context("Failure Test", func() {
				It("should restore backed up data even when the hook failed", func() {
					// Deploy a StatefulSet with prober client. Here, we are using a StatefulSet because we need a stable address
					// for pod where http request will be sent.
					statefulset, err := f.DeployStatefulSetWithProbeClient(framework.ProberDemoPodPrefix)
					Expect(err).NotTo(HaveOccurred())

					// Read data at empty state
					emptyData, err := f.ReadSampleDataFromFromWorkload(statefulset.ObjectMeta, apis.KindStatefulSet)
					Expect(err).NotTo(HaveOccurred())

					// Generate Sample Data
					sampleData, err := f.GenerateSampleData(statefulset.ObjectMeta, apis.KindStatefulSet)
					Expect(err).NotTo(HaveOccurred())
					Expect(sampleData).ShouldNot(BeSameAs(emptyData))

					// Setup a Minio Repository
					repo, err := f.SetupMinioRepository()
					Expect(err).NotTo(HaveOccurred())
					f.AppendToCleanupList(repo)

					// Setup workload Backup
					backupConfig, err := f.SetupWorkloadBackup(statefulset.ObjectMeta, repo, apis.KindStatefulSet)
					Expect(err).NotTo(HaveOccurred())

					// Take an Instant Backup of the Sample Data
					backupSession, err := f.TakeInstantBackup(backupConfig.ObjectMeta, v1beta1.BackupInvokerRef{
						Name: backupConfig.Name,
						Kind: v1beta1.ResourceKindBackupConfiguration,
					})
					Expect(err).NotTo(HaveOccurred())

					By("Verifying that BackupSession has succeeded")
					completedBS, err := f.StashClient.StashV1beta1().BackupSessions(backupSession.Namespace).Get(context.TODO(), backupSession.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					Expect(completedBS.Status.Phase).Should(Equal(v1beta1.BackupSessionSucceeded))

					// Simulate disaster scenario. Remove old data
					By("Removing source data")
					pod, err := f.GetPod(statefulset.ObjectMeta)
					Expect(err).NotTo(HaveOccurred())
					_, err = f.ExecOnPod(pod, "/bin/sh", "-c", fmt.Sprintf("rm -rf %s/*", framework.TestSourceDataMountPath))
					Expect(err).NotTo(HaveOccurred())

					// Restore the backed up data
					// Return non-zero exit code from postRestore hook so that it fail
					By("Restoring the backed up data in the original StatefulSet")
					restoreSession, err := f.SetupRestoreProcess(statefulset.ObjectMeta, repo, apis.KindStatefulSet, framework.SourceVolume, func(restore *v1beta1.RestoreSession) {
						restore.Spec.Hooks = &v1beta1.RestoreHooks{
							PostRestore: &probev1.Handler{
								Exec: &core.ExecAction{
									Command: []string{"/bin/sh", "-c", "exit 1"},
								},
								ContainerName: apis.StashInitContainer,
							},
						}
					})
					Expect(err).NotTo(HaveOccurred())

					By("Verifying that RestoreSession has failed")
					completedRS, err := f.StashClient.StashV1beta1().RestoreSessions(restoreSession.Namespace).Get(context.TODO(), restoreSession.Name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					Expect(completedRS.Status.Phase).Should(Equal(v1beta1.RestoreFailed))

					// Delete RestoreSession so that the StatefulSet can start normally
					By("Deleting RestoreSession")
					err = f.DeleteRestoreSession(restoreSession.ObjectMeta)
					Expect(err).NotTo(HaveOccurred())
					// delete failed pod so that StatefulSet can start
					err = f.DeletePod(pod.ObjectMeta)
					Expect(err).NotTo(HaveOccurred())
					err = app_util.WaitUntilStatefulSetReady(context.TODO(), f.KubeClient, statefulset.ObjectMeta)
					Expect(err).NotTo(HaveOccurred())

					restoredData := f.RestoredData(statefulset.ObjectMeta, apis.KindStatefulSet)
					By("Verifying that data has been restored")
					Expect(restoredData).Should(BeSameAs(sampleData))
				})
			})
		})

		Context("Job Model", func() {
			Context("PVC", func() {
				Context("Success Cases", func() {
					It("should execute postRestore hook successfully", func() {
						// Create new PVC
						pvc, err := f.CreateNewPVC(fmt.Sprintf("%s-%s", framework.SourceVolume, f.App()))
						Expect(err).NotTo(HaveOccurred())

						// Deploy a Pod
						pod, err := f.DeployPod(pvc.Name)
						Expect(err).NotTo(HaveOccurred())

						// Read data at empty state
						emptyData, err := f.ReadSampleDataFromFromWorkload(pod.ObjectMeta, apis.KindPod)
						Expect(err).NotTo(HaveOccurred())

						// Generate Sample Data
						sampleData, err := f.GenerateSampleData(pod.ObjectMeta, apis.KindPod)
						Expect(err).NotTo(HaveOccurred())
						Expect(sampleData).ShouldNot(BeSameAs(emptyData))

						// Setup a Minio Repository
						repo, err := f.SetupMinioRepository()
						Expect(err).NotTo(HaveOccurred())
						f.AppendToCleanupList(repo)

						// Setup PVC Backup
						backupConfig, err := f.SetupPVCBackup(pvc, repo)
						Expect(err).NotTo(HaveOccurred())

						// Take an Instant Backup of the Sample Data
						backupSession, err := f.TakeInstantBackup(backupConfig.ObjectMeta, v1beta1.BackupInvokerRef{
							Name: backupConfig.Name,
							Kind: v1beta1.ResourceKindBackupConfiguration,
						})
						Expect(err).NotTo(HaveOccurred())

						By("Verifying that BackupSession has succeeded")
						completedBS, err := f.StashClient.StashV1beta1().BackupSessions(backupSession.Namespace).Get(context.TODO(), backupSession.Name, metav1.GetOptions{})
						Expect(err).NotTo(HaveOccurred())
						Expect(completedBS.Status.Phase).Should(Equal(v1beta1.BackupSessionSucceeded))

						// Simulate disaster scenario. Remove the old data. Then add a demo corrupted file.
						// This corrupted file will be deleted in postRestore hook.
						By("Modifying source data")
						_, err = f.ExecOnPod(pod, "/bin/sh", "-c", fmt.Sprintf("rm -rf %s/*", framework.TestSourceDataMountPath))
						Expect(err).NotTo(HaveOccurred())
						_, err = f.ExecOnPod(pod, "touch", filepath.Join(framework.TestSourceDataMountPath, "corrupted-data.txt"))
						Expect(err).NotTo(HaveOccurred())

						// Restore the backed up data
						// Cleanup corrupted data in postRestore hook
						By("Restoring the backed up data")
						restoreSession, err := f.SetupRestoreProcessForPVC(pvc, repo, func(restore *v1beta1.RestoreSession) {
							restore.Spec.Hooks = &v1beta1.RestoreHooks{
								PostRestore: &probev1.Handler{
									Exec: &core.ExecAction{
										Command: []string{"/bin/sh", "-c", fmt.Sprintf("rm %s/corrupted-data.txt", apis.StashDefaultMountPath)},
									},
									ContainerName: apis.PostTaskHook,
								},
							}
						})
						Expect(err).NotTo(HaveOccurred())

						By("Verifying that RestoreSession has succeeded")
						completedRS, err := f.StashClient.StashV1beta1().RestoreSessions(restoreSession.Namespace).Get(context.TODO(), restoreSession.Name, metav1.GetOptions{})
						Expect(err).NotTo(HaveOccurred())
						Expect(completedRS.Status.Phase).Should(Equal(v1beta1.RestoreSucceeded))

						restoredData, err := f.ReadSampleDataFromFromWorkload(pod.ObjectMeta, apis.KindPod)
						Expect(err).NotTo(HaveOccurred())
						By("Verifying that the original data has been restored and corrupted file has been removed")
						Expect(restoredData).Should(BeSameAs(sampleData))
					})

					It("should execute postRestore hook even when the restore process failed", func() {
						// Create new PVC
						pvc, err := f.CreateNewPVC(fmt.Sprintf("%s-%s", framework.SourceVolume, f.App()))
						Expect(err).NotTo(HaveOccurred())

						// Deploy a Pod
						pod, err := f.DeployPod(pvc.Name)
						Expect(err).NotTo(HaveOccurred())

						// Read data at empty state
						emptyData, err := f.ReadSampleDataFromFromWorkload(pod.ObjectMeta, apis.KindPod)
						Expect(err).NotTo(HaveOccurred())

						// Generate Sample Data
						sampleData, err := f.GenerateSampleData(pod.ObjectMeta, apis.KindPod)
						Expect(err).NotTo(HaveOccurred())
						Expect(sampleData).ShouldNot(BeSameAs(emptyData))

						// Setup a Minio Repository
						repo, err := f.SetupMinioRepository()
						Expect(err).NotTo(HaveOccurred())
						f.AppendToCleanupList(repo)

						// Setup PVC Backup
						backupConfig, err := f.SetupPVCBackup(pvc, repo)
						Expect(err).NotTo(HaveOccurred())

						// Take an Instant Backup of the Sample Data
						backupSession, err := f.TakeInstantBackup(backupConfig.ObjectMeta, v1beta1.BackupInvokerRef{
							Name: backupConfig.Name,
							Kind: v1beta1.ResourceKindBackupConfiguration,
						})
						Expect(err).NotTo(HaveOccurred())

						By("Verifying that BackupSession has succeeded")
						completedBS, err := f.StashClient.StashV1beta1().BackupSessions(backupSession.Namespace).Get(context.TODO(), backupSession.Name, metav1.GetOptions{})
						Expect(err).NotTo(HaveOccurred())
						Expect(completedBS.Status.Phase).Should(Equal(v1beta1.BackupSessionSucceeded))

						// Simulate disaster scenario. Remove the old data. Then add a demo corrupted file.
						// This corrupted file will be deleted in postRestore hook.
						By("Modifying source data")
						_, err = f.ExecOnPod(pod, "/bin/sh", "-c", fmt.Sprintf("rm -rf %s/*", framework.TestSourceDataMountPath))
						Expect(err).NotTo(HaveOccurred())
						_, err = f.ExecOnPod(pod, "touch", filepath.Join(framework.TestSourceDataMountPath, "corrupted-data.txt"))
						Expect(err).NotTo(HaveOccurred())

						// Restore the backed up data
						// Try to restore an invalid snapshot so that the restore process fail
						// Cleanup corrupted data in postRestore hook
						By("Restoring the backed up data")
						restoreSession, err := f.SetupRestoreProcessForPVC(pvc, repo, func(restore *v1beta1.RestoreSession) {
							restore.Spec.Hooks = &v1beta1.RestoreHooks{
								PostRestore: &probev1.Handler{
									Exec: &core.ExecAction{
										Command: []string{"/bin/sh", "-c", fmt.Sprintf("rm %s/corrupted-data.txt", apis.StashDefaultMountPath)},
									},
									ContainerName: apis.PostTaskHook,
								},
							}
							restore.Spec.Target.Rules = []v1beta1.Rule{
								{
									Snapshots: []string{"invalid-snapshot"},
								},
							}
						})
						Expect(err).NotTo(HaveOccurred())

						By("Verifying that RestoreSession has failed")
						completedRS, err := f.StashClient.StashV1beta1().RestoreSessions(restoreSession.Namespace).Get(context.TODO(), restoreSession.Name, metav1.GetOptions{})
						Expect(err).NotTo(HaveOccurred())
						Expect(completedRS.Status.Phase).Should(Equal(v1beta1.RestoreFailed))

						restoredData, err := f.ReadSampleDataFromFromWorkload(pod.ObjectMeta, apis.KindPod)
						Expect(err).NotTo(HaveOccurred())
						By("Verifying that the corrupted file has been removed")
						Expect(restoredData).Should(BeSameAs(emptyData))
					})
				})

				Context("Failure Cases", func() {
					It("should restore the backed up data even when the hook failed", func() {
						// Create new PVC
						pvc, err := f.CreateNewPVC(fmt.Sprintf("%s-%s", framework.SourceVolume, f.App()))
						Expect(err).NotTo(HaveOccurred())

						// Deploy a Pod
						pod, err := f.DeployPod(pvc.Name)
						Expect(err).NotTo(HaveOccurred())

						// Read data at empty state
						emptyData, err := f.ReadSampleDataFromFromWorkload(pod.ObjectMeta, apis.KindPod)
						Expect(err).NotTo(HaveOccurred())

						// Generate Sample Data
						sampleData, err := f.GenerateSampleData(pod.ObjectMeta, apis.KindPod)
						Expect(err).NotTo(HaveOccurred())
						Expect(sampleData).ShouldNot(BeSameAs(emptyData))

						// Setup a Minio Repository
						repo, err := f.SetupMinioRepository()
						Expect(err).NotTo(HaveOccurred())
						f.AppendToCleanupList(repo)

						// Setup PVC Backup
						backupConfig, err := f.SetupPVCBackup(pvc, repo)
						Expect(err).NotTo(HaveOccurred())

						// Take an Instant Backup of the Sample Data
						backupSession, err := f.TakeInstantBackup(backupConfig.ObjectMeta, v1beta1.BackupInvokerRef{
							Name: backupConfig.Name,
							Kind: v1beta1.ResourceKindBackupConfiguration,
						})
						Expect(err).NotTo(HaveOccurred())

						By("Verifying that BackupSession has succeeded")
						completedBS, err := f.StashClient.StashV1beta1().BackupSessions(backupSession.Namespace).Get(context.TODO(), backupSession.Name, metav1.GetOptions{})
						Expect(err).NotTo(HaveOccurred())
						Expect(completedBS.Status.Phase).Should(Equal(v1beta1.BackupSessionSucceeded))

						// Simulate disaster scenario. Remove old data
						By("Removing source data")
						_, err = f.ExecOnPod(pod, "/bin/sh", "-c", fmt.Sprintf("rm -rf %s/*", framework.TestSourceDataMountPath))
						Expect(err).NotTo(HaveOccurred())

						// Restore the backed up data
						// Return non-zero exit code from postRestore hook so that the hook fail
						By("Restoring the backed up data")
						restoreSession, err := f.SetupRestoreProcessForPVC(pvc, repo, func(restore *v1beta1.RestoreSession) {
							restore.Spec.Hooks = &v1beta1.RestoreHooks{
								PostRestore: &probev1.Handler{
									Exec: &core.ExecAction{
										Command: []string{"/bin/sh", "-c", "exit 1"},
									},
									ContainerName: apis.PostTaskHook,
								},
							}
						})
						Expect(err).NotTo(HaveOccurred())

						By("Verifying that the RestoreSession has failed")
						completedRS, err := f.StashClient.StashV1beta1().RestoreSessions(restoreSession.Namespace).Get(context.TODO(), restoreSession.Name, metav1.GetOptions{})
						Expect(err).NotTo(HaveOccurred())
						Expect(completedRS.Status.Phase).Should(Equal(v1beta1.RestoreFailed))

						restoredData := f.RestoredData(pod.ObjectMeta, apis.KindPod)
						By("Verifying that the sample data has been restored")
						Expect(restoredData).Should(BeSameAs(sampleData))
					})
				})
			})
		})
	})

	Context("Send notification to Slack webhook", func() {
		BeforeEach(func() {
			if f.SlackWebhookURL == "" {
				Skip("Slack Webhook URL is missing")
			}
		})
		It("should send restore success notification", func() {
			// Deploy a StatefulSet with prober client. Here, we are using a StatefulSet because we need a stable address
			// for pod where http request will be sent.
			statefulset, err := f.DeployStatefulSetWithProbeClient(framework.ProberDemoPodPrefix)
			Expect(err).NotTo(HaveOccurred())

			// Read data at empty state
			emptyData, err := f.ReadSampleDataFromFromWorkload(statefulset.ObjectMeta, apis.KindStatefulSet)
			Expect(err).NotTo(HaveOccurred())

			// Generate Sample Data
			sampleData, err := f.GenerateSampleData(statefulset.ObjectMeta, apis.KindStatefulSet)
			Expect(err).NotTo(HaveOccurred())
			Expect(sampleData).ShouldNot(BeSameAs(emptyData))

			// Setup a Minio Repository
			repo, err := f.SetupMinioRepository()
			Expect(err).NotTo(HaveOccurred())
			f.AppendToCleanupList(repo)

			// Setup workload Backup
			backupConfig, err := f.SetupWorkloadBackup(statefulset.ObjectMeta, repo, apis.KindStatefulSet)
			Expect(err).NotTo(HaveOccurred())

			// Take an Instant Backup of the Sample Data
			backupSession, err := f.TakeInstantBackup(backupConfig.ObjectMeta, v1beta1.BackupInvokerRef{
				Name: backupConfig.Name,
				Kind: v1beta1.ResourceKindBackupConfiguration,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that BackupSession has succeeded")
			completedBS, err := f.StashClient.StashV1beta1().BackupSessions(backupSession.Namespace).Get(context.TODO(), backupSession.Name, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(completedBS.Status.Phase).Should(Equal(v1beta1.BackupSessionSucceeded))

			// Simulate disaster scenario. Remove the old data. Then add a demo corrupted file.
			// This corrupted file will be deleted in postRestore hook.
			By("Modifying source data")
			pod, err := f.GetPod(statefulset.ObjectMeta)
			Expect(err).NotTo(HaveOccurred())
			_, err = f.ExecOnPod(pod, "/bin/sh", "-c", fmt.Sprintf("rm -rf %s/*", framework.TestSourceDataMountPath))
			Expect(err).NotTo(HaveOccurred())

			// Restore the backed up data
			// Remove the corrupted data in postRestore hook.
			By("Restoring the backed up data in the original StatefulSet")
			restoreSession, err := f.SetupRestoreProcess(statefulset.ObjectMeta, repo, apis.KindStatefulSet, framework.SourceVolume, func(restore *v1beta1.RestoreSession) {
				restore.Spec.Hooks = &v1beta1.RestoreHooks{
					PostRestore: &probev1.Handler{
						HTTPPost: &probev1.HTTPPostAction{
							Host:   "hooks.slack.com",
							Path:   f.SlackWebhookURL,
							Port:   intstr.FromInt(443),
							Scheme: "HTTPS",
							HTTPHeaders: []core.HTTPHeader{
								{
									Name:  "Content-Type",
									Value: "application/json",
								},
							},
							Body: "{\"blocks\": [{\"type\": \"section\",\"text\": {\"type\": \"mrkdwn\",\"text\": \"{{if eq .Status.Phase `Succeeded`}}:white_check_mark: Restore succeeded for {{ .Namespace }}/{{.Target.Name}}{{else}}:x: Restore failed for {{ .Namespace }}/{{.Target.Name}} Reason: {{.Status.Error}}.{{end}}\"}}]}",
						},
						ContainerName: framework.ProberDemoPodPrefix,
					},
				}
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that RestoreSession succeeded")
			completedRS, err := f.StashClient.StashV1beta1().RestoreSessions(restoreSession.Namespace).Get(context.TODO(), restoreSession.Name, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(completedRS.Status.Phase).Should(Equal(v1beta1.RestoreSucceeded))

			restoredData := f.RestoredData(statefulset.ObjectMeta, apis.KindStatefulSet)
			By("Verifying that the original data has been restored and corrupted file has been removed")
			Expect(restoredData).Should(BeSameAs(sampleData))
		})
		It("should send restore failure notification", func() {
			// Deploy a StatefulSet with prober client. Here, we are using a StatefulSet because we need a stable address
			// for pod where http request will be sent.
			statefulset, err := f.DeployStatefulSetWithProbeClient(framework.ProberDemoPodPrefix)
			Expect(err).NotTo(HaveOccurred())

			// Read data at empty state
			emptyData, err := f.ReadSampleDataFromFromWorkload(statefulset.ObjectMeta, apis.KindStatefulSet)
			Expect(err).NotTo(HaveOccurred())

			// Generate Sample Data
			sampleData, err := f.GenerateSampleData(statefulset.ObjectMeta, apis.KindStatefulSet)
			Expect(err).NotTo(HaveOccurred())
			Expect(sampleData).ShouldNot(BeSameAs(emptyData))

			// Setup a Minio Repository
			repo, err := f.SetupMinioRepository()
			Expect(err).NotTo(HaveOccurred())
			f.AppendToCleanupList(repo)

			// Setup workload Backup
			backupConfig, err := f.SetupWorkloadBackup(statefulset.ObjectMeta, repo, apis.KindStatefulSet)
			Expect(err).NotTo(HaveOccurred())

			// Take an Instant Backup of the Sample Data
			backupSession, err := f.TakeInstantBackup(backupConfig.ObjectMeta, v1beta1.BackupInvokerRef{
				Name: backupConfig.Name,
				Kind: v1beta1.ResourceKindBackupConfiguration,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that BackupSession has succeeded")
			completedBS, err := f.StashClient.StashV1beta1().BackupSessions(backupSession.Namespace).Get(context.TODO(), backupSession.Name, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(completedBS.Status.Phase).Should(Equal(v1beta1.BackupSessionSucceeded))

			// Simulate disaster scenario. Remove the old data. Then add a demo corrupted file.
			// This corrupted file will be deleted in postRestore hook.
			By("Modifying source data")
			pod, err := f.GetPod(statefulset.ObjectMeta)
			Expect(err).NotTo(HaveOccurred())
			_, err = f.ExecOnPod(pod, "/bin/sh", "-c", fmt.Sprintf("rm -rf %s/*", framework.TestSourceDataMountPath))
			Expect(err).NotTo(HaveOccurred())

			// Restore the backed up data
			// Remove the corrupted data in postRestore hook.
			By("Restoring the backed up data in the original StatefulSet")
			restoreSession, err := f.SetupRestoreProcess(statefulset.ObjectMeta, repo, apis.KindStatefulSet, framework.SourceVolume, func(restore *v1beta1.RestoreSession) {
				restore.Spec.Hooks = &v1beta1.RestoreHooks{
					PostRestore: &probev1.Handler{
						HTTPPost: &probev1.HTTPPostAction{
							Host:   "hooks.slack.com",
							Path:   f.SlackWebhookURL,
							Port:   intstr.FromInt(443),
							Scheme: "HTTPS",
							HTTPHeaders: []core.HTTPHeader{
								{
									Name:  "Content-Type",
									Value: "application/json",
								},
							},
							Body: "{\"blocks\": [{\"type\": \"section\",\"text\": {\"type\": \"mrkdwn\",\"text\": \"{{if eq .Status.Phase `Succeeded`}}:white_check_mark: Restore succeeded for {{ .Namespace }}/{{.Target.Name}}{{else}}:x: Restore failed for {{ .Namespace }}/{{.Target.Name}} Reason: {{.Status.Error}}.{{end}}\"}}]}",
						},
						ContainerName: framework.ProberDemoPodPrefix,
					},
				}
				restore.Spec.Target.Rules = []v1beta1.Rule{
					{
						Paths: []string{"/some/non/existing/path"},
					},
				}
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that RestoreSession succeeded")
			completedRS, err := f.StashClient.StashV1beta1().RestoreSessions(restoreSession.Namespace).Get(context.TODO(), restoreSession.Name, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(completedRS.Status.Phase).Should(Equal(v1beta1.RestoreFailed))
		})
	})
})
