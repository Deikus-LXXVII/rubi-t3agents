package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func twoDrafts(t *testing.T) (*fakeHost, *sisters, *fakeSSH) {
	f, s, _, fs := sshFixture(t)
	pok(t, s, f, "ssh_add", `{"device":"dev_mac","text":"web-1 web.example.com deploy 2222"}`)
	pok(t, s, f, "ssh_add", `{"device":"dev_mac","text":"db-1 db.example.com admin"}`)
	pok(t, s, f, "ssh_draft_cmd", `{"device":"dev_mac","item":"db-1","text":"uptime"}`)
	return f, s, fs
}

func TestSSHBatchTwoTargetsOneApprovalWithTheSinglePreviewEach(t *testing.T) {
	f, s, fs := twoDrafts(t)
	pok(t, s, f, "ssh_confirm_all", `{"device":"dev_mac"}`)
	if len(f.submits) != 1 {
		t.Fatalf("%d approvals", len(f.submits))
	}
	r := f.submits[0]
	if r.Kind != kindSSHTarget || len(r.Items) != 2 || len(fs.registeredNames()) != 0 {
		t.Fatalf("%+v", r)
	}
	// Each item shows what the single approval shows: fingerprint, host, user, allowed commands.
	for _, it := range r.Items {
		p, _ := it.Preview.(map[string]any)
		if p["host_key_fingerprint"] != fpGood || p["host"] == nil || p["user"] == nil || p["allowed_commands"] == nil || p["compare_the_fingerprint"] == nil || p["what_it_does"] == nil {
			t.Errorf("%s: %+v", it.Key, p)
		}
	}
	// The single path's preview for the same draft is the same map.
	f2, s2, _, _ := sshFixture(t)
	pok(t, s2, f2, "ssh_add", `{"device":"dev_mac","text":"web-1 web.example.com deploy 2222"}`)
	pok(t, s2, f2, "ssh_confirm", `{"device":"dev_mac","item":"web-1","text":""}`)
	a, _ := json.Marshal(lastSubmit(t, f2).Preview)
	var web map[string]any
	for _, it := range r.Items {
		if it.Key == "web-1" {
			web, _ = it.Preview.(map[string]any)
		}
	}
	b, _ := json.Marshal(web)
	if string(a) != string(b) {
		t.Fatalf("single %s\nbatch  %s", a, b)
	}
}

func TestSSHBatchRegistersOnlyTheTickedTargets(t *testing.T) {
	f, s, fs := twoDrafts(t)
	pok(t, s, f, "ssh_confirm_all", `{"device":"dev_mac"}`)
	payload, _ := json.Marshal(f.submits[0].Payload)
	var bt sshBatchTicket
	if err := json.Unmarshal(payload, &bt); err != nil {
		t.Fatal(err)
	}
	res, err := s.executeSSHRegisterAll(context.Background(), f, "items:db-1", bt)
	if err != nil {
		t.Fatal(err)
	}
	if got := fs.registeredNames(); !slices.Equal(got, []string{"db-1"}) {
		t.Fatalf("registered %v (%v)", got, res)
	}
	// The unticked one stays a draft.
	if d := s.draftsOf("dev_mac"); len(d) != 1 || d[0].Name != "web-1" {
		t.Fatalf("%+v", d)
	}
}

func TestSSHBatchWithoutItemsRegistersNothing(t *testing.T) {
	for _, option := range []string{"", "register", "items:", "items:web-1,nope"} {
		f, s, fs := twoDrafts(t)
		pok(t, s, f, "ssh_confirm_all", `{"device":"dev_mac"}`)
		payload, _ := json.Marshal(f.submits[0].Payload)
		var bt sshBatchTicket
		_ = json.Unmarshal(payload, &bt)
		_, _ = s.executeSSHRegisterAll(context.Background(), f, option, bt)
		if n := fs.registeredNames(); len(n) != 0 {
			t.Errorf("option %q registered %v", option, n)
		}
	}
}

func TestSSHBatchAsksNothingWhenOneServerCantBeRead(t *testing.T) {
	f, s, fs := twoDrafts(t)
	fs.scanErr = homeRefusal("connection refused")
	if _, err := pcall(t, s, f, "ssh_confirm_all", `{"device":"dev_mac"}`); err == nil || !strings.Contains(err.Error(), "nothing was asked") || len(f.submits) != 0 {
		t.Fatalf("%v %d", err, len(f.submits))
	}
}

