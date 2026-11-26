# Linear

Issues and comments for the work queue. The personal API key stays in the vault under `LINEAR_CONNECTION`. The agent never sees it. GitHub issues stay on the GitHub connection.

Every call is a POST to `api.linear.app` with a fixed document. The tool arguments fill the variables. The agent does not send the document.

Reads: `linear_list_teams`, `linear_list_issues`, `linear_get_issue`, `linear_list_comments`. Writes, which ask first: `linear_create_issue`, `linear_comment`, `linear_update_status`. There is no delete, no project admin, and no invite.

`linear_update_status` takes the workflow state id returned with the team or the issue.

Reads are always-allow until you change them. Writes stay ask until you change them. Disconnect keeps the vault row and turns the connection off for every agent.
