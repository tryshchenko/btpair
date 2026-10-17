package engine

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/tryshchenko/btpair/internal/meta"
	"github.com/tryshchenko/btpair/internal/volume"
)

// Pair groups the connected drives that share a pair ID. Either side may be
// nil when that drive is not plugged in.
type Pair struct {
	ID       string
	Label    string
	Main     *volume.Volume
	Replica  *volume.Volume
	Problems []string
}

func (p *Pair) Ready() bool { return p.Main != nil && p.Replica != nil && len(p.Problems) == 0 }

// State returns the freshest last-run state found on either drive.
func (p *Pair) State() *meta.State {
	var best *meta.State
	for _, v := range []*volume.Volume{p.Main, p.Replica} {
		if v == nil || v.State == nil {
			continue
		}
		if best == nil || v.State.StartedAt.After(best.StartedAt) {
			best = v.State
		}
	}
	return best
}

// Group sorts detected drives into pairs and unmanaged drives.
func Group(vols []*volume.Volume) (pairs []*Pair, unpaired []*volume.Volume) {
	byID := map[string]*Pair{}
	for _, v := range vols {
		if !v.Managed() {
			unpaired = append(unpaired, v)
			continue
		}
		id := v.Identity
		p := byID[id.PairID]
		if p == nil {
			p = &Pair{ID: id.PairID, Label: id.Label}
			byID[id.PairID] = p
			pairs = append(pairs, p)
		}
		if id.VolumeUUID != v.UUID {
			p.Problems = append(p.Problems, fmt.Sprintf("%s: metadata belongs to volume %s, but this volume is %s (cloned drive?)", v.Name, id.VolumeUUID, v.UUID))
		}
		switch id.Role {
		case meta.RoleMain:
			if p.Main != nil {
				p.Problems = append(p.Problems, fmt.Sprintf("two main drives connected: %s and %s", p.Main.Name, v.Name))
				continue
			}
			p.Main = v
		case meta.RoleReplica:
			if p.Replica != nil {
				p.Problems = append(p.Problems, fmt.Sprintf("two replicas connected: %s and %s", p.Replica.Name, v.Name))
				continue
			}
			p.Replica = v
		default:
			p.Problems = append(p.Problems, fmt.Sprintf("%s: unknown role %q", v.Name, id.Role))
		}
	}
	for _, p := range pairs {
		if p.Main != nil && p.Replica != nil {
			if p.Main.Identity.PeerUUID != p.Replica.UUID || p.Replica.Identity.PeerUUID != p.Main.UUID {
				p.Problems = append(p.Problems, "main and replica do not list each other as peers")
			}
		}
	}
	// Ready pairs first, then by name.
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Ready() != pairs[j].Ready() {
			return pairs[i].Ready()
		}
		return pairs[i].ID < pairs[j].ID
	})
	return pairs, unpaired
}

// InitPair writes identities that bind main and replica together.
func InitPair(mainVol, replica *volume.Volume, pairID, label string) error {
	if mainVol == nil || replica == nil {
		return errors.New("need both a main drive and a replica")
	}
	if mainVol.UUID == replica.UUID {
		return errors.New("main and replica must be different drives")
	}
	for _, v := range []*volume.Volume{mainVol, replica} {
		if v.Managed() {
			return fmt.Errorf("%s already belongs to pair %q", v.Name, v.Identity.PairID)
		}
		if v.ReadOnly {
			return fmt.Errorf("%s is mounted read-only", v.Name)
		}
	}
	if !validID(pairID) {
		return errors.New("pair name may only contain letters, digits, '-' and '_'")
	}
	now := time.Now()
	if err := meta.SaveIdentity(mainVol.Path, meta.Identity{
		PairID: pairID, Role: meta.RoleMain, VolumeUUID: mainVol.UUID, PeerUUID: replica.UUID, Label: label, CreatedAt: now,
	}); err != nil {
		return err
	}
	if err := meta.SaveIdentity(replica.Path, meta.Identity{
		PairID: pairID, Role: meta.RoleReplica, VolumeUUID: replica.UUID, PeerUUID: mainVol.UUID, Label: label, CreatedAt: now,
	}); err != nil {
		return err
	}
	mainVol.LoadMeta()
	replica.LoadMeta()
	return nil
}

func validID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
