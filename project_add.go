package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Deikus-LXXVII/rubi/sdk/rubiplugin"
)

// projectAddsPerHour limits how often one Bot asks the user to add a folder.
const projectAddsPerHour = 5

type projectAddIn struct {
	Agent         string `json:"rubi_agent" jsonschema:"your Bot name as connected to Rubi"`
	AgentCode     string `json:"rubi_agent_code,omitempty" jsonschema:"the agent_code of your latest Rubi webhook (or call rubi_verify with your name to get one); proves you are that Bot"`
	VerifiedAgent string `json:"rubi_verified_agent,omitempty" jsonschema:"set by Rubi; don't pass it"`
	Device        string `json:"device,omitempty" jsonschema:"the computer, when there are several"`
	Dir           string `json:"dir" jsonschema:"the absolute path of the folder on that computer, the one the user told you to use"`
}

// projectAddTool asks the user (one strong approval) to add a folder as a project of the agent path. The folder is
// the one the Bot names: the setup no longer asks the user to choose one. Rubi Home checks it like a chosen folder
// (no system folder, not the home folder itself or a private folder in it), and core lets Home's call through only
// while the user's approval of exactly this folder is being executed.
func (s *sisters) projectAddTool(ctx context.Context, h sisterHost, in projectAddIn) (any, error) {
	sister := strings.TrimSpace(in.VerifiedAgent)
	if sister == "" || len(sister) > 64 {
		return nil, errors.New("Rubi didn't confirm which Bot you are: pass rubi_agent (your Bot name) and rubi_agent_code (the agent_code of your latest Rubi webhook, or call rubi_verify to get one)")
	}
	dir := strings.TrimSpace(in.Dir)
	if dir == "" || len(dir) > 1024 || !strings.HasPrefix(dir, "/") || strings.ContainsFunc(dir, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return nil, errors.New("dir must be the absolute path of a folder on that computer")
	}
	d, err := s.setupDevice(ctx, h, in.Device)
	if err != nil {
		return nil, err
	}
	if !s.allow("projectadd|"+botID(sister), projectAddsPerHour) {
		return nil, fmt.Errorf("at most %d folder requests per hour", projectAddsPerHour)
	}
	preview := map[string]any{"bot": sister, "computer": d.Name, "folder": dir,
		"what_it_does": "Adds the folder " + dir + " on " + d.Name + " as a project of the agent path, asked by " + sister + " (a Bot). Claude in the project's VM can then read and write everything in it, and Bots can start sessions there with your approvals. Remove it any time on the setup page."}
	return h.Submit(ctx, rubiplugin.Request{Kind: kindSetupProject, Summary: "Add the folder " + oneLine(dir, 80) + " as an agent project on " + d.Name + " (asked by " + sister + ")",
		Preview: preview, Payload: setupTicket{Device: d.ID, DeviceName: d.Name, Dir: dir}})
}
