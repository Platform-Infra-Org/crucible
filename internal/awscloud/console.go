package awscloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// SigninEndpoint is AWS's federation endpoint: it turns temporary credentials into an AWS console sign-in link.
const SigninEndpoint = "https://signin.aws.amazon.com/federation"

// ConsoleURL makes a console sign-in URL from c (an assumed-role session): the console session has exactly c's
// permissions and lasts until c expires. The URL works once, within 15 minutes; never log or store it.
func ConsoleURL(ctx context.Context, hc *http.Client, endpoint string, c Credentials, issuer, destination string) (string, error) {
	sess, _ := json.Marshal(map[string]string{"sessionId": c.AccessKeyID, "sessionKey": c.SecretAccessKey, "sessionToken": c.SessionToken})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+url.Values{"Action": {"getSigninToken"}, "Session": {string(sess)}}.Encode(), nil)
	if err != nil {
		return "", errors.New("the AWS sign-in request could not be built")
	}
	res, err := hc.Do(req)
	if err != nil {
		return "", errors.New("the AWS sign-in endpoint is unreachable") // the request URL carries the secret: never wrap err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the AWS sign-in endpoint refused the lab session (HTTP %d)", res.StatusCode)
	}
	var body struct{ SigninToken string }
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&body); err != nil || body.SigninToken == "" {
		return "", errors.New("the AWS sign-in endpoint sent no sign-in token")
	}
	return endpoint + "?" + url.Values{"Action": {"login"}, "Issuer": {issuer}, "Destination": {destination}, "SigninToken": {body.SigninToken}}.Encode(), nil
}
