package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ApolloF/syncer/internal/syncthing"
)

func TestFolderErrorsInPlainWords(t *testing.T) {
	for msg, want := range map[string]string{
		"folder marker missing (this indicates potential data loss, search docs/forum to get information about how to proceed)": "The folder was emptied or replaced outside Syncer, so syncing stopped to protect the other PCs' copies.",
		"folder path missing":                          "The folder is gone.",
		"folder path not a directory":                  "There's a file where the folder should be.",
		"insufficient space on disk for database (C:)": "The disk is almost full, so syncing stopped.",
		"something new from Syncthing":                 "Something new from Syncthing.",
	} {
		if got := plainFolderError(msg); got != want {
			t.Errorf("%q: %q, want %q", msg, got, want)
		}
	}
	for msg, want := range map[string]string{
		"opening temp file: The process cannot access the file because it is being used by another process.": "it's open in another program; is the game running?",
		"no connected device has the required version of this file":                                          "no PC that has it is online",
		"pull: Access is denied.": "Syncthing isn't allowed to change it",
		"something else":          "something else",
	} {
		if got := plainFileError(msg); got != want {
			t.Errorf("%q: %q, want %q", msg, got, want)
		}
	}
}

// fakeSyncthing answers /rest/folder/errors with one file that couldn't be
// synced.
func fakeSyncthing(t *testing.T) *syncthing.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/folder/errors" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"folder":"f","errors":[{"path":"Saves/slot1.sav","error":"The process cannot access the file because it is being used by another process."}],"page":1,"perpage":1}`))
	}))
	t.Cleanup(srv.Close)
	c, err := syncthing.NewAt(strings.TrimPrefix(srv.URL, "http://"), "key")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestEveryFolderIssueCountedIsExplained(t *testing.T) {
	c := fakeSyncthing(t)
	ctx := context.Background()
	for _, c2 := range []struct {
		st   syncthing.FolderStatus
		want string
	}{
		{syncthing.FolderStatus{State: "idle"}, ""},
		{syncthing.FolderStatus{State: "error", Error: "folder path missing"}, "The folder is gone."},
		{syncthing.FolderStatus{State: "error"}, "Syncthing stopped syncing this folder."},
		{syncthing.FolderStatus{State: "idle", Errors: 2, PullErrors: 2}, "2 files can't be synced: Saves/slot1.sav (it's open in another program; is the game running?)."},
		{syncthing.FolderStatus{State: "idle", PullErrors: 1}, "1 file can't be synced: Saves/slot1.sav (it's open in another program; is the game running?)."},
	} {
		got := folderIssue(ctx, c, "f", c2.st)
		if got != c2.want {
			t.Errorf("%+v: %q, want %q", c2.st, got, c2.want)
		}
		if hasIssue(c2.st) != (got != "") {
			t.Errorf("%+v: counted %v but explained as %q", c2.st, hasIssue(c2.st), got)
		}
	}
}

func TestFileIssueWithoutDetailsStillSaysWhat(t *testing.T) {
	c, err := syncthing.NewAt("127.0.0.1:1", "key") // nothing answers
	if err != nil {
		t.Fatal(err)
	}
	if got := folderIssue(context.Background(), c, "f", syncthing.FolderStatus{Errors: 3}); got != "3 files can't be synced." {
		t.Errorf("got %q", got)
	}
}
