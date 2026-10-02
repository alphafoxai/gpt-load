package zcode

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

type site string

const (
	siteZAI      site = "zai"
	siteBigModel site = "bigmodel"
)

func (s site) valid() bool { return s == siteZAI || s == siteBigModel }

func (s site) label() string {
	if s == siteBigModel {
		return "BigModel"
	}
	return "Z.ai"
}

func (c *Client) anthropicBase(s site) string {
	if s == siteBigModel {
		return c.Endpoints.BigModelAnthropic
	}
	return c.Endpoints.ZAIAnthropic
}

func (c *Client) bizRoot(s site) string {
	if s == siteBigModel {
		return c.Endpoints.BigModelAPI
	}
	return c.Endpoints.ZAIAPI
}

type startedFlow struct {
	Site      site
	PollAuth  string
	DeviceID  string
	FlowID    string
	URL       string
	ExpiresAt time.Time
	PollEvery time.Duration
}

func (c *Client) beginFlow(ctx context.Context, s site, now time.Time) (startedFlow, error) {
	pollAuth, err := newPollAuth()
	if err != nil {
		return startedFlow{}, err
	}
	device, err := newID()
	if err != nil {
		return startedFlow{}, err
	}
	data, err := c.call(ctx, httpMethodPost, strings.TrimRight(c.Endpoints.ZCode, "/")+"/api/v1/oauth/cli/init", pollAuth, device, map[string]string{"provider": string(s)}, nil)
	if err != nil {
		return startedFlow{}, fmt.Errorf("ZCode sign-in: %w", err)
	}
	parsed, err := url.Parse(data.Get("authorize_url").String())
	if err != nil || data.Get("flow_id").String() == "" || parsed.Scheme != "https" {
		return startedFlow{}, fmt.Errorf("ZCode gave no sign-in page")
	}
	back := strings.TrimRight(c.Endpoints.ZCode, "/") + "/app/oauth/login?" + url.Values{
		"redirect": {"zcode://oauth/callback"}, "app_version": {appVersion},
	}.Encode()
	if s == siteBigModel {
		parsed.RawQuery = setQuery(parsed, "redirect", back)
	} else {
		parsed.RawQuery = setQuery(parsed, "redirect_uri", back)
	}
	every := time.Duration(data.Get("poll_interval_sec").Int()) * time.Second
	if every < time.Second {
		every = time.Second
	}
	if every > time.Minute {
		every = time.Minute
	}
	expires := now.Add(5 * time.Minute)
	if seconds := data.Get("expires_at").Int(); seconds > 0 {
		candidate := time.Unix(seconds, 0).UTC()
		if candidate.After(now) && candidate.Before(expires) {
			expires = candidate
		}
	}
	return startedFlow{
		Site: s, PollAuth: pollAuth, DeviceID: device, FlowID: data.Get("flow_id").String(),
		URL: parsed.String(), ExpiresAt: expires, PollEvery: every,
	}, nil
}

func setQuery(u *url.URL, key, value string) string {
	query := u.Query()
	query.Set(key, value)
	return query.Encode()
}

const httpMethodPost = "POST"

func newID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:]), nil
}

func newPollAuth() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "Bearer " + hex.EncodeToString(buf), nil
}

type pollResult struct {
	Status string
	Token  string
	JWT    string
	User   string
}

func (c *Client) pollFlow(ctx context.Context, flow startedFlow) (pollResult, error) {
	data, err := c.call(ctx, "GET", strings.TrimRight(c.Endpoints.ZCode, "/")+"/api/v1/oauth/cli/poll/"+url.PathEscape(flow.FlowID), flow.PollAuth, flow.DeviceID, nil, nil)
	if err != nil {
		return pollResult{}, err
	}
	token := firstString(data.Get("zai.access_token").String())
	if flow.Site == siteBigModel {
		token = firstString(data.Get("bigmodel.access_token").String(), data.Get("bigmodel.accessToken").String())
	}
	user := data.Get("user")
	return pollResult{
		Status: data.Get("status").String(),
		Token:  token,
		JWT:    firstString(data.Get("token").String()),
		User:   accountName(user),
	}, nil
}

func accountName(user gjson.Result) string {
	email := strings.TrimSpace(user.Get("email").String())
	email = strings.TrimSuffix(email, "@phone.local")
	return firstString(email, user.Get("name").String(), user.Get("user_id").String())
}

func firstString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// Credential is the coding-plan key produced by a finished sign-in.
type Credential struct {
	Site           string `json:"site"`
	BaseURL        string `json:"base_url"`
	APIKey         string `json:"api_key"`
	DeviceID       string `json:"device_id,omitempty"`
	Account        string `json:"account,omitempty"`
	Plan           string `json:"plan,omitempty"`
	OrganizationID string `json:"organization_id,omitempty"`
	ProjectID      string `json:"project_id,omitempty"`
	Token          string `json:"token,omitempty"`
}

