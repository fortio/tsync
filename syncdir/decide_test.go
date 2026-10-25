package syncdir_test

import (
	"fmt"
	"io/fs"
	"slices"
	"testing"
	"time"

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
	got := syncdir.Plan(local, remote, b, syncdir.PlanOptions{})
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

func TestSummary(t *testing.T) {
	items := []syncdir.Item{
		{Path: "same", Action: syncdir.InSync},
		{Path: "new", Action: syncdir.Pull, Remote: &syncdir.Entry{Size: 1500}},
		{Path: "newdir", Action: syncdir.Pull, Remote: &syncdir.Entry{Dir: true}},
		{Path: "mine", Action: syncdir.PeerPulls},
		{Path: "both", Action: syncdir.Conflict},
		{Path: "gone", Action: syncdir.DeletedThere},
	}
	sum := syncdir.Summarize(items)
	if sum.Pending() != 5 || sum.PullBytes != 1500 || len(sum.Conflicts) != 1 || sum.Conflicts[0] != "both" {
		t.Errorf("unexpected summary %+v", sum)
	}
	if got := sum.Describe("bob"); got != "2 to get (1.5 Kb) · 1 for bob · 1 conflict · 1 to remove" {
		t.Errorf("unexpected description %q", got)
	}
	if !sum.Equal(syncdir.Summarize(items)) || sum.Equal(syncdir.Summarize(items[:1])) {
		t.Errorf("Equal mismatch")
	}
	inSync := syncdir.Summarize(items[:1])
	if inSync.Pending() != 0 || inSync.Describe("bob") != "in sync (1 item)" {
		t.Errorf("unexpected in sync summary %+v %q", inSync, inSync.Describe("bob"))
	}
}

func TestPlanRemoteIgnore(t *testing.T) {
	local := map[string]syncdir.Entry{
		"build/out": {Path: "build/out", Size: 1, Hash: syncdir.Hash{'o'}},
		".env":      {Path: ".env", Size: 1, Hash: syncdir.Hash{'e'}},
		"gone":      {Path: "gone", Size: 1, Hash: syncdir.Hash{'g'}},
	}
	b := syncdir.Base{}
	for _, e := range local {
		b.Record(e)
	}
	remoteIg, err := syncdir.ParseIgnore("build/\n")
	if err != nil {
		t.Fatal(err)
	}
	ours, err := syncdir.ParseIgnore("") // we sync everything, hidden files too.
	if err != nil {
		t.Fatal(err)
	}
	// The peer ignores build/ (and with its default rules, .env): not deletions there.
	got := syncdir.Plan(local, nil, b, syncdir.PlanOptions{Ignore: ours, RemoteIgnore: remoteIg})
	want := map[string]syncdir.Action{".env": syncdir.DeletedThere, "build/out": syncdir.InSync, "gone": syncdir.DeletedThere}
	for _, it := range got {
		if it.Action != want[it.Path] {
			t.Errorf("%s: got %v, want %v", it.Path, it.Action, want[it.Path])
		}
	}
	got = syncdir.Plan(local, nil, b, syncdir.PlanOptions{Ignore: ours}) // default rules there: hidden files aren't synced.
	if got[0].Path != ".env" || got[0].Action != syncdir.InSync {
		t.Errorf("unexpected %+v", got[0])
	}
}

func TestPlanMassDeletion(t *testing.T) {
	local := make(map[string]syncdir.Entry)
	remote := make(map[string]syncdir.Entry)
	b := syncdir.Base{}
	for i := range 30 {
		p := fmt.Sprintf("f%02d", i)
		e := syncdir.Entry{Path: p, Size: 1, Hash: syncdir.Hash{byte(i)}}
		local[p] = e
		b.Record(e)
		if i >= 25 { // the peer only has 5 left: 25 deletions (over 20, and over half of 30).
			remote[p] = e
		}
	}
	sum := syncdir.Summarize(syncdir.Plan(local, remote, b, syncdir.PlanOptions{}))
	if sum.Counts[syncdir.DeletionHeld] != 25 || sum.Counts[syncdir.DeletedThere] != 0 {
		t.Errorf("mass deletion should be held: %+v", sum.Counts)
	}
	if d := sum.Describe("bob"); d != "25 deletions on bob held (too many at once)" {
		t.Errorf("unexpected description %q", d)
	}
	for i := range 10 { // 15 deletions: applied.
		p := fmt.Sprintf("f%02d", i+10)
		remote[p] = local[p]
	}
	if sum = syncdir.Summarize(syncdir.Plan(local, remote, b, syncdir.PlanOptions{})); sum.Counts[syncdir.DeletedThere] != 15 {
		t.Errorf("deletions should be applied: %+v", sum.Counts)
	}
}

// planBoth plans one path from both sides (A has local, B has remote) and returns A's and B's items.
func planBoth(t *testing.T, p string, local, remote, b *syncdir.Entry, copies bool) (syncdir.Item, syncdir.Item) {
	t.Helper()
	side := func(ours, theirs *syncdir.Entry, name string) syncdir.Item {
		l, r, base := map[string]syncdir.Entry{}, map[string]syncdir.Entry{}, syncdir.Base{}
		if ours != nil {
			l[p] = *ours
		}
		if theirs != nil {
			r[p] = *theirs
		}
		if b != nil {
			base[p] = *baseOf(b)
		}
		items := syncdir.Plan(l, r, base, syncdir.PlanOptions{ConflictCopies: copies, Name: name})
		if len(items) != 1 {
			t.Fatalf("expected 1 item, got %+v", items)
		}
		return items[0]
	}
	return side(local, remote, "hostA"), side(remote, local, "hostB")
}

func baseOf(e *syncdir.Entry) *syncdir.BaseEntry {
	return &syncdir.BaseEntry{Dir: e.Dir, Mode: e.Mode, Hash: e.Hash}
}

func TestPlanConflictCopies(t *testing.T) {
	t1, t2 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)
	at := func(c byte, when time.Time) *syncdir.Entry {
		return &syncdir.Entry{Mode: 0o644, Size: 1, MTime: when, Hash: syncdir.Hash{c}}
	}
	orig := at('o', t1.Add(-time.Hour))
	copyA := "notes.sync-conflict-hostA-20260102-030405.txt"
	copyB := "notes.sync-conflict-hostB-20260102-030405.txt"
	for _, tc := range []struct {
		name                string
		local, remote, base *syncdir.Entry
		wantA, wantB        syncdir.Action
		copyA, copyB        string
	}{
		{"both changed, A older", at('a', t1), at('b', t2), orig, syncdir.ConflictCopy, syncdir.PeerConflictCopy, copyA, ""},
		{"both changed, A newer", at('a', t2), at('b', t1), orig, syncdir.PeerConflictCopy, syncdir.ConflictCopy, "", copyB},
		{"same time: by hash", at('b', t1), at('a', t1), orig, syncdir.PeerConflictCopy, syncdir.ConflictCopy, "", copyB},
		{"new on both", at('a', t1), at('b', t2), nil, syncdir.ConflictCopy, syncdir.PeerConflictCopy, copyA, ""},
		{"modified here, deleted there", at('a', t1), nil, orig, syncdir.ConflictCopy, syncdir.PeerConflictCopy, copyA, ""},
		{"file vs directory", at('a', t1), dir(0o755), nil, syncdir.Conflict, syncdir.Conflict, "", ""},
		{"deleted there, unchanged here", orig, nil, orig, syncdir.DeletedThere, syncdir.DeletedHere, "", ""},
	} {
		a, b := planBoth(t, "notes.txt", tc.local, tc.remote, tc.base, true)
		if a.Action != tc.wantA || b.Action != tc.wantB || a.Copy != tc.copyA || b.Copy != tc.copyB {
			t.Errorf("%s: got A %v %q, B %v %q; want A %v %q, B %v %q", tc.name, a.Action, a.Copy, b.Action, b.Copy,
				tc.wantA, tc.copyA, tc.wantB, tc.copyB)
		}
	}
	// Without conflict copies: left alone, modified wins over deleted.
	a, b := planBoth(t, "notes.txt", at('a', t1), at('b', t2), orig, false)
	if a.Action != syncdir.Conflict || b.Action != syncdir.Conflict {
		t.Errorf("expected conflicts, got %v %v", a.Action, b.Action)
	}
	a, b = planBoth(t, "notes.txt", at('a', t1), nil, orig, false)
	if a.Action != syncdir.PeerPulls || b.Action != syncdir.Pull {
		t.Errorf("modified should win, got %v %v", a.Action, b.Action)
	}
}

