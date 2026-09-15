package state

import (
	"slices"
	"strings"
)

// Build assembles a tree from stored nodes and contributors. The inputs are
// copied, so the caller may keep and reuse them.
//
// Rows can be written by hand, so they are checked against the tree's shape:
// a second workspace, or a node whose parent is missing or of the wrong kind,
// is left out and reported.
func Build(nodes []Node, contributors []Contributor) (*State, []Problem) {
	s := newState(len(nodes))
	var problems []Problem

	// Containers are placed before their contents, so each check sees the
	// outcome of the level above.
	for _, kind := range []Kind{KindWorkspace, KindNotebook, KindNote, KindTask} {
		for i := range nodes {
			if nodes[i].Kind != kind {
				continue
			}
			n := nodes[i]
			if err := s.checkContainment(n.Kind, n.Parent); err != nil {
				problems = append(problems, Problem{ID: n.ID, Reason: err.Error()})
				continue
			}
			if n.Kind == KindWorkspace {
				if s.Workspace != "" {
					problems = append(problems, Problem{ID: n.ID, Reason: "a second workspace"})
					continue
				}
				s.Workspace = n.ID
			}
			n.Tags = slices.Clone(n.Tags)
			n.Links = slices.Clone(n.Links)
			n.Assignees = slices.Clone(n.Assignees)
			s.Nodes[n.ID] = &n
			if n.Parent != "" {
				s.children[n.Parent] = append(s.children[n.Parent], n.ID)
			}
			if !n.Deleted {
				for _, tag := range n.Tags {
					s.tags[tag]++
				}
			}
		}
	}
	for parent, kids := range s.children {
		s.sortChildren(parent, kids)
	}
	for _, c := range contributors {
		s.Contributors[c.ID] = &Contributor{ID: c.ID, Name: c.Name}
	}

	slices.SortFunc(problems, func(a, b Problem) int { return strings.Compare(a.ID, b.ID) })
	return s, problems
}
