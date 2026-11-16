# S13 Members and group chats

The workspace owner invites a person from Settings. The link is shown once, on that response, and it expires in 7 days. Accepting it creates a member in the same workspace and shows the authenticator secret once. A member does not invite people. Each person keeps their own agents, secrets, files, and machines, and each person has their own machine cap.

A group needs a title and at least two of the creator's agents. Other people in the workspace can be included. Mentioning a participant with @Name starts a reply from that agent. A name that is not in the group does not start a reply. The group page polls while a reply is in progress. Stop, approve, and the run stream stay with the agent owner.

## What the tests show

Schema version 29 adds `invites` and `thread_members`. Opening the database twice records version 29 once. Both tables carry `user_id` and `workspace_id`.

`TestInviteMemberAndSeparateQuota` refuses a member invite, refuses an email that already has an account, rotates a pending link, and accepts the new link once. The member is in the same workspace with a separate quota. The owner's machines do not count toward the member.

`TestInviteExpiryAndRevoke` drops a revoked invite and an invite past 7 days. Another user cannot revoke it.

`TestGroupAccessMentionAndRun` keeps a private chat private, lets a member read and post the shared group, and returns not found for another workspace. Mentions match participant names, including a name with a space, and skip a longer name and a name inside a longer token. A run for an agent outside the group is not found.

`TestMembersInvitesAndGroupChat` shows the invite once, spends it, and refuses a second accept. A member cannot invite. Another workspace gets 404 on the group, the owner's chat, and revoke. The member can post. Mentioning two agents starts two replies owned by the group owner. The member cannot open those runs. An unmatched name does not start a reply. The member's machine does not change the owner's "1 of 4".

`TestExpiredInviteIsNotFound` returns 404 for an invite past 7 days.

`TestRequestLogHidesInviteToken` logs `/invite/redacted` and does not log the token.

## Live

No live tag was run. No machine API was called. Machines created: 0. Spend: $0.

## Evidence

Headless Chrome, dark and light, 1280 and 390, no sideways scroll. Demo data only. Alex Rivera, workspace Personal, member Quinn Hale, pending invite Pip Lane, agents Atlas and Pip, group Standup. The messages were seeded. The invite page text does not include the link token. A signed-in person sees the invite page in their theme. A logged-out invitee gets the dark sign-in layout.

- `people-dark-1280.png`, `people-dark-390.png`, `people-light-1280.png`, `people-light-390.png` People, the member, the pending invite, and Create invite
- `usage-dark-1280.png`, `usage-dark-390.png`, `usage-light-1280.png`, `usage-light-390.png` Your machines 1 of 4, and Your uploads 0 B of 2 GB
- `group-dark-1280.png`, `group-dark-390.png`, `group-light-1280.png`, `group-light-390.png` Standup, named replies from Atlas and Pip, and Quinn Hale
- `new-dark-1280.png`, `new-dark-390.png`, `new-light-1280.png`, `new-light-390.png` New group, Atlas, Pip, and Quinn Hale
- `invite-dark-1280.png`, `invite-dark-390.png`, `invite-light-1280.png`, `invite-light-390.png` Join Personal for Pip Lane
