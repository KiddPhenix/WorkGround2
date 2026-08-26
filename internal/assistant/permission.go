package assistant

import "workground2/internal/permission"

// PermissionPolicy translates a frozen Assistant policy into the shared tool
// permission rules used by both Desktop and the local daemon. Browser actions
// carry authoritative subjects classified from the current page snapshot:
// ordinary, publish, delete, payment, secret, or private.
func PermissionPolicy(policy Policy) permission.Policy {
	safeLocalWrites := []string{"write_file", "edit_file", "multi_edit", "notebook_edit"}
	localAll := []string{
		"write_file", "edit_file", "multi_edit", "notebook_edit",
		"move_file", "delete_range", "delete_symbol", "bash", "run_skill",
	}
	networkAll := []string{
		"web_fetch", "web_search", "browser_open", "browser_navigate", "browser_state", "browser_scroll",
		"browser_tab", "browser_close", "browser_click", "browser_type", "browser_upload", "browser_attach",
		"assistant_channel_metrics", "assistant_channel_publish", "assistant_channel_reply",
	}
	networkReaders := []string{
		"web_fetch", "web_search", "browser_open", "browser_navigate", "browser_state", "browser_scroll",
		"browser_tab", "browser_close", "assistant_channel_metrics",
	}
	allow := []string{"memory", "remember", "forget"}
	ask := []string{"delete_range", "delete_symbol", "browser_attach", "mcp__*"}
	deny := []string{}
	add := func(access Access, rules ...string) {
		switch access {
		case AccessAllow:
			allow = append(allow, rules...)
		case AccessDeny:
			deny = append(deny, rules...)
		default:
			ask = append(ask, rules...)
		}
	}

	switch policy.LocalWrite {
	case AccessAllow:
		allow = append(allow, safeLocalWrites...)
		allow = append(allow, "bash", "run_skill")
	case AccessDeny:
		deny = append(deny, localAll...)
	case AccessApprove:
		ask = append(ask, "bash", "run_skill")
	}

	switch policy.Network {
	case AccessDeny:
		deny = append(deny, networkAll...)
		deny = append(deny, "mcp__*")
	case AccessApprove:
		ask = append(ask, networkAll...)
	case AccessAllow:
		allow = append(allow, networkReaders...)
		allow = append(allow, "browser_click(ordinary)", "browser_type(ordinary)")
		add(policy.Publish,
			"browser_click(publish)", "browser_type(publish)",
			"assistant_channel_publish", "assistant_channel_reply",
		)

		// Destructive, payment, credential and private-data boundaries remain
		// fail-closed even when their persisted value is allow. Their UI labels
		// explicitly describe that allow still means per-action approval.
		if policy.Delete == AccessDeny {
			deny = append(deny, "browser_click(delete)")
		} else {
			ask = append(ask, "browser_click(delete)")
		}
		if policy.Payment == AccessDeny {
			deny = append(deny, "browser_click(payment)")
		} else {
			ask = append(ask, "browser_click(payment)")
		}
		if policy.Secrets == AccessDeny {
			deny = append(deny, "browser_type(secret)")
		} else {
			ask = append(ask, "browser_type(secret)")
		}
		if policy.Private == AccessDeny {
			deny = append(deny, "browser_type(private)", "browser_upload")
		} else {
			ask = append(ask, "browser_type(private)", "browser_upload")
		}
	}

	// install_source fetches untrusted content and writes a project
	// capability, so both frozen dimensions must explicitly allow it.
	switch {
	case policy.LocalWrite == AccessDeny || policy.Network == AccessDeny:
		deny = append(deny, "install_source")
	case policy.LocalWrite == AccessAllow && policy.Network == AccessAllow:
		allow = append(allow, "install_source")
	default:
		ask = append(ask, "install_source")
	}
	return permission.New("ask", allow, ask, deny)
}
