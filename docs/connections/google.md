# Google

Mail and calendar for one Gmail address. The address and an app password stay in the vault under `GOOGLE_CONNECTION`. The agent never sees the app password. The owner turns on two-factor authentication and creates the app password. Signing in with a browser token is a follow-up.

The guest still speaks the catalog tools. The host speaks IMAP, SMTP, and CalDAV. IMAP uses `imap.gmail.com` on port 993. SMTP uses `smtp.gmail.com` on port 465. Calendar uses `https://apidata.googleusercontent.com`. A generic mail host is not accepted. The agent cannot choose the address.

Reads: `google_list_messages`, `google_get_message`, `google_list_events`. Writes, which ask first: `google_create_draft`, `google_send`, `google_create_event`. Message reads return headers and text, capped around 64 KB. Attachments stay metadata. There is no delete, no sharing change, and no mailbox rule.

Reads are always-allow until you change them. Writes stay ask until you change them. Disconnect keeps the vault row and turns the connection off for every agent.
