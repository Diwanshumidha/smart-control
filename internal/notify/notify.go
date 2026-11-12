// Package notify gets the user's attention when the AI is waiting on them:
// a desktop notification with Allow / Deny actions for every URL approval,
// and (optionally) the floating confirm window from the desktop app.
package notify

import (
	"context"
	"fmt"
	"log"

	"github.com/Diwanshumidha/smart-control/internal/app"
	"github.com/Diwanshumidha/smart-control/internal/events"
	"github.com/Diwanshumidha/smart-control/internal/security/urlpolicy"
)

// Action is a button pressed on a notification.
type Action struct {
	NotificationID uint32
	Key            string // allow | deny | default (body clicked)
}

// Desktop sends notifications through the platform's notification service.
type Desktop interface {
	Show(summary, body string, timeoutMs int32) (uint32, error)
	Close(id uint32)
	Actions() <-chan Action
	Shutdown()
}

var capabilityVerb = map[string]string{
	urlpolicy.CapBrowse:     "open",
	urlpolicy.CapScreenshot: "screenshot",
	urlpolicy.CapDownload:   "download from",
}

// Run notifies about URL approvals until ctx ends. popup opens the floating
// confirm window; it may be nil.
func Run(ctx context.Context, a *app.App, popup func()) {
	ch, unsubscribe := a.Bus.Subscribe()
	defer unsubscribe()

	desk, err := newDesktop()
	if err != nil {
		log.Printf("notify: desktop notifications unavailable: %v", err)
	}
	var actions <-chan Action
	if desk != nil {
		defer desk.Shutdown()
		actions = desk.Actions()
	}
	shown := map[uint32]string{} // notification id → approval id
	byApproval := map[string]uint32{}

	closeFor := func(approvalID string) {
		if nid, ok := byApproval[approvalID]; ok {
			desk.Close(nid)
			delete(byApproval, approvalID)
			delete(shown, nid)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case act, ok := <-actions:
			if !ok {
				actions = nil
				continue
			}
			id, ok := shown[act.NotificationID]
			if !ok {
				continue
			}
			switch act.Key {
			case "allow", "deny":
				d := urlpolicy.AllowOnce
				if act.Key == "deny" {
					d = urlpolicy.Deny
				}
				if _, err := a.Policy.Decide(ctx, id, d); err != nil {
					log.Printf("notify: %v", err)
				}
			default:
				if popup != nil {
					popup()
				}
			}
		case ev, ok := <-ch:
			if !ok {
				return
			}
			switch ev.Topic {
			case events.ApprovalRequest:
				ap, ok := ev.Data.(urlpolicy.Approval)
				if !ok {
					continue
				}
				st := a.Settings()
				n := st.Notify
				if n.Desktop && desk != nil {
					verb := capabilityVerb[ap.Capability]
					if verb == "" {
						verb = ap.Capability
					}
					body := ap.URL
					if ap.Reason != "" {
						body += "\n" + ap.Reason
					}
					timeout := int32(st.URLPolicy.ApprovalTimeoutSec * 1000)
					nid, err := desk.Show(fmt.Sprintf("The AI wants to %s %s", verb, ap.Host), body, timeout)
					if err != nil {
						log.Printf("notify: %v", err)
					} else {
						shown[nid], byApproval[ap.ID] = ap.ID, nid
					}
				}
				if n.Popup && popup != nil {
					popup()
				}
			case events.ApprovalDecided:
				if desk == nil {
					continue
				}
				switch d := ev.Data.(type) {
				case urlpolicy.Approval:
					closeFor(d.ID)
				case *urlpolicy.Approval:
					closeFor(d.ID)
				case map[string]string:
					closeFor(d["id"])
				}
			}
		}
	}
}
