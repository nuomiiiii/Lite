package oauth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/komari-monitor/komari/database/config"
	"github.com/komari-monitor/komari/utils"
)

type GitHubUser struct {
	ID    int    `json:"id"`
	Login string `json:"login"`
	Email string `json:"email"`
}

// 获取OAuth的URL、state ！！请务必验证state防止CSRF攻击
func GetOAuthUrl() (string, string) {
	cfg, _ := config.Get()

	state := utils.GenerateRandomString(16)

	// 构建GitHub OAuth授权URL
	authURL := fmt.Sprintf(
		"https://github.com/login/oauth/authorize?client_id=%s&state=%s&scope=user:email",
		url.QueryEscape(cfg.OAuthClientID),
		url.QueryEscape(state),
	)

	return authURL, state
}

func OAuthCallback(code, state string) (GitHubUser, error) {
	cfg, _ := config.Get()

	// 验证state防止CSRF攻击
	// state, _ := c.Cookie("oauth_state")
	if state == "" {
		return GitHubUser{}, fmt.Errorf("invalid state")
	}

	// 获取code
	//code := c.Query("code")
	if code == "" {
		return GitHubUser{}, fmt.Errorf("no code provided")
	}

	// 获取访问令牌
	tokenURL := "https://github.com/login/oauth/access_token"
	data := url.Values{
		"client_id":     {cfg.OAuthClientID},
		"client_secret": {cfg.OAuthClientSecret},
		"code":          {code},
	}

	req, _ := http.NewRequest("POST", tokenURL, nil)
	req.URL.RawQuery = data.Encode()
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return GitHubUser{}, fmt.Errorf("failed to get access token: %v", err)
	}
	defer resp.Body.Close()

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return GitHubUser{}, fmt.Errorf("failed to parse access token response: %v", err)
	}

	// 获取用户信息
	userReq, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
	userReq.Header.Set("Authorization", "Bearer "+tokenResp.AccessToken)
	userReq.Header.Set("Accept", "application/json")

	userResp, err := http.DefaultClient.Do(userReq)
	if err != nil {
		return GitHubUser{}, fmt.Errorf("failed to get user info: %v", err)
	}
	defer userResp.Body.Close()

	var githubUser GitHubUser
	if err := json.NewDecoder(userResp.Body).Decode(&githubUser); err != nil {
		return GitHubUser{}, fmt.Errorf("failed to parse user info response: %v", err)
	}

	return githubUser, nil
}
