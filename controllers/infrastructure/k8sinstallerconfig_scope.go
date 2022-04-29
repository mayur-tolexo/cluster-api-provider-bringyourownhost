// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	infrav1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// k8sInstallerConfigScope defines a scope defined around a K8sInstallerConfig and its ByoMachine
type k8sInstallerConfigScope struct {
	Client      client.Client
	PatchHelper *patch.Helper
	Cluster     *clusterv1.Cluster
	ByoMachine  *infrav1.ByoMachine
	Config      *infrav1.K8sInstallerConfig
}
