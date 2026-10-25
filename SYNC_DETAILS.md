# How directory sync works

This is for the curious and for developers: what `-sync label=path` does, and why. The short version is in the
[README](README.md).

## The idea: compare snapshots, never replay operations

tsync doesn't record what you do (no file system event log, no inode tracking). Each machine only knows:

- its **index**: the current list of paths in the synced directory, with for each one: file or directory, mode,
  size, modification time and, for files, the sha256 of the content;
- the **peer's index**, received over the encrypted link;
- the **base**: what both sides had at the last sync, per path (content hash and mode). It is kept per synced
  directory *and per peer* in `~/.tsync/sync/<label>/<peer public key>.base` (see `-tsync-dir`).

Comparing the three (ours, theirs, base) tells, for each path, who changed what since the last sync, without
looking at clocks. It's the same "3-way merge" idea as in version control: with only 2 sides you can't tell
"I added it" from "they deleted it".

A consequence: there are no "operations", only states. A directory has no identity other than its path: renaming
`subdir1` to `subdir2` is seen as `subdir1` (and its files) gone and `subdir2` (and its files) new. Moves are
recognized for *files*, by content (see [Moves](#moves-and-renames)), which covers directory renames.

## Scanning

Each sync directory is scanned every `-sync-rescan` (10s by default), and right after each batch of changes
synced from a peer (so the other peers learn about them quickly). Unless watched:

- With `-sync-watch` (default on Linux and Windows) the directories are watched for changes (file system
  notifications: inotify on Linux, ReadDirectoryChangesW on Windows, kqueue on macOS, which needs a file
  descriptor per file, so it's off by default there). The point is not to walk hundreds of directories every
  10s: only the directories notified are read again (their direct content; a new sub directory is scanned
  whole, a removed one removed with all it had), once there was no notification for 2s (still being
  written), or at most 10s after the first one. A file written to is hashed again even if its size and time
  didn't change. A change to `.tsyncignore`, or lost notifications (overflow), means a full scan.
- Notifications are only hints: the whole directory is still scanned every 15 minutes (or `-sync-rescan` if
  longer). If that finds changes in directories that weren't notified (eg a network or shared folder, where
  notifications don't see changes made by other machines), the directory goes back to scanning every
  `-sync-rescan` (with a warning). Ditto if it can't watch all the directories (eg the inotify watches limit
  on Linux, `fs.inotify.max_user_watches`) or the notifications stop.
- What sync writes is notified like any other change: after a batch of changes synced from a peer, only the
  directories notified are scanned again (right away, not waiting for the 2s).

- Paths are keys: slash separated, relative to the synced directory, Unicode NFC normalized (macOS can give
  decomposed names for accented letters, Linux and Windows usually composed ones; normalized, they are the same
  path everywhere).
- Only regular files and directories: symlinks, devices... are skipped (and counted).
- A file is only hashed again if its size or time changed since the previous scan, or if it was modified less
  than 2s before that scan (it may have been rewritten with the same size and time just after we read it).
- Modes: the permission bits, minus group and other write (a peer's umask, or Windows reporting every file as
  writable by all, shouldn't change yours). On Windows, which only has a read only attribute, the other bits
  come from the previous scan, so an executable from a Mac doesn't lose its `x` by going through Windows.
- Directory times are ignored (they change whenever something is added or removed in them).

### Ignored paths

Without a `.tsyncignore` at the root of the synced directory, hidden files and directories (name starting with
a dot) are ignored, except that `.tsyncignore` itself. With one, only what it lists is ignored, with the usual
`.gitignore` syntax: `#` comments, `*`, `?`, `[...]`, `**`, a trailing `/` for directories only, a pattern with
a `/` anchored at the root, `!` to re-include (the last matching line decides, and like git nothing inside an
ignored directory can be re-included). Not supported: `.tsyncignore` files in sub directories, `\` escapes,
`[!...]`, case insensitive matching. A test checks the matches are the same as `git check-ignore`'s.

`.tsync-old/` and `*.tsync-partial` (downloads in progress) are always ignored.

Each side sends its rules with its index: a path the peer ignores isn't a deletion there (eg you add `*.log` to
your `.tsyncignore`: your `.log` files disappear from your index, but the peer doesn't delete its copies).

## The decision, per path

`L` is ours, `R` the peer's, `B` the base; "changed" means a different content (or a file replaced by a
directory) than the base.

| ours / theirs / base | what we do |
|---|---|
| same content (or absent on both) | nothing, in sync; recorded in the base |
| unchanged here, changed there | **get** theirs (ours kept in `.tsync-old/`) |
| changed here, unchanged there | nothing: the peer gets ours |
| only there, never synced | **get** it |
| only here, never synced | nothing: the peer gets it |
| unchanged here, deleted there | **remove** ours (moved to `.tsync-old/`) |
| deleted here, unchanged there | nothing: the peer removes its copy |
| changed on both sides, or new on both with different content | **conflict** (see below) |
| changed on one side, deleted on the other | **conflict** (see below) |
| file on one side, directory on the other | conflict, left alone, listed |
| same content, mode changed on one side | the mode is applied on the other side |
| same content, modes changed on both sides (or unknown) | each side keeps its own |

The peer, comparing the same three with the roles swapped, decides the mirror action (a test checks this for
every case): when we get a file, it does nothing; when we remove one, it had deleted it; etc.

### Moves and renames

After deciding, a file **new** there (never synced) with exactly the same content (sha256 and size) as one
**deleted** there (unchanged here) is a move: we rename ours to the new path instead of downloading it and
removing the old one. It isn't counted as a deletion either (see the guard below). A renamed directory is all
its files moved this way, plus the new directory created and the old one removed once empty.

Limits: a file renamed *and* edited between 2 syncs is a deletion plus a new file (downloaded); identical files
(eg empty ones) can be paired arbitrarily, which gives the same result anyway.

### Conflicts

A file changed on both sides since the last sync (typically a laptop edited while offline, then back on the
network after the others changed it too) or changed on one side and deleted on the other is resolved without
losing anything (unless `-sync-conflicts list`, see below):

- for changed on both: the newest version (modification time; same time: decided by content, so both sides
  agree) keeps the name; the other one is kept, by the side that has it, as
  `name.sync-conflict-<host>-<UTC time of that version>.ext` (eg `notes.sync-conflict-laptop-20261024-170102.txt`),
  which then syncs to all machines like any new file;
- for changed vs deleted: the changed version is kept as such a copy, so the deletion still applies to the name.

The side keeping a copy first downloads the other version, then hard links its own as the copy and renames the
downloaded one over the name: the name never looks deleted (else the other side would see "changed here,
deleted there" and make a copy of its version too). Scans are locked out during those renames.

The copies are counted on the sync line ("1 conflict copy to review") and listed in
`~/.tsync/sync/<label>/conflicts.txt` until you delete them, on any machine (the deletion propagates).

As the newest version wins, the clocks matter: each side sends its time when the link is up and both warn (log
and sync line) if they differ by more than a second.

With `-sync-conflicts list`, files changed on both sides are left alone (both versions stay where they are) and
listed instead; resolve them by deleting or moving away the version you don't want: the other one is then
synced back. A file changed on one side and deleted on the other comes back (the change wins over the
deletion).

### Mass deletion guard

If the peer's index lacks more than 20 paths at once that we both had, and that's more than half of everything
both had, those deletions are **held**: not applied, shown on the sync line and listed in `conflicts.txt`. That
is what a disk not mounted, or an emptied directory by mistake, looks like. Delete them on this side too if it
was intended, or restore them on the peer. Moves aren't deletions for this.

## Doing it: pull only, checked twice, nothing lost

Each side only ever writes its own disk: it gets from the peer what's newer there, removes what was deleted
there. The peer only serves files.

- A file is requested by path and expected sha256. The peer serves it only if it is still its current version
  and wasn't modified in the last 2s (it may still be being written), else refuses; we try again later
  (3s, then doubling, up to about 3 minutes). It is streamed with the same encrypted frames as file sharing
  (`-max-rate` applies, per peer link).
- The download goes to a temporary `.name.xxxx.tsync-partial` next to the target, checked against the expected
  sha256.
- Right before replacing (or creating, removing, moving, renaming to a conflict copy), our file is checked again
  to still be the one the decision was made on (same size and time, or same content if recently modified). If
  not (you edited it meanwhile), nothing is replaced and it's decided again with the new version.
- The replaced version is kept in `.tsync-old/<path>~<UTC time>` (as a hard link when possible, so the
  replacement is atomic), the last `-sync-keep` (3 by default, 0 for none) versions per file. Removed files go
  there too (at least that version, whatever `-sync-keep` is). Nothing removes them otherwise: clean it up when
  you don't need them.
- The new file gets the peer's mode and modification time.
- All writes go through Go's `os.Root`: nothing can be written outside the synced directory, even through a
  symlink in it.
- Scans and these changes are serialized per synced directory, and a change is recorded before the next scan
  can start: a scan never sees a change half done, nor mistakes a replaced file for the previous one (they can
  have the same size and time).
- The base is updated (and saved) for what got synced, and for what is found the same on both sides.

## Several machines

Every pair of machines that use the same label syncs directly, each pair with its own base (A with B, B with C, A with C). A
change reaches each machine through whichever pair sees it first; when the other pairs compare, both sides
already have the same content: in sync, nothing transferred. Concurrent changes show up as the same conflict
on the pairs involved, resolved the same way (same copy name everywhere). A path is only synced from one peer at
a time.

Using the same label on both sides is the consent: labels are announced to the linked (already trusted, see the
README) peers, the indexes and contents only to the peers that use that label too.

## On the wire

Within the encrypted link of each linked peer (see the README's security section):

- `F`: our time and our labels, when the link is up;
- `I`: our index of a directory both use, in chunks of up to 512KB (the first one with our ignore rules and
  the peers' paths we can't store), sent again when it changes; paths are validated on receipt (normalized, no
  `..`, no control characters...);
- `Q`: get a file (path + expected sha256, with an id that can't collide with file sharing's), answered with the
  usual file data frames, or an abort with the reason (changed since, still being written...).

## Limits

- Changes are noticed by rescanning, every `-sync-rescan`, or when watched (`-sync-watch`) about 2s after
  they're notified.
- Size: meant for directories of up to some tens of thousands of files. Each side keeps its index in memory,
  walks the whole directory at each scan (only changed files are hashed; when watched, only the directories
  notified, and all of it every 15 minutes) and sends the whole index (about 1 MB
  per 10k files) to each peer when anything changed; deciding takes about 70 ms for 100k files and is done at
  most once a second while syncing. An initial sync of 100k small files took about 23s between 2 local
  instances (so mostly the per file overhead, not the network). It works for more (up to a million entries per
  index) but for a big tree, several `-sync` labels for its main sub directories are lighter.
- Names Windows can't store (with one of `<>:"|?*`, ending with a dot or a space, or a device name like `CON`
  or `com1.txt`) aren't synced to Windows machines. The Windows machine warns, and tells the others (in its
  index): both sides show it on the sync line ("1 from mac can't be stored here", "1 can't be stored on
  omen") and list the paths in their `conflicts.txt`, so you know which file to rename where it is.
- Directory modification times aren't synced; empty directories are.
- Names differing only by case (`A.txt` and `a.txt`) are a conflict unless both sides have both (case sensitive
  file systems on both).
- Symlinks are skipped.
