# S27 Per-agent colors

Each agent stores one palette key. That key colors the avatar in the sidebar, the chat header, the agent panel, and the message avatar. The same key colors every human bubble in that agent's chat, including a message that was already on the page. Agent reply bubbles stay on the surface color. Edit and Machine live in the side panel. The panel chrome uses the same avatar class.

The palette is Sol-approved (November 11, 2026). It is not a proposal. Keys stay c1 to c9. c0 remains a legacy default and is not assigned.

Schema version 20 backfills empty and c0 rows. A key that is already pickable is left as it is. The update touches avatar_color only. A second open does not shuffle colors.

## Palette

The avatar fill is the same in both themes. The avatar letter is #10161a. Bubble fill, border, and text are literal hex.

| Key | Name | Avatar | Avatar ratio | Dark fill | Dark border | Dark text | Dark ratio | Light fill | Light border | Light text | Light ratio |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| c1 | Coral | #ffb38a | 10.48 | #52443c | #7e6050 | #e7eef0 | 7.95 | #ffe2d3 | #ffcaad | #1b2226 | 13.10 |
| c2 | Mint | #8fd8c4 | 11.10 | #354e4b | #4c716a | #e7eef0 | 7.63 | #d4f0e9 | #b1e4d6 | #1b2226 | 13.38 |
| c3 | Violet | #c9b6ff | 10.09 | #44455b | #666285 | #e7eef0 | 7.96 | #eae3ff | #d9ccff | #1b2226 | 12.99 |
| c4 | Gold | #ffd57a | 13.07 | #524d38 | #7e7049 | #e7eef0 | 7.22 | #ffefcc | #ffe2a2 | #1b2226 | 14.17 |
| c5 | Sky | #9cc7ff | 10.44 | #38495b | #526a85 | #e7eef0 | 7.87 | #d9eaff | #bad8ff | #1b2226 | 13.17 |
| c6 | Rose | #f59fb8 | 9.15 | #4f3f48 | #7a5865 | #e7eef0 | 8.37 | #fbdbe4 | #f8bccd | #1b2226 | 12.55 |
| c7 | Lime | #a7e08a | 11.87 | #3b503c | #577550 | #e7eef0 | 7.46 | #def3d3 | #c1e9ad | #1b2226 | 13.71 |
| c8 | Sand | #d9c9a3 | 11.14 | #484a43 | #6d6a5c | #e7eef0 | 7.66 | #f1eadc | #e4d9bf | #1b2226 | 13.46 |
| c9 | Aqua | #86dcf2 | 11.75 | #324f57 | #48737f | #e7eef0 | 7.48 | #d1f2fa | #aae6f6 | #1b2226 | 13.65 |

Every bubble ratio is at least 7.22:1. `TestBubbleContrast` measures the same pairs and requires at least 4.5:1.

## What the tests show

`TestMigrateV20Backfill` keeps a pickable key, assigns c0 rows in created order with the avoid-in-use rule, and does it per user. A second open leaves the colors alone.

`TestNextAgentColorAvoidsInUse` gives the first nine agents c1 through c9. The tenth reuses c1. Deleting the agents that held c1 frees it. Another user's first agent is c1.

`TestRejectUnknownColorOnWrite` rejects c0, empty, hex, and an unknown key on SetAgentColor, CreateAgent, and SaveAgentFields. The row is unchanged.

`TestChatCreateStoresAutoColor` stores the auto-pick from the setup card, shows that color on the card, and does not put a swatch row on the card.

`TestAdvancedColorChoiceAndDefault` preselects the auto-pick. Omitting the field stores that key. A chosen swatch is stored and shown on the chat avatar.

`TestEditColorUpdatesChatBubbles` changes the color and updates the sidebar, the chat header, the agent panel, the machine panel, the message avatar, and every human bubble, including one that existed before the change. The page has no inline style.

`TestColorRejectedOnWrite` returns 400 with "That color isn't available. Pick one from the list." for an empty value, c0, a hex value, and an unknown key. Nothing is written.

`TestColorPickerAccessible` shows the nine names, native radios, and the accessible name "Color: Coral" on Advanced and on Edit. Advanced includes the hint. Edit does not.

`TestColorAuthzAndCSRF` returns 404 when another user posts a color, and 403 when the CSRF token is wrong.

`TestPaletteCSSMatchesTable` requires the avatar, bubble, and queued rules to use the palette hex.

## Live

No live tag was run. No machine API was called. Machines created: 0. Spend: $0.

## Evidence

Headless Chrome, dark and light, 1280 and 390. No sideways scroll, no console or content-security errors, and phone swatches are at least 44 px. Demo data is Alex Rivera on example accounts, with Scout (Gold), Nia (Rose), and Pip (Coral). With those three in use, Advanced preselects Mint.

The picker shots move the selection with the right arrow and back with the left arrow before the picture, and the review avatar follows the class. Chat shots use the side panel header. The colored avatar opens the agent panel, and the machine chip opens the machine panel. Home shots are the pick screen. The sidebar still shows Scout, Nia, and Pip.

- `chat-dark-1280.png`, `chat-light-1280.png`, `chat-dark-390.png`, `chat-light-390.png` header avatar and human bubbles, old and new
- `list-dark-1280.png`, `list-light-1280.png`, `list-dark-390.png`, `list-light-390.png` home pick screen and the colored sidebar. On a phone the list fills the screen
- `advanced-dark-1280.png`, `advanced-light-1280.png`, `advanced-dark-390.png`, `advanced-light-390.png` Advanced picker, Mint preselected, with the hint
- `edit-dark-1280.png`, `edit-light-1280.png`, `edit-dark-390.png`, `edit-light-390.png` Edit picker, Gold selected, no hint
