package labs

import (
	"context"
	"errors"
	"fmt"

	"crucible/internal/agenthub"
	ap "crucible/internal/agentproto"
	"crucible/internal/apperr"
	"crucible/internal/content"
)

// AgentClusterRunner runs runtime: cluster labs on a cluster of the trainee's choosing through their laptop agent:
// the agent installs the lab's charts with the toolbox image the server pins (spec 2026-10-11). Free and
// self-reported, like laptop labs. Terminals and scripts reach the toolbox through the hub as for laptop labs.
type AgentClusterRunner struct {
	Hub     *agenthub.Hub
	Toolbox string // CRUCIBLE_TOOLBOX_IMAGE; empty: cluster labs are unavailable

	// test seams; nil = the Hub
	call     func(ctx context.Context, userID int64, m ap.Msg) (ap.Msg, error)
	contexts func(userID int64) []ap.KubeContext
	online   func(userID int64) bool
}

var _ Runner = AgentClusterRunner{}
var _ labProvisioner = AgentClusterRunner{}

func (a AgentClusterRunner) local() LocalRunner { return LocalRunner{Hub: a.Hub} }

func (a AgentClusterRunner) Contexts(userID int64) []ap.KubeContext {
	if a.contexts != nil {
		return a.contexts(userID)
	}
	return a.Hub.Contexts(userID)
}

func (a AgentClusterRunner) isOnline(userID int64) bool {
	if a.online != nil {
		return a.online(userID)
	}
	return a.Hub.Online(userID)
}

func (a AgentClusterRunner) Available(inst *Instance) error {
	if a.Toolbox == "" {
		return apperr.Wrap(apperr.Unavailable, "cluster labs are not set up on this server (CRUCIBLE_TOOLBOX_IMAGE)")
	}
	if !a.isOnline(inst.UserID) {
		return errAgentOffline
	}
	if inst.ClusterContext != "" {
		return a.CheckContext(inst.UserID, inst.ClusterContext)
	}
	return nil
}

// CheckContext: the trainee picked a context their agent reported.
func (a AgentClusterRunner) CheckContext(userID int64, name string) error {
	if name == "" {
		return apperr.Wrap(apperr.Invalid, "pick a cluster from your kubeconfig to run this lab on")
	}
	for _, c := range a.Contexts(userID) {
		if c.Name == name {
			return nil
		}
	}
	return apperr.Wrap(apperr.Invalid, fmt.Sprintf("%q is not in your kubeconfig any more; pick another cluster", name))
}

func clusterSpec(lab *content.Lab, kubeContext, toolbox string) ap.ClusterSpec {
	s := ap.ClusterSpec{Context: kubeContext, Toolbox: toolbox, ClusterWide: lab.Cluster.ClusterWide}
	for _, c := range lab.Cluster.Charts {
		s.Charts = append(s.Charts, ap.Chart{Name: c.Name, Path: c.Path, Values: c.Values, TimeoutS: int64(c.Timeout.D().Seconds())})
	}
	return s
}

func (a AgentClusterRunner) ProvisionLab(ctx context.Context, inst *Instance, lab *content.Lab) error {
	if lab.Cluster == nil {
		return errors.New("not a cluster lab")
	}
	bundle, err := Bundle(lab.Dir, "cluster")
	if err != nil {
		return err
	}
	spec := clusterSpec(lab, inst.ClusterContext, a.Toolbox)
	call := a.call
	if call == nil {
		call = a.Hub.Call
	}
	_, err = call(ctx, inst.UserID, ap.Msg{Type: ap.TProvision, LabID: inst.ID, Data: bundle, Cluster: &spec})
	return err
}

func (a AgentClusterRunner) Provision(context.Context, *Instance, []byte, string) error {
	return errors.New("cluster labs provision through ProvisionLab")
}

func (a AgentClusterRunner) Destroy(ctx context.Context, inst *Instance) error {
	return a.local().Destroy(ctx, inst)
}

func (a AgentClusterRunner) RunScript(ctx context.Context, inst *Instance, s ScriptSpec) (ScriptResult, error) {
	return a.local().RunScript(ctx, inst, s)
}

func (a AgentClusterRunner) OpenPTY(ctx context.Context, inst *Instance, service string, cols, rows int) (PTY, error) {
	return a.local().OpenPTY(ctx, inst, service, cols, rows)
}