func (c *Client) complete(ctx context.Context, flow startedFlow, polled pollResult) (Credential, error) {
	if polled.Status == "failed" {
		return Credential{}, fmt.Errorf("the sign-in was declined on %s", flow.Site.label())
	}
	if polled.Status != "ready" || polled.Token == "" {
		return Credential{}, fmt.Errorf("ZCode sign-in is not ready")
	}
	value, err := c.signedIn(ctx, flow.Site, polled.Token)
	if err != nil {
		return Credential{}, err
	}
	value.Account = firstString(polled.User, value.Account)
	value.DeviceID = flow.DeviceID
	return value, nil
}

func (c *Client) bizAuth(ctx context.Context, s site, token string) (string, error) {
	if s == siteBigModel {
		return token, nil
	}
	data, err := c.call(ctx, httpMethodPost, strings.TrimRight(c.Endpoints.ZAIAPI, "/")+"/api/auth/z/login", "", "", map[string]string{"token": token}, nil)
	if err != nil {
		return "", fmt.Errorf("Z.ai sign-in: %w", err)
	}
	access := data.Get("access_token").String()
	if access == "" {
		return "", fmt.Errorf("Z.ai sign-in: no token")
	}
	return "Bearer " + access, nil
}

func (c *Client) signedIn(ctx context.Context, s site, token string) (Credential, error) {
	auth, err := c.bizAuth(ctx, s, token)
	if err != nil {
		return Credential{}, err
	}
	info, err := c.call(ctx, "GET", strings.TrimRight(c.bizRoot(s), "/")+"/api/biz/customer/getCustomerInfo", auth, "", nil, nil)
	if err != nil {
		return Credential{}, fmt.Errorf("%s account: %w", s.label(), err)
	}
	own, ownErr := c.mintOwn(ctx, s, auth, info)
	if ownErr == nil && own.Plan != "" {
		own.Credential.Account = accountName(info)
		return own.Credential, nil
	}
	team, teamErr := c.teamSignIn(ctx, s, auth, info)
	if teamErr == nil && team.Plan != "" {
		return team, nil
	}
	if ownErr != nil && teamErr != nil {
		return Credential{}, fmt.Errorf("%v; %v", ownErr, teamErr)
	}
	if teamErr != nil {
		return Credential{}, teamErr
	}
	return Credential{}, fmt.Errorf("this %s account has no GLM Coding Plan, of its own or a team's — subscribe, then sign in again", s.label())
}

type minted struct {
	Credential Credential
	Plan       string
}

func (c *Client) mintOwn(ctx context.Context, s site, auth string, info gjson.Result) (minted, error) {
	org, project := defaultProject(info)
	if org == "" || project == "" {
		return minted{}, fmt.Errorf("this %s account has no project for an API key", s.label())
	}
	key, err := c.projectKey(ctx, s, org, project, auth, nil, keyRequest{Name: "zcode-api-key"})
	if err != nil {
		return minted{}, err
	}
	value := Credential{Site: string(s), BaseURL: c.anthropicBase(s), APIKey: key, Token: auth}
	planName, err := c.planName(ctx, value)
	if err != nil {
		return minted{}, fmt.Errorf("GLM Coding Plan: %w", err)
	}
	value.Plan = planName
	return minted{Credential: value, Plan: planName}, nil
}

func (c *Client) teamSignIn(ctx context.Context, s site, auth string, info gjson.Result) (Credential, error) {
	var why string
	var last error
	for _, project := range teamProjects(info) {
		headers := teamHeaders(c.anthropicBase(s), project.org, project.project, s)
		detail, err := c.call(ctx, "GET", strings.TrimRight(c.bizRoot(s), "/")+"/api/biz/team/subscribe/product/querySubscribeDetail", auth, "", nil, headers)
		if err != nil {
			last = err
			continue
		}
		status := strings.ToUpper(detail.Get("status").String())
		grant := strings.ToUpper(detail.Get("memberGrantStatus").String())
		if detail.Get("hasSubscription").Exists() && !detail.Get("hasSubscription").Bool() || status != "EFFECTIVE" || grant != "VALID" {
			if why == "" && status == "EFFECTIVE" && grant == "UNASSIGNED" {
				why = fmt.Sprintf("this %s account is in a team with a GLM Coding Plan but has no seat on it yet", s.label())
			}
			continue
		}
		key, err := c.projectKey(ctx, s, project.org, project.project, auth, headers, keyRequest{Name: "zcode-team-api-key", KeyType: 2, typed: true})
		if err != nil {
			return Credential{}, fmt.Errorf("the team's GLM Coding Plan: %w", err)
		}
		return Credential{
			Site: string(s), BaseURL: c.anthropicBase(s), APIKey: key, Token: auth,
			OrganizationID: project.org, ProjectID: project.project,
			Plan: firstString(detail.Get("productName").String(), "GLM Coding Team"),
		}, nil
	}
	if why != "" {
		return Credential{}, fmt.Errorf("%s", why)
	}
	if last != nil {
		return Credential{}, fmt.Errorf("the team's GLM Coding Plan: %w", last)
	}
	return Credential{}, fmt.Errorf("this %s account has no team GLM Coding Plan", s.label())
}

