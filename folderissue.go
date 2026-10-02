package main

// Folder issues: what is wrong with a synced folder, in plain words. Syncer
// counts a folder as having an issue exactly when folderIssue has something
// to say about it, so every issue it counts can be explained.

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ApolloF/syncer/internal/syncthing"
)

// hasIssue reports whether Syncthing has a problem with a folder: it stopped
// the folder, or some of its files can't be synced.
func hasIssue(st syncthing.FolderStatus) bool {
	return st.Error != "" || st.Errors+st.PullErrors > 0 || st.State == "error"
}

// folderIssue says what is wrong with folder id, or "" when nothing is.
func folderIssue(ctx context.Context, c *syncthing.Client, id string, st syncthing.FolderStatus) string {
	if !hasIssue(st) {
		return ""
	}
	if st.Error != "" {
		return plainFolderError(st.Error)
	}
	// Errors and PullErrors are the same count (older Syncthings set both).
	n := max(st.Errors, st.PullErrors)
	if n == 0 {
		return "Syncthing stopped syncing this folder."
	}
	what := "1 file can't be synced"
	if n > 1 {
		what = fmt.Sprintf("%d files can't be synced", n)
	}
	if es, err := c.FolderErrors(ctx, id, 1); err == nil && len(es) > 0 {
		return fmt.Sprintf("%s: %s (%s).", what, es[0].Path, plainFileError(es[0].Error))
	}
	return what + "."
}

// Syncthing's own words for a folder it stopped because its folder or the
// folder's marker (.stfolder) is gone.
const (
	stMarkerMissing = "folder marker missing"
	stPathMissing   = "folder path missing"
)

func markerMissing(msg string) bool {
	return strings.Contains(strings.ToLower(msg), stMarkerMissing)
}

func pathMissing(msg string) bool {
	return strings.Contains(strings.ToLower(msg), stPathMissing)
}

// plainFolderError turns why Syncthing stopped a folder into plain words.
// Messages it doesn't know are passed on as they are.
func plainFolderError(msg string) string {
	m := strings.ToLower(msg)
	switch {
	case markerMissing(m):
		return "The folder was emptied or replaced outside Syncer, so syncing stopped to protect the other PCs' copies."
	case pathMissing(m):
		return "The folder is gone."
	case strings.Contains(m, "folder path not a directory"):
		return "There's a file where the folder should be."
	case strings.Contains(m, "insufficient space"), strings.Contains(m, "not enough space"):
		return "The disk is almost full, so syncing stopped."
	case strings.Contains(m, "access is denied"), strings.Contains(m, "permission denied"):
		return "Syncthing isn't allowed to open the folder."
	}
	return sentence(msg)
}

// plainFileError turns why Syncthing couldn't sync a file into plain words.
func plainFileError(msg string) string {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "used by another process"):
		return "it's open in another program; is the game running?"
	case strings.Contains(m, "access is denied"), strings.Contains(m, "permission denied"):
		return "Syncthing isn't allowed to change it"
	case strings.Contains(m, "insufficient space"), strings.Contains(m, "not enough space"):
		return "the disk is full"
	case strings.Contains(m, "no connected device has the required version"), strings.Contains(m, "peers who had this file went away"):
		return "no PC that has it is online"
	case strings.Contains(m, "modified but not rescanned"):
		return "it changed while syncing; Syncthing tries again"
	}
	return strings.TrimSpace(msg)
}

// sentence starts msg with a capital letter and ends it with a full stop.
func sentence(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ""
	}
	r, n := utf8.DecodeRuneInString(msg)
	msg = string(unicode.ToUpper(r)) + msg[n:]
	if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	return msg
}
