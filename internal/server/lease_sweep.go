package server

import (
	"context"
	"log"
	"sort"
	"time"

	"carbon/internal/home"
	"carbon/internal/lease"
	"carbon/internal/store"
)

const leaseSweepInterval = time.Minute

// StartLeaseSweep runs a bounded, cancelable ownership-expiry pass for every physical
// store reachable from this server's launch scope. It never walks arbitrary source paths:
// Carbon roots come exclusively from the home manifest and legacy retains its one root.
func (s *Server) StartLeaseSweep(parent context.Context) {
	s.leaseSweepMu.Lock()
	if s.leaseSweepCancel != nil {
		s.leaseSweepMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.leaseSweepCancel = cancel
	s.leaseSweepMu.Unlock()

	go func() {
		s.sweepLeases(ctx)
		ticker := time.NewTicker(leaseSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.sweepLeases(ctx)
			}
		}
	}()
}

// StopLeaseSweep makes tests and embedding hosts able to release the background worker.
func (s *Server) StopLeaseSweep() {
	s.leaseSweepMu.Lock()
	cancel := s.leaseSweepCancel
	s.leaseSweepCancel = nil
	s.leaseSweepMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Server) sweepLeases(ctx context.Context) {
	for _, root := range s.leaseSweepRoots() {
		if _, err := lease.New(store.New(root), nil, 0).Expire(ctx); err != nil {
			// A transient unreadable store must not kill the scheduler for every other
			// cluster. Endpoint calls still surface their own exact failure to clients.
			log.Printf("carbon lease sweep %s: %v", root, err)
		}
	}
}

func (s *Server) leaseSweepRoots() []string {
	roots := map[string]struct{}{}
	if s.defaultRoot != "" {
		roots[s.defaultRoot] = struct{}{}
	}
	if s.defaultHome != "" {
		if s.defaultCluster != "" {
			if root, err := home.ClusterDataRoot(s.defaultHome, s.defaultCluster); err == nil {
				roots[root] = struct{}{}
			}
		} else if s.defaultProject != "" {
			if root, err := home.ProjectDataRoot(s.defaultHome, s.defaultProject); err == nil {
				roots[root] = struct{}{}
			}
		} else {
			if clusters, err := home.ListClusters(s.defaultHome); err == nil {
				for _, cluster := range clusters {
					if root, err := home.ClusterDataRoot(s.defaultHome, cluster.ID); err == nil {
						roots[root] = struct{}{}
					}
				}
			}
			if projects, err := home.ListProjects(s.defaultHome); err == nil {
				for _, project := range projects {
					if root, err := home.ProjectDataRoot(s.defaultHome, project.ID); err == nil {
						roots[root] = struct{}{}
					}
				}
			}
		}
	}
	out := make([]string, 0, len(roots))
	for root := range roots {
		out = append(out, root)
	}
	sort.Strings(out)
	return out
}
