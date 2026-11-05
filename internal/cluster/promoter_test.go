package cluster

import (
	"slices"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	autopilot "github.com/hashicorp/raft-autopilot"
)

func TestCappedPromoter(t *testing.T) {
	now := time.Now()
	stableSince := now.Add(-time.Minute)

	server := func(state autopilot.RaftState, healthy bool) *autopilot.ServerState {
		return &autopilot.ServerState{State: state, Health: autopilot.ServerHealth{Healthy: healthy, StableSince: stableSince}}
	}

	tests := []struct {
		name              string
		servers           map[raft.ServerID]*autopilot.ServerState
		promoted, demoted []raft.ServerID
	}{
		{
			name: "a second node waits as a standby",
			servers: map[raft.ServerID]*autopilot.ServerState{
				"a": server(autopilot.RaftLeader, true), "b": server(autopilot.RaftNonVoter, true),
			},
		},
		{
			name: "a third node makes three voters",
			servers: map[raft.ServerID]*autopilot.ServerState{
				"a": server(autopilot.RaftLeader, true), "b": server(autopilot.RaftNonVoter, true),
				"c": server(autopilot.RaftNonVoter, true),
			},
			promoted: []raft.ServerID{"b", "c"},
		},
		{
			name: "a fourth node stays a standby",
			servers: map[raft.ServerID]*autopilot.ServerState{
				"a": server(autopilot.RaftLeader, true), "b": server(autopilot.RaftVoter, true),
				"c": server(autopilot.RaftVoter, true), "d": server(autopilot.RaftNonVoter, true),
			},
		},
		{
			name: "a failed voter is swapped for the standby",
			servers: map[raft.ServerID]*autopilot.ServerState{
				"a": server(autopilot.RaftLeader, true), "b": server(autopilot.RaftVoter, false),
				"c": server(autopilot.RaftVoter, true), "d": server(autopilot.RaftNonVoter, true),
			},
			promoted: []raft.ServerID{"d"},
			demoted:  []raft.ServerID{"b"},
		},
		{
			name: "two voters become one",
			servers: map[raft.ServerID]*autopilot.ServerState{
				"a": server(autopilot.RaftLeader, true), "b": server(autopilot.RaftVoter, true),
			},
			demoted: []raft.ServerID{"b"},
		},
		{
			name: "two voters wait for a joining third",
			servers: map[raft.ServerID]*autopilot.ServerState{
				"a": server(autopilot.RaftLeader, true), "b": server(autopilot.RaftVoter, true),
				"c": {State: autopilot.RaftNonVoter, Health: autopilot.ServerHealth{Healthy: true, StableSince: now}},
			},
		},
		{
			name: "at most five voters",
			servers: map[raft.ServerID]*autopilot.ServerState{
				"a": server(autopilot.RaftLeader, true), "b": server(autopilot.RaftVoter, true),
				"c": server(autopilot.RaftVoter, true), "d": server(autopilot.RaftNonVoter, true),
				"e": server(autopilot.RaftNonVoter, true), "f": server(autopilot.RaftNonVoter, true),
				"g": server(autopilot.RaftNonVoter, true),
			},
			promoted: []raft.ServerID{"d", "e"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changes := cappedPromoter{}.CalculatePromotionsAndDemotions(&autopilot.Config{ServerStabilizationTime: stabilization},
				&autopilot.State{Servers: tt.servers})

			if !slices.Equal(changes.Promotions, tt.promoted) || !slices.Equal(changes.Demotions, tt.demoted) {
				t.Fatalf("promoted %v, demoted %v; want %v, %v", changes.Promotions, changes.Demotions, tt.promoted,
					tt.demoted)
			}
		})
	}
}