type projectRef struct{ org, project string }

func defaultProject(info gjson.Result) (string, string) {
	var org, project string
	for _, organization := range info.Get("organizations").Array() {
		projects := make([]gjson.Result, 0)
		for _, project := range organization.Get("projects").Array() {
			if project.Get("projectId").String() != "" && project.Get("projectType").String() != "2" {
				projects = append(projects, project)
			}
		}
		if organization.Get("organizationId").String() == "" || len(projects) == 0 {
			continue
		}
		chosen := projects[0]
		for _, candidate := range projects {
			if strings.Contains(candidate.Get("projectName").String(), "默认项目") {
				chosen = candidate
				break
			}
		}
		named := strings.Contains(organization.Get("organizationName").String(), "默认机构")
		if org == "" || named {
			org = organization.Get("organizationId").String()
			project = chosen.Get("projectId").String()
			if named {
				break
			}
		}
	}
	return org, project
}

func teamProjects(info gjson.Result) []projectRef {
	var refs []projectRef
	for _, organization := range info.Get("organizations").Array() {
		for _, project := range organization.Get("projects").Array() {
			if organization.Get("organizationId").String() != "" && project.Get("projectId").String() != "" && project.Get("projectType").String() == "2" {
				refs = append(refs, projectRef{org: organization.Get("organizationId").String(), project: project.Get("projectId").String()})
			}
		}
	}
	return refs
}

func teamHeaders(base, org, project string, s site) map[string]string {
	language := "en"
	if s == siteBigModel || strings.Contains(base, "bigmodel.cn") {
		language = "zh"
	}
	return map[string]string{
		"Bigmodel-Organization": org,
		"Bigmodel-Project":      project,
		"Set-Language":          language,
		"Accept-Language":       "en-US,en",
	}
}

type keyRequest struct {
	Name    string `json:"name"`
	KeyType int    `json:"keyType,omitempty"`
	typed   bool
}

func (c *Client) projectKey(ctx context.Context, s site, org, project, auth string, headers map[string]string, want keyRequest) (string, error) {
	keys := fmt.Sprintf("%s/api/biz/v1/organization/%s/projects/%s/api_keys", strings.TrimRight(c.bizRoot(s), "/"), url.PathEscape(org), url.PathEscape(project))
	list, err := c.call(ctx, "GET", keys, auth, "", nil, headers)
	if err != nil {
		return "", fmt.Errorf("%s API keys: %w", s.label(), err)
	}
	var id string
	for _, key := range list.Array() {
		if key.Get("name").String() == want.Name && (!want.typed || key.Get("keyType").String() == fmt.Sprint(want.KeyType)) {
			if value := key.Get("apiKey").String(); value != "" {
				id = value
			}
		}
	}
	if id == "" {
		created, err := c.call(ctx, "POST", keys, auth, "", want, headers)
		if err != nil {
			return "", fmt.Errorf("%s API key: %w", s.label(), err)
		}
		id = created.Get("apiKey").String()
	}
	secret := ""
	if id != "" {
		copied, err := c.call(ctx, "GET", keys+"/copy/"+url.PathEscape(id), auth, "", nil, headers)
		if err != nil {
			return "", fmt.Errorf("%s API key: %w", s.label(), err)
		}
		secret = copied.Get("secretKey").String()
	}
	if id != "" && secret == "" && want.typed {
		return id, nil
	}
	if id == "" || secret == "" {
		return "", fmt.Errorf("%s gave no API key", s.label())
	}
	return id + "." + secret, nil
}

func (c *Client) planName(ctx context.Context, value Credential) (string, error) {
	root := c.bizRoot(site(value.Site))
	list, err := c.call(ctx, "GET", strings.TrimRight(root, "/")+"/api/biz/subscription/list", value.APIKey, "", nil, nil)
	if err != nil {
		return "", err
	}
	for _, item := range list.Array() {
		if strings.EqualFold(item.Get("status").String(), "VALID") {
			return item.Get("productName").String(), nil
		}
	}
	return "", nil
}
