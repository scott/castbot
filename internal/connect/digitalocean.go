package connect

func init() {
	register(digitalOceanEntry())
}

func digitalOceanEntry() Entry {
	return Entry{
		ID: DigitalOcean, Title: "DigitalOcean", SecretName: SecretNameDigitalOcean,
		Detail:     "API token. Calls use the MCP server only. You choose what it can access. Stored in your vault.",
		TokenLabel: "DigitalOcean token",
		// The MCP host is the tool path. The API host stays for doctl HTTP.
		Hosts:     []string{"mcp.digitalocean.com", "api.digitalocean.com"},
		APIHost:   "api.digitalocean.com",
		Auth:      AuthBearer,
		RemoteMCP: DigitalOceanMCPURL,
		Aliases:   []string{"DIGITALOCEAN_API_TOKEN", "DIGITALOCEAN_TOKEN", "DIGITALOCEAN_ACCESS_TOKEN"},
		Tools:     digitalOceanTools,
		Retired:   retiredDigitalOceanTools,
	}
}

// digitalOceanTools are the hosted MCP router tools. Path stays empty so
// the adapter cannot turn them into api.digitalocean.com requests.
var digitalOceanTools = []Tool{
	{Name: "find_tools", Title: "DigitalOcean: find tools"},
	{Name: "retrieve_schema", Title: "DigitalOcean: read a tool schema"},
	{Name: "invoke_tool", Title: "DigitalOcean: run a tool", Write: true},
}

var retiredDigitalOceanTools = []string{
	"do_account_get",
	"do_droplets_list",
	"do_droplet_get",
	"do_apps_list",
	"do_droplet_create",
	"do_droplet_delete",
}
