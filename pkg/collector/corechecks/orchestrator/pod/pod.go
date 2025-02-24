// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.
//go:build kubelet && orchestrator

// Package pod is used for the orchestrator pod check
package pod

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/atomic"

	"github.com/DataDog/datadog-agent/comp/core/autodiscovery/integration"
	"github.com/DataDog/datadog-agent/pkg/aggregator/sender"
	"github.com/DataDog/datadog-agent/pkg/collector/check"
	core "github.com/DataDog/datadog-agent/pkg/collector/corechecks"
	"github.com/DataDog/datadog-agent/pkg/collector/corechecks/cluster/orchestrator/processors"
	k8sProcessors "github.com/DataDog/datadog-agent/pkg/collector/corechecks/cluster/orchestrator/processors/k8s"
	"github.com/DataDog/datadog-agent/pkg/orchestrator"
	oconfig "github.com/DataDog/datadog-agent/pkg/orchestrator/config"
	"github.com/DataDog/datadog-agent/pkg/util/hostname"
	"github.com/DataDog/datadog-agent/pkg/util/kubernetes/clustername"
	"github.com/DataDog/datadog-agent/pkg/util/kubernetes/kubelet"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	"github.com/DataDog/datadog-agent/pkg/util/optional"
)

// CheckName is the name of the check
const CheckName = "orchestrator_pod"

var groupID atomic.Int32

func nextGroupID() int32 {
	groupID.Add(1)
	return groupID.Load()
}

// Check doesn't need additional fields
type Check struct {
	core.CheckBase
	hostName  string
	clusterID string
	sender    sender.Sender
	processor *processors.Processor
	config    *oconfig.OrchestratorConfig
}

// Factory creates a new check factory
func Factory() optional.Option[func() check.Check] {
	return optional.NewOption(newCheck)
}

func newCheck() check.Check {
	return &Check{
		CheckBase: core.NewCheckBase(CheckName),
		config:    oconfig.NewDefaultOrchestratorConfig(),
	}
}

// Configure the CPU check
// nil check to allow for overrides
func (c *Check) Configure(
	senderManager sender.SenderManager,
	integrationConfigDigest uint64,
	data integration.Data,
	initConfig integration.Data,
	source string,
) error {
	c.BuildID(integrationConfigDigest, data, initConfig)

	err := c.CommonConfigure(senderManager, integrationConfigDigest, initConfig, data, source)
	if err != nil {
		return err
	}

	err = c.config.Load()
	if err != nil {
		return err
	}
	if !c.config.OrchestrationCollectionEnabled {
		log.Warn("orchestrator pod check is configured but the feature is disabled")
		return nil
	}
	if c.config.KubeClusterName == "" {
		return errors.New("orchestrator check is configured but the cluster name is empty")
	}

	if c.processor == nil {
		c.processor = processors.NewProcessor(new(k8sProcessors.PodHandlers))
	}

	if c.sender == nil {
		sender, err := c.GetSender()
		if err != nil {
			return err
		}
		c.sender = sender
	}

	if c.hostName == "" {
		hname, _ := hostname.Get(context.TODO())
		c.hostName = hname
	}

	return nil
}

// Run executes the check
func (c *Check) Run() error {
	if c.clusterID == "" {
		clusterID, err := clustername.GetClusterID()
		if err != nil {
			return err
		}
		c.clusterID = clusterID
	}

	kubeUtil, err := kubelet.GetKubeUtil()
	if err != nil {
		return err
	}

	podList, err := kubeUtil.GetRawLocalPodList(context.TODO())
	if err != nil {
		return err
	}

	groupID := nextGroupID()
	ctx := &processors.K8sProcessorContext{
		BaseProcessorContext: processors.BaseProcessorContext{
			Cfg:        c.config,
			MsgGroupID: groupID,
			NodeType:   orchestrator.K8sPod,
			ClusterID:  c.clusterID,
		},
		HostName:           c.hostName,
		ApiGroupVersionTag: "kube_api_version:v1",
	}

	processResult, processed := c.processor.Process(ctx, podList)
	if processed == -1 {
		return fmt.Errorf("unable to process pods: a panic occurred")
	}

	orchestrator.SetCacheStats(len(podList), processed, ctx.NodeType)

	c.sender.OrchestratorMetadata(processResult.MetadataMessages, c.clusterID, int(orchestrator.K8sPod))
	c.sender.OrchestratorManifest(processResult.ManifestMessages, c.clusterID)

	return nil
}
