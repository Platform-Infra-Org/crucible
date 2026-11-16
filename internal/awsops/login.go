package awsops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/aws-sdk-go-v2/service/sso"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc"
	ssooidctypes "github.com/aws/aws-sdk-go-v2/service/ssooidc/types"
)

// LoginOpts are `crucible aws login`'s inputs. Account and Role are optional:
// with one account and one role to choose from, login finds them.
type LoginOpts struct {
	StartURL  string
	SSORegion string
	Session   string // the sso-session name, when ~/.aws/config uses one; keys the cache as the CLI keys it
	Account   string
	Role      string
	Open      func(url string) error // nil = the platform's browser opener
}

// Login signs in through Identity Center's device authorization flow and writes
// the access token to the cache the AWS CLI reads, so `aws sso login` and
// `crucible aws login` are interchangeable and nobody is signed in twice.
func (o Ops) Login(ctx context.Context, in LoginOpts) error {
	_, _, err := o.LoginAndResolve(ctx, in)
	return err
}

// LoginAndResolve is Login, and reports which account and role it signed in to.
// With neither configured it lists what the operator may use: exactly one of
// each is used and named, and anything else is an error listing the choices,
// because guessing which account to deploy into is not ours to do.
func (o Ops) LoginAndResolve(ctx context.Context, in LoginOpts) (string, string, error) {
	if in.StartURL == "" {
		return "", "", errors.New("no Identity Center start URL: set aws.sso_start_url in crucible.yaml, or sign in with `aws sso login`")
	}
	region := cmp2(in.SSORegion, o.region())
	if region == "" {
		return "", "", errors.New("no SSO region: set aws.sso_region in crucible.yaml")
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return "", "", err
	}
	oidc := ssooidc.NewFromConfig(cfg, func(opt *ssooidc.Options) {
		if o.Endpoint != "" {
			opt.BaseEndpoint = aws.String(o.Endpoint)
		}
	})

	reg, err := oidc.RegisterClient(ctx, &ssooidc.RegisterClientInput{
		ClientName: aws.String("crucible"),
		ClientType: aws.String("public"),
		Scopes:     []string{"sso:account:access"},
	})
	if err != nil {
		return "", "", fmt.Errorf("registering with Identity Center: %w", err)
	}
	auth, err := oidc.StartDeviceAuthorization(ctx, &ssooidc.StartDeviceAuthorizationInput{
		ClientId:     reg.ClientId,
		ClientSecret: reg.ClientSecret,
		StartUrl:     aws.String(in.StartURL),
	})
	if err != nil {
		return "", "", fmt.Errorf("starting the sign-in: %w", err)
	}

	url := aws.ToString(auth.VerificationUriComplete)
	fmt.Fprintf(o.Log, "opening %s\nif your browser does not open, go to %s and enter %s\n",
		url, aws.ToString(auth.VerificationUri), aws.ToString(auth.UserCode))
	open := in.Open
	if open == nil {
		open = openBrowser
	}
	if err := open(url); err != nil {
		fmt.Fprintf(o.Log, "could not open a browser (%v): open the URL above by hand\n", err)
	}

	tok, err := o.pollForToken(ctx, oidc, reg, auth)
	if err != nil {
		return "", "", err
	}
	if err := writeTokenCache(cmp2(in.Session, in.StartURL), in, region, reg, tok); err != nil {
		return "", "", fmt.Errorf("signed in, but caching the token failed: %w", err)
	}

	account, role, err := o.resolveAccountAndRole(ctx, cfg, aws.ToString(tok.AccessToken), in)
	if err != nil {
		return "", "", err
	}
	fmt.Fprintf(o.Log, "signed in to %s as %s\n", account, role)
	return account, role, nil
}

