# Sentry

Issues in the orgs this token can see. The token stays in the vault under `SENTRY_CONNECTION`. The agent never sees it.

Reads: `sentry_list_orgs`, `sentry_list_projects`, `sentry_list_issues`, `sentry_get_issue`. The org comes from the list, not from a second field. Writes, which ask first: `sentry_resolve`. There is no delete, no alert rule, and no member admin.

Reads are always-allow until you change them. Writes stay ask until you change them. Disconnect keeps the vault row and turns the connection off for every agent.
