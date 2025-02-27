/*
Copyright The CloudNativePG Contributors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package status implements the kubectl-cnpg status command
package status

import (
	"context"
	"fmt"
	"os"
	"path"
	"sort"
	"time"

	"github.com/cheynewallace/tabby"
	"github.com/logrusorgru/aurora/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/cloudnative-pg/cloudnative-pg/internal/cmd/plugin"
	"github.com/cloudnative-pg/cloudnative-pg/internal/plugin/resources"
	"github.com/cloudnative-pg/cloudnative-pg/pkg/management/postgres/constants"
	"github.com/cloudnative-pg/cloudnative-pg/pkg/postgres"
	"github.com/cloudnative-pg/cloudnative-pg/pkg/specs"
	"github.com/cloudnative-pg/cloudnative-pg/pkg/utils"
)

// PostgresqlStatus contains the status of the Cluster and of all its instances
type PostgresqlStatus struct {
	// Cluster is the Cluster we are investigating
	Cluster *apiv1.Cluster `json:"cluster"`

	// InstanceStatus is the status of each instance, extracted directly
	// from the instance manager running into each Pod
	InstanceStatus *postgres.PostgresqlStatusList `json:"instanceStatus"`

	// PrimaryPod contains the primary Pod
	PrimaryPod corev1.Pod
}

// Status implements the "status" subcommand
func Status(ctx context.Context, clusterName string, verbose bool, format plugin.OutputFormat) error {
	status, err := ExtractPostgresqlStatus(ctx, clusterName)
	if err != nil {
		return err
	}

	err = plugin.Print(status, format, os.Stdout)
	if err != nil {
		return err
	}

	if format != plugin.OutputFormatText {
		return nil
	}

	status.printBasicInfo()
	var nonFatalError error
	if verbose {
		err = status.printPostgresConfiguration(ctx)
		if err != nil {
			nonFatalError = err
		}
	}
	status.printCertificatesStatus()
	status.printBackupStatus()
	status.printReplicaStatus()
	status.printInstancesStatus()

	if nonFatalError != nil {
		return nonFatalError
	}
	return nil
}

// ExtractPostgresqlStatus gets the PostgreSQL status using the Kubernetes API
func ExtractPostgresqlStatus(ctx context.Context, clusterName string) (*PostgresqlStatus, error) {
	var cluster apiv1.Cluster

	// Get the Cluster object
	err := plugin.Client.Get(ctx, client.ObjectKey{Namespace: plugin.Namespace, Name: clusterName}, &cluster)
	if err != nil {
		return nil, err
	}

	// Get the list of Pods created by this Cluster
	var instancesStatus postgres.PostgresqlStatusList
	managedPods, primaryPod, err := resources.GetInstancePods(ctx, clusterName)
	if err != nil {
		return nil, err
	}

	instancesStatus = resources.ExtractInstancesStatus(
		ctx,
		plugin.Config,
		managedPods,
		specs.PostgresContainerName)

	// Extract the status from the instances
	status := PostgresqlStatus{
		Cluster:        &cluster,
		InstanceStatus: &instancesStatus,
		PrimaryPod:     primaryPod,
	}
	return &status, nil
}

func (fullStatus *PostgresqlStatus) printBasicInfo() {
	fmt.Println(aurora.Green("Cluster Summary"))
	summary := tabby.New()

	cluster := fullStatus.Cluster

	primaryInstance := cluster.Status.CurrentPrimary
	if cluster.Status.CurrentPrimary != cluster.Status.TargetPrimary {
		primaryInstance = fmt.Sprintf("%v (switching to %v)",
			cluster.Status.CurrentPrimary, cluster.Status.TargetPrimary)
	}

	var status string
	switch cluster.Status.Phase {
	case apiv1.PhaseHealthy, apiv1.PhaseFirstPrimary, apiv1.PhaseCreatingReplica:
		status = fmt.Sprintf("%v %v", aurora.Green(cluster.Status.Phase), cluster.Status.PhaseReason)
	case apiv1.PhaseUpgrade, apiv1.PhaseWaitingForUser:
		status = fmt.Sprintf("%v %v", aurora.Yellow(cluster.Status.Phase), cluster.Status.PhaseReason)
	default:
		status = fmt.Sprintf("%v %v", aurora.Red(cluster.Status.Phase), cluster.Status.PhaseReason)
	}

	primaryInstanceStatus := fullStatus.tryGetPrimaryInstance()

	summary.AddLine("Name:", cluster.Name)
	summary.AddLine("Namespace:", cluster.Namespace)
	if primaryInstanceStatus != nil {
		summary.AddLine("System ID:", primaryInstanceStatus.SystemID)
	}
	summary.AddLine("PostgreSQL Image:", cluster.GetImageName())
	summary.AddLine("Primary instance:", primaryInstance)
	summary.AddLine("Status:", status)
	if cluster.Spec.Instances == cluster.Status.Instances {
		summary.AddLine("Instances:", aurora.Green(cluster.Spec.Instances))
	} else {
		summary.AddLine("Instances:", aurora.Red(cluster.Spec.Instances))
	}
	if cluster.Spec.Instances == cluster.Status.ReadyInstances {
		summary.AddLine("Ready instances:", aurora.Green(cluster.Status.ReadyInstances))
	} else {
		summary.AddLine("Ready instances:", aurora.Red(cluster.Status.ReadyInstances))
	}

	if cluster.Status.CurrentPrimary != cluster.Status.TargetPrimary {
		if cluster.Status.CurrentPrimary == "" {
			fmt.Println(aurora.Red("Primary server is initializing"))
		} else {
			fmt.Println(aurora.Red("Switchover in progress"))
		}
	}
	if !cluster.IsReplica() && primaryInstanceStatus != nil {
		lsnInfo := fmt.Sprintf(
			"%s (Timeline: %d - WAL File: %s)",
			primaryInstanceStatus.CurrentLsn,
			primaryInstanceStatus.TimeLineID,
			primaryInstanceStatus.CurrentWAL,
		)
		summary.AddLine("Current Write LSN:", lsnInfo)
	}

	summary.Print()
	fmt.Println()
}

func (fullStatus *PostgresqlStatus) printPostgresConfiguration(ctx context.Context) error {
	timeout := time.Second * 2
	clientInterface := kubernetes.NewForConfigOrDie(plugin.Config)

	// Read PostgreSQL configuration from custom.conf
	customConf, _, err := utils.ExecCommand(ctx, clientInterface, plugin.Config, fullStatus.PrimaryPod,
		specs.PostgresContainerName,
		&timeout,
		"cat",
		path.Join(specs.PgDataPath, constants.PostgresqlCustomConfigurationFile))
	if err != nil {
		return err
	}

	// Read PostgreSQL HBA Rules from pg_hba.conf
	pgHBAConf, _, err := utils.ExecCommand(ctx, clientInterface, plugin.Config, fullStatus.PrimaryPod,
		specs.PostgresContainerName,
		&timeout, "cat", path.Join(specs.PgDataPath, constants.PostgresqlHBARulesFile))
	if err != nil {
		return err
	}

	fmt.Println(aurora.Green("PostgreSQL Configuration"))
	fmt.Println(customConf)
	fmt.Println()

	fmt.Println(aurora.Green("PostgreSQL HBA Rules"))
	fmt.Println(pgHBAConf)
	fmt.Println()

	return nil
}

func (fullStatus *PostgresqlStatus) printBackupStatus() {
	cluster := fullStatus.Cluster

	fmt.Println(aurora.Green("Continuous Backup status"))
	if cluster.Spec.Backup == nil {
		fmt.Println(aurora.Yellow("Not configured"))
		fmt.Println()
		return
	}
	status := tabby.New()
	FPoR := cluster.Status.FirstRecoverabilityPoint
	if FPoR == "" {
		FPoR = "Not Available"
	}
	status.AddLine("First Point of Recoverability:", FPoR)

	primaryInstanceStatus := fullStatus.tryGetPrimaryInstance()
	if primaryInstanceStatus == nil {
		status.AddLine("No Primary instance found")
		return
	}
	status.AddLine("Working WAL archiving:",
		getWalArchivingStatus(primaryInstanceStatus.IsArchivingWAL, primaryInstanceStatus.LastFailedWAL))
	status.AddLine("WALs waiting to be archived:", primaryInstanceStatus.ReadyWALFiles)

	if primaryInstanceStatus.LastArchivedWAL == "" {
		status.AddLine("Last Archived WAL:", "-")
	} else {
		status.AddLine("Last Archived WAL:", primaryInstanceStatus.LastArchivedWAL,
			" @ ", primaryInstanceStatus.LastArchivedWALTime)
	}
	if primaryInstanceStatus.LastFailedWAL == "" {
		status.AddLine("Last Failed WAL:", "-")
	} else {
		status.AddLine("Last Failed WAL:", primaryInstanceStatus.LastFailedWAL,
			" @ ", primaryInstanceStatus.LastFailedWALTime)
	}

	status.Print()
	fmt.Println()
}

func getWalArchivingStatus(isArchivingWAL bool, lastFailedWAL string) string {
	switch {
	case isArchivingWAL:
		return aurora.Green("OK").String()
	case lastFailedWAL != "":
		return aurora.Red("Failing").String()
	default:
		return aurora.Yellow("Starting Up").String()
	}
}

func (fullStatus *PostgresqlStatus) printReplicaStatus() {
	fmt.Println(aurora.Green("Streaming Replication status"))
	if fullStatus.Cluster.Spec.Instances == 1 {
		fmt.Println(aurora.Yellow("Not configured").String())
		fmt.Println()
		return
	}

	primaryInstanceStatus := fullStatus.tryGetPrimaryInstance()
	if primaryInstanceStatus == nil {
		fmt.Println(aurora.Yellow("Primary instance not found").String())
		fmt.Println()
		return
	}

	if len(primaryInstanceStatus.ReplicationInfo) == 0 {
		fmt.Println(aurora.Yellow("Not available yet").String())
		fmt.Println()
		return
	}

	status := tabby.New()
	status.AddHeader(
		"Name",
		"Sent LSN",
		"Write LSN",
		"Flush LSN",
		"Replay LSN", // For standby use "Replay LSN"
		"Write Lag",
		"Flush Lag",
		"Replay Lag",
		"State",
		"Sync State",
		"Sync Priority",
	)

	replicationInfo := primaryInstanceStatus.ReplicationInfo
	sort.Sort(replicationInfo)
	for _, replication := range replicationInfo {
		status.AddLine(
			replication.ApplicationName,
			replication.SentLsn,
			replication.WriteLsn,
			replication.FlushLsn,
			replication.ReplayLsn,
			replication.WriteLag,
			replication.FlushLag,
			replication.ReplayLag,
			replication.State,
			replication.SyncState,
			replication.SyncPriority,
		)
	}
	status.Print()
	fmt.Println()
}

func (fullStatus *PostgresqlStatus) printInstancesStatus() {
	//  Column "Replication role"
	//  If instance is primary, print "Primary"
	//  	Otherwise, it is considered a standby
	//  else if it is not replicating:
	//  	if it is accepting connections: # readiness OK
	//      	print "Standby (file based)"
	//    	else:
	//  		if pg_rewind is running, print "Standby (pg_rewind)"  - #liveness OK, readiness Not OK
	//    		else print "Standby (starting up)"  - #liveness OK, readiness Not OK
	//  else:
	//  	if it is paused, print "Standby (paused)"
	//  	else if SyncState = sync/quorum print "Standby (sync)"
	//  	else print "Standby (async)"

	status := tabby.New()
	fmt.Println(aurora.Green("Instances status"))
	status.AddHeader(
		"Name",
		"Database Size",
		"Current LSN", // For standby use "Replay LSN"
		"Replication role",
		"Status",
		"QoS",
		"Manager Version",
		"Node")

	sort.Sort(fullStatus.InstanceStatus)
	for _, instance := range fullStatus.InstanceStatus.Items {
		if instance.Error != nil {
			status.AddLine(
				instance.Pod.Name,
				"-",
				"-",
				"-",
				instance.Error.Error(),
				instance.Pod.Status.QOSClass,
				"-",
				instance.Pod.Spec.NodeName,
			)
			continue
		}
		statusMsg := "OK"
		if instance.PendingRestart {
			statusMsg += " (pending restart)"
		}

		replicaRole := getReplicaRole(instance, fullStatus)
		status.AddLine(
			instance.Pod.Name,
			instance.TotalInstanceSize,
			getCurrentLSN(instance),
			replicaRole,
			statusMsg,
			instance.Pod.Status.QOSClass,
			instance.InstanceManagerVersion,
			instance.Pod.Spec.NodeName,
		)
		continue
	}
	status.Print()
}

func (fullStatus *PostgresqlStatus) printCertificatesStatus() {
	status := tabby.New()
	status.AddHeader("Certificate Name", "Expiration Date", "Days Left Until Expiration")

	hasExpiringCertificate := false
	hasExpiredCertificate := false

	certExpirations := fullStatus.Cluster.Status.Certificates.Expirations

	// Sort `fullStatus.Cluster.Status.Certificates.Expirations` by `certificationName` asc
	certNames := make([]string, 0, len(certExpirations))
	for certName := range certExpirations {
		certNames = append(certNames, certName)
	}
	sort.Strings(certNames)

	for _, certName := range certNames {
		expirationDate := certExpirations[certName]
		expirationTime, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", expirationDate)
		if err != nil {
			fmt.Printf("\n error while parsing the following certificate: %s, date: %s",
				certName, expirationDate)
		}

		validityLeft := time.Until(expirationTime)

		validityLeftInDays := fmt.Sprintf("%.2f", validityLeft.Hours()/24)

		if validityLeft < 0 {
			validityLeftInDays = "Expired"
			hasExpiredCertificate = true
		} else if validityLeft < time.Hour*24*7 {
			validityLeftInDays += " - Expires Soon"
			hasExpiringCertificate = true
		}
		status.AddLine(certName, expirationDate, validityLeftInDays)
	}

	color := aurora.Green

	if hasExpiredCertificate {
		color = aurora.Red
	} else if hasExpiringCertificate {
		color = aurora.Yellow
	}

	fmt.Println(color("Certificates Status"))
	status.Print()
	fmt.Println()
}

func (fullStatus *PostgresqlStatus) tryGetPrimaryInstance() *postgres.PostgresqlStatus {
	for idx, instanceStatus := range fullStatus.InstanceStatus.Items {
		if instanceStatus.IsPrimary || len(instanceStatus.ReplicationInfo) > 0 {
			return &fullStatus.InstanceStatus.Items[idx]
		}
	}

	return nil
}

func getCurrentLSN(instance postgres.PostgresqlStatus) postgres.LSN {
	if instance.IsPrimary {
		return instance.CurrentLsn
	}
	return instance.ReplayLsn
}

func getReplicaRole(instance postgres.PostgresqlStatus, fullStatus *PostgresqlStatus) string {
	if instance.IsPrimary {
		return "Primary"
	}
	if fullStatus.Cluster.IsReplica() && len(instance.ReplicationInfo) > 0 {
		return "Designated primary"
	}

	if !instance.IsWalReceiverActive {
		if utils.IsPodReady(instance.Pod) {
			return "Standby (file based)"
		}
		if instance.IsPgRewindRunning {
			return "Standby (pg_rewind)"
		}
		return "Standby (starting up)"
	}

	if instance.ReplayPaused {
		return "Standby (paused)"
	}

	primaryInstanceStatus := fullStatus.tryGetPrimaryInstance()
	if primaryInstanceStatus == nil {
		return "Unknown"
	}

	for _, state := range primaryInstanceStatus.ReplicationInfo {
		// todo: handle others states other than 'streaming'
		if !(state.ApplicationName == instance.Pod.Name && state.State == "streaming") {
			continue
		}
		switch state.SyncState {
		case "quorum", "sync":
			return "Standby (sync)"
		case "async":
			return "Standby (async)"
		default:
			continue
		}
	}

	return "Unknown"
}