func TestConflictCopyName(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600))
	for in, want := range map[string]string{
		"dir/notes.txt": "dir/notes.sync-conflict-my_host_-20260102-020405.txt",
		".bashrc":       ".bashrc.sync-conflict-my_host_-20260102-020405",
		"Makefile":      "Makefile.sync-conflict-my_host_-20260102-020405",
		"a.tar.gz":      "a.tar.sync-conflict-my_host_-20260102-020405.gz",
	} {
		if got := syncdir.ConflictCopyName(in, "my host!", when); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
		if !syncdir.IsConflictCopy(want) || syncdir.IsConflictCopy(in) {
			t.Errorf("IsConflictCopy mismatch for %q", in)
		}
	}
}

func TestPlanMoves(t *testing.T) {
	local, remote, b := map[string]syncdir.Entry{}, map[string]syncdir.Entry{}, syncdir.Base{}
	add := func(m map[string]syncdir.Entry, p string, e syncdir.Entry) {
		e.Path = p
		m[p] = e
	}
	add(local, "old", syncdir.Entry{Dir: true, Mode: 0o755})
	for i := range 25 { // more than massDeletion: moves aren't deletions.
		e := syncdir.Entry{Mode: 0o644, Size: int64(i), Hash: syncdir.Hash{byte(i)}}
		add(local, fmt.Sprintf("old/f%02d", i), e)
		add(remote, fmt.Sprintf("new/f%02d", i), e)
	}
	add(local, "gone", syncdir.Entry{Mode: 0o644, Size: 1, Hash: syncdir.Hash{'g'}})
	for _, e := range local {
		b.Record(e)
	}
	add(remote, "new", syncdir.Entry{Dir: true, Mode: 0o755})
	sum := syncdir.Summarize(syncdir.Plan(local, remote, b, syncdir.PlanOptions{}))
	c := sum.Counts
	if c[syncdir.Move] != 25 || c[syncdir.MovedAway] != 25 || c[syncdir.DeletedThere] != 2 || c[syncdir.Pull] != 1 ||
		c[syncdir.DeletionHeld] != 0 {
		t.Errorf("unexpected counts %+v", c)
	}
	for _, it := range syncdir.Plan(local, remote, b, syncdir.PlanOptions{}) {
		if it.Action == syncdir.Move && (it.From == nil || it.From.Path != "old/"+it.Path[len("new/"):]) {
			t.Errorf("unexpected move %+v from %+v", it, it.From)
		}
	}
	if d := sum.Describe("bob"); d != "1 to get (0 b) · 25 to move · 2 to remove" {
		t.Errorf("unexpected description %q", d)
	}
}

func TestPlanCantStoreThere(t *testing.T) {
	local := map[string]syncdir.Entry{"a:b": {Path: "a:b", Size: 1}, "ok": {Path: "ok", Size: 1}}
	items := syncdir.Plan(local, nil, syncdir.Base{}, syncdir.PlanOptions{RemoteCantStore: map[string]bool{"a:b": true}})
	if items[0].Path != "a:b" || items[0].Action != syncdir.CantStoreThere || items[1].Action != syncdir.PeerPulls {
		t.Errorf("unexpected %+v", items)
	}
	if d := syncdir.Summarize(items).Describe("omen"); d != "1 for omen · 1 can't be stored on omen" {
		t.Errorf("unexpected description %q", d)
	}
}