// pollForToken waits for the operator to approve the sign-in, at the interval
// Identity Center asked for, until the device code expires.
func (o Ops) pollForToken(ctx context.Context, oidc *ssooidc.Client,
	reg *ssooidc.RegisterClientOutput, auth *ssooidc.StartDeviceAuthorizationOutput) (*ssooidc.CreateTokenOutput, error) {
	wait := time.Duration(max32(auth.Interval, 1)) * time.Second
	deadline := o.now().Add(time.Duration(max32(auth.ExpiresIn, 600)) * time.Second)
	sleep := o.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	for o.now().Before(deadline) {
		tok, err := oidc.CreateToken(ctx, &ssooidc.CreateTokenInput{
			ClientId:     reg.ClientId,
			ClientSecret: reg.ClientSecret,
			DeviceCode:   auth.DeviceCode,
			GrantType:    aws.String("urn:ietf:params:oauth:grant-type:device_code"),
		})
		if err == nil {
			return tok, nil
		}
		var pending *ssooidctypes.AuthorizationPendingException
		var slow *ssooidctypes.SlowDownException
		switch {
		case errors.As(err, &pending):
		case errors.As(err, &slow):
			wait += 5 * time.Second
		default:
			return nil, fmt.Errorf("signing in: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sleep(wait)
	}
	return nil, errors.New("the sign-in expired before it was approved: run `crucible aws login` again")
}

// resolveAccountAndRole keeps what the configuration already names, and
// otherwise asks Identity Center what this person may use.
func (o Ops) resolveAccountAndRole(ctx context.Context, cfg aws.Config, token string, in LoginOpts) (string, string, error) {
	if in.Account != "" && in.Role != "" {
		return in.Account, in.Role, nil
	}
	cl := sso.NewFromConfig(cfg, func(opt *sso.Options) {
		if o.Endpoint != "" {
			opt.BaseEndpoint = aws.String(o.Endpoint)
		}
	})
	account := in.Account
	if account == "" {
		out, err := cl.ListAccounts(ctx, &sso.ListAccountsInput{AccessToken: aws.String(token)})
		if err != nil {
			return "", "", fmt.Errorf("listing the accounts you can use: %w", err)
		}
		var ids []string
		for _, a := range out.AccountList {
			ids = append(ids, aws.ToString(a.AccountId)+" ("+aws.ToString(a.AccountName)+")")
		}
		sort.Strings(ids)
		if len(out.AccountList) != 1 {
			return "", "", fmt.Errorf("you can use %d accounts, so set aws.account in crucible.yaml to the one you mean: %v",
				len(ids), ids)
		}
		account = aws.ToString(out.AccountList[0].AccountId)
	}
	role := in.Role
	if role == "" {
		out, err := cl.ListAccountRoles(ctx, &sso.ListAccountRolesInput{
			AccessToken: aws.String(token),
			AccountId:   aws.String(account),
		})
		if err != nil {
			return "", "", fmt.Errorf("listing your roles in %s: %w", account, err)
		}
		var names []string
		for _, r := range out.RoleList {
			names = append(names, aws.ToString(r.RoleName))
		}
		sort.Strings(names)
		if len(names) != 1 {
			return "", "", fmt.Errorf("you have %d roles in %s, so set aws.role in crucible.yaml to the one you mean: %v",
				len(names), account, names)
		}
		role = names[0]
	}
	return account, role, nil
}

// cachedToken is the AWS CLI's own cache shape. Writing it, rather than a store
// of our own, is what keeps one sign-in instead of two.
type cachedToken struct {
	StartURL              string `json:"startUrl"`
	Region                string `json:"region"`
	AccessToken           string `json:"accessToken"`
	ExpiresAt             string `json:"expiresAt"`
	ClientID              string `json:"clientId,omitempty"`
	ClientSecret          string `json:"clientSecret,omitempty"`
	RegistrationExpiresAt string `json:"registrationExpiresAt,omitempty"`
	RefreshToken          string `json:"refreshToken,omitempty"`
}

func writeTokenCache(key string, in LoginOpts, region string,
	reg *ssooidc.RegisterClientOutput, tok *ssooidc.CreateTokenOutput) error {
	path, err := ssocreds.StandardCachedTokenFilepath(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	c := cachedToken{
		StartURL:     in.StartURL,
		Region:       region,
		AccessToken:  aws.ToString(tok.AccessToken),
		ExpiresAt:    time.Now().UTC().Add(time.Duration(tok.ExpiresIn) * time.Second).Format(time.RFC3339),
		ClientID:     aws.ToString(reg.ClientId),
		ClientSecret: aws.ToString(reg.ClientSecret),
		RefreshToken: aws.ToString(tok.RefreshToken),
	}
	if reg.ClientSecretExpiresAt != 0 {
		c.RegistrationExpiresAt = time.Unix(reg.ClientSecretExpiresAt, 0).UTC().Format(time.RFC3339)
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// openBrowser is the platform's "open this URL" command. A failure is reported,
// never fatal: the URL is printed too.
func openBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	return exec.Command(cmd, append(args, url)...).Start()
}

func (o Ops) region() string {
	if o.AWS != nil {
		return o.AWS.Region()
	}
	return ""
}

func (o Ops) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func cmp2(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func max32(a, def int32) int32 {
	if a <= 0 {
		return def
	}
	return a
}
