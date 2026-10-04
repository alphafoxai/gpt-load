package qoder

// Qoder and Qoder CN are separate subscriptions. The protocol is the same;
// the hosts and the device-flow client id are not. Values match
// @magpie-community/opencode-qoder-auth 0.2.5.

const (
	chatPath   = "/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"
	modelsPath = "/algo/api/v2/model/list?Encode=1"

	deviceSelectPath  = "/device/selectAccounts"
	devicePollPath    = "/api/v1/deviceToken/poll"
	deviceRefreshPath = "/api/v1/deviceToken/refresh"
	jobTokenPath      = "/api/v1/me/jobToken"
	jobRefreshPath    = "/api/v1/jobToken/refresh"
	userInfoPath      = "/api/v1/userinfo"

	siteQoder   = "qoder"
	siteQoderCN = "qoder-cn"
)

type site struct {
	ID          string
	Name        string
	Web         string
	OpenAPI     string
	API         string
	ClientID    string
	RedirectURI string
	DeviceChat  bool
}

func (s site) valid() bool { return s.ID == siteQoder || s.ID == siteQoderCN }

func (s site) chatURL() string   { return s.API + chatPath }
func (s site) modelsURL() string { return s.API + modelsPath }

var (
	siteGlobal = site{
		ID: siteQoder, Name: "Qoder",
		Web: "https://qoder.com", OpenAPI: "https://openapi.qoder.sh", API: "https://api3.qoder.sh",
		ClientID: "732aef47-9cf2-46a2-95fe-4cebb5d0d1fa", RedirectURI: "qoder-app://",
	}
	siteCN = site{
		ID: siteQoderCN, Name: "Qoder CN",
		Web: "https://qoder.cn", OpenAPI: "https://openapi.qoder.com.cn", API: "https://gateway.qoder.com.cn",
		ClientID: "e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb", DeviceChat: true,
	}
)

func siteByID(id string) (site, bool) {
	switch id {
	case siteQoder:
		return siteGlobal, true
	case siteQoderCN:
		return siteCN, true
	default:
		return site{}, false
	}
}
