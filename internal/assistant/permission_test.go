package assistant

import (
	"testing"

	"workground2/internal/permission"
)

func TestPermissionPolicyBrowserAndPublishThreeStates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		network Access
		publish Access
		want    permission.Decision
	}{
		{"allowed", AccessAllow, AccessAllow, permission.Allow},
		{"publish approval", AccessAllow, AccessApprove, permission.Ask},
		{"publish denied", AccessAllow, AccessDeny, permission.Deny},
		{"network approval wins", AccessApprove, AccessAllow, permission.Ask},
		{"network denial wins", AccessDeny, AccessAllow, permission.Deny},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := DefaultPolicy()
			policy.Network = tc.network
			policy.Publish = tc.publish
			got := PermissionPolicy(policy)
			for _, call := range []struct{ tool, subject string }{
				{"browser_click", "publish"},
				{"browser_type", "publish"},
				{"assistant_channel_publish", ""},
				{"assistant_channel_reply", ""},
			} {
				if decision := got.DecideSubject(call.tool, false, call.subject); decision != tc.want {
					t.Fatalf("%s(%s) = %s, want %s", call.tool, call.subject, decision, tc.want)
				}
			}
		})
	}
}

func TestPermissionPolicyAllowsOrdinaryBrowserActionsWithNetwork(t *testing.T) {
	policy := DefaultPolicy()
	policy.Network = AccessAllow
	got := PermissionPolicy(policy)
	for _, call := range []struct{ tool, subject string }{
		{"browser_click", "ordinary"},
		{"browser_type", "ordinary"},
	} {
		if decision := got.DecideSubject(call.tool, false, call.subject); decision != permission.Allow {
			t.Fatalf("%s(%s) = %s, want allow", call.tool, call.subject, decision)
		}
	}
}

func TestPermissionPolicyKeepsSensitiveBrowserActionsFailClosed(t *testing.T) {
	policy := DefaultPolicy()
	policy.Network = AccessAllow
	policy.Delete = AccessAllow
	policy.Payment = AccessAllow
	policy.Secrets = AccessAllow
	policy.Private = AccessAllow
	got := PermissionPolicy(policy)
	for _, call := range []struct{ tool, subject string }{
		{"browser_click", "delete"},
		{"browser_click", "payment"},
		{"browser_type", "secret"},
		{"browser_type", "private"},
		{"browser_upload", ""},
	} {
		if decision := got.DecideSubject(call.tool, false, call.subject); decision != permission.Ask {
			t.Fatalf("%s(%s) = %s, want ask", call.tool, call.subject, decision)
		}
	}
}
