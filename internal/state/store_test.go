package state

import (
	"testing"

	"hynode/internal/panel"
)

func TestSnapshotCommitKeepsTrafficAddedAfterSnapshot(t *testing.T) {
	store := New()
	store.ReplaceUsers([]panel.User{{ID: 1, UUID: "u"}})
	store.Add("a", 1, 10, 20)
	snapshot := store.Snapshot("a")
	store.Add("a", 1, 4, 5)
	store.Commit("a", snapshot)
	remaining := store.Snapshot("a").Traffic["1"]
	if remaining != [2]int64{4, 5} {
		t.Fatalf("remaining traffic = %v", remaining)
	}
}

func TestDeviceLimitSharedByStore(t *testing.T) {
	store := New()
	store.ReplaceUsers([]panel.User{{ID: 1, UUID: "u", DeviceLimit: 1}})
	if !store.Open("a", 1, "1.1.1.1") {
		t.Fatal("first IP rejected")
	}
	if store.Open("b", 1, "2.2.2.2") {
		t.Fatal("second IP accepted above device limit")
	}
	if !store.Open("b", 1, "1.1.1.1") {
		t.Fatal("existing IP rejected")
	}
}
