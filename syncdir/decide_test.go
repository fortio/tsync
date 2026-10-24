package syncdir_test

import (
	"io/fs"
	"slices"
	"testing"

	"fortio.org/tsync/syncdir"
)

func file(content byte, mode fs.FileMode) *syncdir.Entry {
	return &syncdir.Entry{Mode: mode, Size: 1, Hash: syncdir.Hash{content}}
}

func dir(mode fs.FileMode) *syncdir.Entry {
	return &syncdir.Entry{Dir: true, Mode: mode}
}

func base(e *syncdir.Entry) *syncdir.BaseEntry {
	if e == nil {
		return nil
	}
	return &syncdir.BaseEntry{Dir: e.Dir, Mode: e.Mode, Hash: e.Hash}
}

// mirror returns the possible actions on the other side for an action on ours.
func mirror(a syncdir.Action) []syncdir.Action {
	switch a {
	case syncdir.Pull, syncdir.MetadataOnly:
		return []syncdir.Action{syncdir.PeerPulls}
	case syncdir.PeerPulls:
		return []syncdir.Action{syncdir.Pull, syncdir.MetadataOnly}
	case syncdir.DeletedHere:
		return []syncdir.Action{syncdir.DeletedThere}
	case syncdir.DeletedThere:
		return []syncdir.Action{syncdir.DeletedHere}
	default:
		return []syncdir.Action{a}
	}
}

func TestDecide(t *testing.T) {
	a, b, c := file('a', 0o644), file('b', 0o644), file('c', 0o644)
	for _, tc := range []struct {
		name          string
		local, remote *syncdir.Entry
		base          *syncdir.Entry
		want          syncdir.Action
	}{
		{"absent everywhere", nil, nil, nil, syncdir.InSync},
		{"same, never synced", a, a, nil, syncdir.InSync},
		{"same, synced", a, a, a, syncdir.InSync},
		{"both changed the same way", b, b, a, syncdir.InSync},
		{"changed there", a, b, a, syncdir.Pull},
		{"changed here", b, a, a, syncdir.PeerPulls},
		{"changed on both sides", b, c, a, syncdir.Conflict},
		{"new there", nil, a, nil, syncdir.Pull},
		{"new here", a, nil, nil, syncdir.PeerPulls},
		{"new on both sides, different", a, b, nil, syncdir.Conflict},
		{"deleted here, unchanged there", nil, a, a, syncdir.DeletedHere},
		{"deleted there, unchanged here", a, nil, a, syncdir.DeletedThere},
		{"deleted here, modified there", nil, b, a, syncdir.Pull},
		{"modified here, deleted there", b, nil, a, syncdir.PeerPulls},
		{"deleted on both sides", nil, nil, a, syncdir.InSync},
		{"file vs directory", a, dir(0o755), nil, syncdir.Conflict},
		{"file vs directory, synced file", a, dir(0o755), a, syncdir.Conflict},
		{"directory on both", dir(0o755), dir(0o755), nil, syncdir.InSync},
		{"new directory there", nil, dir(0o755), nil, syncdir.Pull},
		{"directory deleted there", dir(0o755), nil, dir(0o755), syncdir.DeletedThere},
		{"deleted here, directory replaced by a file there", nil, a, dir(0o755), syncdir.Pull},
		{"mode changed there", a, file('a', 0o755), a, syncdir.MetadataOnly},
		{"mode changed here", file('a', 0o755), a, a, syncdir.PeerPulls},
		{"mode changed on both sides", file('a', 0o700), file('a', 0o755), a, syncdir.InSync},
		{"different modes, never synced", a, file('a', 0o755), nil, syncdir.InSync},
		{"different modes, content changed since", file('b', 0o600), file('b', 0o755), a, syncdir.InSync},
		{"directory mode changed there", dir(0o755), dir(0o700), dir(0o755), syncdir.MetadataOnly},
		{"content and mode changed there", a, file('b', 0o755), a, syncdir.Pull},
	} {
		got := syncdir.Decide(tc.local, tc.remote, base(tc.base))
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
		// The peer decides the mirror action on its side.
		other := syncdir.Decide(tc.remote, tc.local, base(tc.base))
		if !slices.Contains(mirror(got), other) {
			t.Errorf("%s: the other side decides %v, expected one of %v", tc.name, other, mirror(got))
		}
	}
}

func TestPlan(t *testing.T) {
	entries := func(es map[string]*syncdir.Entry) map[string]syncdir.Entry {
		m := make(map[string]syncdir.Entry, len(es))
		for p, e := range es {
			e.Path = p
			m[p] = *e
		}
		return m
	}
	local := entries(map[string]*syncdir.Entry{
		"same.txt": file('s', 0o644), "mine.txt": file('m', 0o644), "sub": dir(0o755), "sub/x": file('x', 0o644),
		"Read.me": file('r', 0o644), "Both": file('1', 0o644), "both": file('2', 0o644),
	})
	remote := entries(map[string]*syncdir.Entry{
		"same.txt": file('s', 0o644), "theirs.txt": file('t', 0o644), "sub": dir(0o755), "sub/x": file('y', 0o644),
		"read.me": file('r', 0o644), "Both": file('1', 0o644), "both": file('2', 0o644),
		".hidden": file('h', 0o644), // ignored by our (default) rules.
	})
	b := syncdir.Base{}
	b.Record(local["sub/x"])
	got := syncdir.Plan(local, remote, b, nil)
	want := []struct {
		path   string
		action syncdir.Action
	}{
		{"Both", syncdir.InSync}, // both case variants on both sides: case sensitive file systems.
		{"Read.me", syncdir.Conflict},
		{"both", syncdir.InSync},
		{"mine.txt", syncdir.PeerPulls},
		{"read.me", syncdir.Conflict},
		{"same.txt", syncdir.InSync},
		{"sub", syncdir.InSync},
		{"sub/x", syncdir.Pull},
		{"theirs.txt", syncdir.Pull},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Path != w.path || got[i].Action != w.action {
			t.Errorf("item %d: got %s %v, want %s %v", i, got[i].Path, got[i].Action, w.path, w.action)
		}
	}
	if got[7].Local == nil || got[7].Remote == nil || got[8].Local != nil || got[8].Remote == nil {
		t.Errorf("unexpected entries in %+v / %+v", got[7], got[8])
	}
}
