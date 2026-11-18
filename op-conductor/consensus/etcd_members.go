package consensus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"go.etcd.io/etcd/client/pkg/v3/types"
	pb "go.etcd.io/raft/v3/raftpb"
)

type memberResult struct {
	id  uuid.UUID
	err error
}

type memberChange struct {
	Request uuid.UUID
	Member  ServerInfo
	Version uint64
}

func peerURLs(addr string) (types.URLs, error) {
	urls, err := types.NewURLs([]string{"http://" + addr})
	if err != nil {
		return nil, err
	}
	u := urls[0]
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil || port == 0 || u.Host != addr || u.User != nil || u.Hostname() == "" || net.ParseIP(u.Hostname()).IsUnspecified() {
		return nil, fmt.Errorf("invalid advertised peer address %q", addr)
	}
	return urls, nil
}

func (c *etcdConsensus) ClusterMembership() (*ClusterMembership, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m := &ClusterMembership{Version: c.version}
	for _, info := range c.members {
		m.Servers = append(m.Servers, info)
	}
	sort.Slice(m.Servers, func(i, j int) bool { return m.Servers[i].ID < m.Servers[j].ID })
	return m, nil
}

func (c *etcdConsensus) change(t pb.ConfChangeType, info ServerInfo, v uint64) error {
	if !c.changing.CompareAndSwap(false, true) {
		return errors.New("raft membership change already in progress")
	}
	defer c.changing.Store(false)
	if info.ID == "" {
		return errors.New("empty raft server ID")
	}
	return c.submit(func(ctx context.Context, id uuid.UUID) error {
		if info.Addr != "" {
			urls, err := peerURLs(info.Addr)
			if err != nil {
				return err
			}
			req, err := http.NewRequestWithContext(ctx, "GET", urls[0].String()+"/conductor/member", nil)
			if err != nil {
				return err
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer res.Body.Close()
			remote, err := io.ReadAll(io.LimitReader(res.Body, int64(len(info.ID)+1)))
			if err != nil {
				return err
			}
			if res.StatusCode != http.StatusOK || res.Header.Get("X-Conductor-Cluster") != c.transport.ClusterID.String() || string(remote) != info.ID {
				return errors.New("not the requested etcd raft cluster member")
			}
		}
		b, err := json.Marshal(memberChange{id, info, v})
		if err != nil {
			return err
		}
		return c.node.ProposeConfChange(ctx, pb.ConfChange{NodeID: raftID(info.ID), Type: t, Context: b})
	})
}

func (c *etcdConsensus) AddVoter(id, addr string, v uint64) error {
	if addr == "" {
		return errors.New("missing voter address")
	}
	return c.change(pb.ConfChangeAddNode, ServerInfo{id, addr, Voter}, v)
}

func (c *etcdConsensus) AddNonVoter(id, addr string, v uint64) error {
	if addr == "" {
		return errors.New("missing learner address")
	}
	return c.change(pb.ConfChangeAddLearnerNode, ServerInfo{id, addr, Nonvoter}, v)
}

func (c *etcdConsensus) DemoteVoter(id string, v uint64) error {
	return c.change(pb.ConfChangeAddLearnerNode, ServerInfo{ID: id, Suffrage: Nonvoter}, v)
}

func (c *etcdConsensus) RemoveServer(id string, v uint64) error {
	return c.change(pb.ConfChangeRemoveNode, ServerInfo{ID: id}, v)
}

// Validate against applied state on every replica, not the configuration seen
// by the RPC handler. Another committed change can win the version race.
func (c *etcdConsensus) validateChange(m *memberChange, cc *pb.ConfChange) error {
	if m.Version != 0 && m.Version != c.version {
		return errors.New("raft membership version mismatch")
	}
	old, exists := c.members[cc.NodeID]
	if c.removed[cc.NodeID] || exists && old.ID != m.Member.ID {
		return errors.New("member ID is not available")
	}
	// AddNonVoter updates an existing voter's address without demoting it.
	// DemoteVoter is distinguishable because it does not supply an address.
	if exists && cc.Type == pb.ConfChangeAddLearnerNode && m.Member.Addr != "" && old.Suffrage == Voter {
		m.Member.Suffrage = Voter
		cc.Type = pb.ConfChangeAddNode
	}
	if cc.Type == pb.ConfChangeRemoveNode || m.Member.Addr == "" {
		if !exists {
			return errors.New("unknown member")
		}
		m.Member.Addr = old.Addr
	}
	if cc.Type != pb.ConfChangeAddNode && slices.Contains(c.conf.Voters, cc.NodeID) && len(c.conf.Voters) == 1 {
		return errors.New("cannot remove or demote last voter")
	}
	if _, err := peerURLs(m.Member.Addr); err != nil {
		return err
	}
	for k, info := range c.members {
		if k != cc.NodeID && info.Addr == m.Member.Addr {
			return errors.New("address already belongs to another member")
		}
	}
	return nil
}

func (c *etcdConsensus) applyMember(e pb.Entry) error {
	var cc pb.ConfChange
	if err := cc.Unmarshal(e.Data); err != nil {
		return err
	}
	var m memberChange
	if err := json.Unmarshal(cc.Context, &m); err != nil {
		return err
	}
	if raftID(m.Member.ID) != cc.NodeID {
		return errors.New("invalid member ID in raft log")
	}
	if err := c.validateChange(&m, &cc); err != nil {
		c.memberResults = append(c.memberResults, memberResult{m.Request, err})
		return nil
	}
	c.conf = *c.node.ApplyConfChange(cc)
	id := types.ID(cc.NodeID)
	old, known := c.members[cc.NodeID]
	c.mu.Lock()
	if cc.Type == pb.ConfChangeRemoveNode {
		delete(c.members, cc.NodeID)
		c.removed[cc.NodeID] = true
	} else {
		c.members[cc.NodeID] = m.Member
	}
	c.version = e.Index
	c.mu.Unlock()
	if cc.NodeID != c.id {
		urls := []string{"http://" + m.Member.Addr}
		switch {
		case cc.Type == pb.ConfChangeRemoveNode:
			c.transport.RemovePeer(id)
		case !known:
			c.transport.AddPeer(id, urls)
		case old.Addr != m.Member.Addr:
			c.transport.UpdatePeer(id, urls)
		}
	}
	c.memberResults = append(c.memberResults, memberResult{id: m.Request})
	return nil
}

func (c *etcdConsensus) TransferLeader() error {
	if !c.Leader() {
		return nil
	}
	// Prefer a reachable voter with the most up-to-date log.
	status := c.node.Status()
	m, _ := c.ClusterMembership()
	var target ServerInfo
	var matched uint64
	for _, peer := range m.Servers {
		pr := status.Progress[raftID(peer.ID)]
		if peer.Suffrage == Voter && peer.ID != c.ServerID() && pr.RecentActive && (target.ID == "" || pr.Match > matched) {
			target = peer
			matched = pr.Match
		}
	}
	if target.ID != "" {
		return c.TransferLeaderTo(target.ID, target.Addr)
	}
	return errors.New("no other voter available")
}

func (c *etcdConsensus) TransferLeaderTo(id, addr string) error {
	dest := raftID(id)
	c.mu.RLock()
	info, ok := c.members[dest]
	c.mu.RUnlock()
	if !c.Leader() || !ok || dest == c.id || info.Addr != addr || info.Suffrage != Voter {
		return errors.New("invalid leadership transfer")
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	c.node.TransferLeadership(ctx, c.id, dest)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if c.leader.Load() == dest {
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return errors.New("raft stopped")
		}
	}
}