func TestSSHBatchNeedsTwoAndThePageOffersIt(t *testing.T) {
	f, s, _, _ := sshFixture(t)
	pok(t, s, f, "ssh_add", `{"device":"dev_mac","text":"web-1 web.example.com deploy"}`)
	if _, err := pcall(t, s, f, "ssh_confirm_all", `{"device":"dev_mac"}`); err == nil || len(f.submits) != 0 {
		t.Fatalf("%v", err)
	}
	if strings.Contains(pageText(view(t, s, f, `{"page":"ssh"}`)), "ssh_confirm_all") {
		t.Fatal("the page offers it with one draft")
	}
	pok(t, s, f, "ssh_add", `{"device":"dev_mac","text":"db-1 db.example.com admin"}`)
	if !strings.Contains(pageText(view(t, s, f, `{"page":"ssh"}`)), "ssh_confirm_all") {
		t.Fatal("the page doesn't offer it with two drafts")
	}
}

// A payload whose tickets name another computer, or the same target twice, is refused before anything is registered.
func TestSSHBatchRefusesAMixedPayload(t *testing.T) {
	f, s, fs := twoDrafts(t)
	pok(t, s, f, "ssh_confirm_all", `{"device":"dev_mac"}`)
	payload, _ := json.Marshal(f.submits[0].Payload)
	for name, edit := range map[string]func(*sshBatchTicket){
		"other computer": func(b *sshBatchTicket) { b.Targets[1].Device = "dev_other" },
		"duplicate":      func(b *sshBatchTicket) { b.Targets[1].Name = b.Targets[0].Name },
	} {
		var bt sshBatchTicket
		_ = json.Unmarshal(payload, &bt)
		edit(&bt)
		if _, err := s.executeSSHRegisterAll(context.Background(), f, "items:"+bt.Targets[0].Name+","+bt.Targets[1].Name, bt); err == nil || len(fs.registeredNames()) != 0 {
			t.Errorf("%s: %v %v", name, err, fs.registeredNames())
		}
	}
}

func TestSSHBatchIsCapped(t *testing.T) {
	f, s, _, _ := sshFixture(t)
	for i := range maxSSHBatch + 1 {
		pok(t, s, f, "ssh_add", `{"device":"dev_mac","text":"t`+string(rune('a'+i))+` h`+string(rune('a'+i))+`.example.com u"}`)
	}
	if _, err := pcall(t, s, f, "ssh_confirm_all", `{"device":"dev_mac"}`); err == nil || !strings.Contains(err.Error(), "at most") || len(f.submits) != 0 {
		t.Fatalf("%v", err)
	}
}

// A draft the user edited after the approval was asked is registered as approved, and the edited draft stays.
func TestSSHRegisterKeepsADraftEditedAfterTheApproval(t *testing.T) {
	f, s, fs := twoDrafts(t)
	pok(t, s, f, "ssh_confirm_all", `{"device":"dev_mac"}`)
	payload, _ := json.Marshal(f.submits[0].Payload)
	var bt sshBatchTicket
	_ = json.Unmarshal(payload, &bt)
	pok(t, s, f, "ssh_draft_cmd", `{"device":"dev_mac","item":"db-1","text":"df -h"}`)
	if _, err := s.executeSSHRegisterAll(context.Background(), f, "items:db-1,web-1", bt); err != nil {
		t.Fatal(err)
	}
	if len(fs.registeredNames()) != 2 {
		t.Fatalf("%v", fs.registeredNames())
	}
	for _, r := range fs.registers {
		if r["name"] == "db-1" {
			if cs, _ := r["allowed_commands"].([]any); len(cs) != 1 || cs[0] != "uptime" {
				t.Fatalf("registered what the user did not approve: %v", r)
			}
		}
	}
	d := s.draftsOf("dev_mac")
	if len(d) != 1 || d[0].Name != "db-1" || len(d[0].Allowed) != 2 {
		t.Fatalf("the edited draft is gone or changed: %+v", d)
	}
}
