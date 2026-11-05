package cluster

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/settings"
	"github.com/hashicorp/raft"
)

const (
	readyLag        = 1000
	healthCallLimit = 2 * time.Second
)

type MemberStatus struct {
	NodeID        string    `json:"node_id"`
	Address       string    `json:"address"`
	DiameterLabel string    `json:"diameter_label"`
	DisplayName   string    `json:"display_name"`
	Version       string    `json:"version"`
	JoinedAt      time.Time `json:"joined_at"`
	DrainState    string    `json:"drain_state"`
	Suffrage      string    `json:"suffrage"`
	Leader        bool      `json:"leader"`
	Healthy       *bool     `json:"healthy,omitempty"`
	LastContact   string    `json:"last_contact,omitempty"`
	AppliedIndex  uint64    `json:"applied_index,omitempty"`
	Diverged      bool      `json:"diverged,omitempty"`
}

type Status struct {
	NodeID           string         `json:"node_id"`
	ClusterID        string         `json:"cluster_id"`
	Clustered        bool           `json:"clustered"`
	LeaderID         string         `json:"leader_id,omitempty"`
	Term             uint64         `json:"term"`
	AppliedIndex     uint64         `json:"applied_index"`
	CommitIndex      uint64         `json:"commit_index"`
	Schema           int            `json:"schema"`
	Healthy          *bool          `json:"healthy,omitempty"`
	FailureTolerance *int           `json:"failure_tolerance,omitempty"`
	Members          []MemberStatus `json:"members"`
}

func (n *Node) Status(ctx context.Context) (Status, error) {
	r, _ := n.current()

	_, leaderID := r.LeaderWithID()

	st := Status{
		NodeID: n.id, Clustered: n.cfg.clustered(), LeaderID: string(leaderID), Term: r.CurrentTerm(),
		AppliedIndex: n.db.AppliedIndex(), CommitIndex: r.CommitIndex(), Schema: n.db.Schema(),
	}

	secrets, err := n.db.ReadSecrets(ctx)
	if err == nil {
		st.ClusterID = secrets.ClusterID
	}

	members, err := n.db.Members(ctx)
	if err != nil {
		return Status{}, err
	}

	future := r.GetConfiguration()
	if err := future.Error(); err != nil && !errors.Is(err, raft.ErrRaftShutdown) {
		return Status{}, err
	}

	servers := future.Configuration().Servers

	h, known := n.leaderHealth(ctx)
	if known {
		st.Healthy, st.FailureTolerance = &h.Healthy, &h.FailureTolerance
	}

	for _, m := range members {
		ms := MemberStatus{
			NodeID: m.NodeID, Address: m.Address, DiameterLabel: settings.NodeLabel(m.NodeID), DisplayName: m.DisplayName,
			Version:  m.Version,
			JoinedAt: m.JoinedAt, DrainState: string(m.DrainState), Suffrage: "none", Leader: m.NodeID == string(leaderID),
		}

		if i := slices.IndexFunc(servers, func(s raft.Server) bool { return string(s.ID) == m.NodeID }); i >= 0 {
			ms.Suffrage = "voter"
			if servers[i].Suffrage != raft.Voter {
				ms.Suffrage = "nonvoter"
			}
		}

		if i := slices.IndexFunc(h.Servers, func(s ServerHealth) bool { return s.NodeID == m.NodeID }); i >= 0 {
			s := h.Servers[i]
			ms.Healthy, ms.LastContact, ms.AppliedIndex, ms.Diverged = &s.Healthy, s.LastContact, s.AppliedIndex, s.Diverged
		}

		st.Members = append(st.Members, ms)
	}

	return st, nil
}

func (n *Node) leaderHealth(ctx context.Context) (Health, bool) {
	if leading, _ := n.Leader(); leading {
		h := n.health.snapshot()
		return h, h.Servers != nil
	}

	if n.peers == nil {
		return Health{}, false
	}

	ctx, cancel := context.WithTimeout(ctx, healthCallLimit)
	defer cancel()

	addr, id, err := n.peers.leader()
	if err != nil {
		return Health{}, false
	}

	resp, err := n.peers.do(ctx, addr, id, "GET", "/cluster/v1/health", nil)
	if err != nil {
		return Health{}, false
	}

	defer func() { _ = resp.Body.Close() }()

	var h Health
	if err := decodeReply(resp, &h); err != nil {
		return Health{}, false
	}

	return h, h.Servers != nil
}

func (n *Node) Ready() error {
	select {
	case <-n.removed:
		return ErrRemoved
	default:
	}

	if m, err := n.db.Member(context.Background(), n.id); err == nil && m.DrainState != db.DrainActive {
		return fmt.Errorf("the node is %s", m.DrainState)
	}

	r, _ := n.current()

	if lag := r.CommitIndex() - min(r.CommitIndex(), n.db.AppliedIndex()); lag > readyLag {
		return errors.New("the database is behind the cluster")
	}

	if r.State() == raft.Leader {
		return nil
	}

	if last := r.LastContact(); last.IsZero() || n.cfg.Now().Sub(last) > 2*n.electionTimeout() {
		return errors.New("no contact with the cluster leader")
	}

	return nil
}

func (n *Node) HandOver() error {
	if leading, _ := n.Leader(); !leading {
		return nil
	}

	r, _ := n.current()

	future := r.GetConfiguration()
	if err := future.Error(); err != nil {
		return err
	}

	for _, s := range future.Configuration().Servers {
		if s.Suffrage == raft.Voter && string(s.ID) != n.id && !n.isDraining(s.ID) {
			return r.LeadershipTransferToServer(s.ID, s.Address).Error()
		}
	}

	return nil
}

func (n *Node) RaftLeader() bool {
	r, _ := n.current()

	return r.State() == raft.Leader
}

var ErrLastMember = errors.New("the only member of the cluster cannot be drained")

func (n *Node) SetDrainState(ctx context.Context, nodeID string, state db.DrainState) error {
	if state == db.DrainDraining {
		members, err := n.db.Members(ctx)
		if err != nil {
			return err
		}

		if len(members) < 2 {
			return ErrLastMember
		}
	}

	return n.db.SetDrainState(ctx, nodeID, state)
}

func (n *Node) SetDisplayName(ctx context.Context, nodeID, name string) error {
	return n.db.SetDisplayName(ctx, nodeID, name)
}

func (n *Node) DisplayName(ctx context.Context) string {
	m, err := n.db.Member(ctx, n.id)
	if err != nil {
		return ""
	}

	return m.DisplayName
}
