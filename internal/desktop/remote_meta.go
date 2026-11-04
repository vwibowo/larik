package desktop

import (
	"errors"
	"slices"

	"larik/internal/session"
)

// remoteMetaReply is sidebar metadata collected by the host, where the
// processes and repositories actually live. Paths are host-local on wire.
type remoteMetaReply struct {
	Git   map[string]gitMeta `json:"git,omitempty"`
	Ports map[string][]int   `json:"ports,omitempty"`
}

func (h *host) remoteMeta() (remoteMetaReply, error) {
	projects, err := session.Index(h.dataDir, session.IndexOptions{Scratch: h.scratch})
	if err != nil {
		return remoteMetaReply{}, err
	}
	meta := remoteMetaReply{Git: map[string]gitMeta{}}
	pids := map[string]int{}
	for _, p := range projects {
		if p.Missing {
			continue
		}
		if m, ok := readGit(p.Root); ok {
			meta.Git[p.Root] = m
		}
		for _, s := range p.Sessions {
			if s.Cwd != p.Root {
				if _, seen := meta.Git[s.Cwd]; !seen {
					if m, ok := readGit(s.Cwd); ok {
						meta.Git[s.Cwd] = m
					}
				}
			}
			if s.PID > 0 && s.Running {
				pids[s.Path] = s.PID
			}
		}
	}
	meta.Ports = listening(pids)
	return meta, nil
}

type remoteMetaSource struct {
	name string
	dial dialer
}

func readRemoteMeta(src remoteMetaSource) (remoteMetaReply, error) {
	conn, _, rep, err := askHostVia(src.dial, hostRequest{Op: "meta"})
	if err != nil {
		return remoteMetaReply{}, err
	}
	conn.Close()
	if rep.Meta == nil {
		return remoteMetaReply{}, errors.New("remote host returned no metadata")
	}
	return *rep.Meta, nil
}

func (a *App) remoteMetaSources() []remoteMetaSource {
	var sources []remoteMetaSource
	for _, srv := range a.servers {
		if slices.Contains(srv.features, "remote-meta") {
			if dial := a.dialOf(srv); dial != nil {
				sources = append(sources, remoteMetaSource{name: srv.name, dial: dial})
			}
		}
	}
	return sources
}
