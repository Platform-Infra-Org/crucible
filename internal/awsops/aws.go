package awsops

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/aws-sdk-go-v2/service/sso"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

// ErrNotSignedIn is every "your AWS session has gone" error, so that no command
// ever shows an operator a raw SDK credential failure.
var ErrNotSignedIn = errors.New("not signed in to AWS: run `crucible aws login`")

// AWS is everything awsops needs from AWS. It replaces the `aws` CLI, so an
// operator installs no AWS CLI; later method groups are added by the tasks that
// need them. Tests use a fake, or the real client against httptest through
// AWSConfig.Endpoint.
type AWS interface {
	// Region is the region every call is made in.
	Region() string
	// In is the same credentials against another region. Callers hold the
	// install's region in the stack outputs, which need not be the configured
	// default, and labs have regions of their own.
	In(region string) AWS
	// Account is the signed-in account id, and the check that we are signed in.
	Account(ctx context.Context) (string, error)

	// Systems Manager: the node's settings, and every command run on it.
	GetParameter(ctx context.Context, name string, decrypt bool) (string, error)
	PutParameter(ctx context.Context, name, value string, secure bool) error
	DeleteParameter(ctx context.Context, name string) error
	SendCommand(ctx context.Context, instanceID, document string, params map[string][]string) (string, error)
	CommandResult(ctx context.Context, commandID, instanceID string) (status, stdout, stderr string, err error)
	InstanceOnline(ctx context.Context, instanceID string) (bool, error)
}

// AWSConfig says who to sign in as. An empty SSOStartURL means "use the shared
// configuration", so ~/.aws/config keeps working with no code of our own.
type AWSConfig struct {
	Region      string
	Profile     string // empty = the shared configuration's default
	SSOStartURL string // empty = resolve credentials from the default chain
	SSORegion   string
	Account     string // the account to assume into; empty = discovered at login
	Role        string // the permission set to assume; empty = discovered at login
	Endpoint    string // tests only: every service at this URL
}

type awsClient struct {
	cfg      aws.Config
	endpoint string
}

var _ AWS = (*awsClient)(nil)

// NewAWS resolves credentials once. With an SSO session configured it reads the
// shared SSO token cache — the same cache the AWS CLI writes, so `aws sso login`
// and `crucible aws login` are interchangeable. Without one it uses the default
// chain, which reads ~/.aws/config and honours AWS_PROFILE and the other AWS_*
// variables.
func NewAWS(ctx context.Context, c AWSConfig) (AWS, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if c.Region != "" {
		opts = append(opts, awsconfig.WithRegion(c.Region))
	}
	if c.Profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(c.Profile))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, signInError(err)
	}
	cl := &awsClient{cfg: cfg, endpoint: c.Endpoint}
	if c.SSOStartURL != "" {
		cl.cfg.Credentials = aws.NewCredentialsCache(ssoProvider(cl, c))
	}
	return cl, nil
}

// ssoProvider reads the cached SSO token and exchanges it for role credentials.
// A cache miss surfaces as ErrNotSignedIn rather than as a file error.
func ssoProvider(cl *awsClient, c AWSConfig) aws.CredentialsProvider {
	region := c.SSORegion
	if region == "" {
		region = c.Region
	}
	oidcCfg := cl.cfg.Copy()
	oidcCfg.Region = region
	ssoCfg := oidcCfg

	cache, err := ssocreds.StandardCachedTokenFilepath(c.SSOStartURL)
	tokenOpts := func(o *ssocreds.SSOTokenProviderOptions) {}
	if err == nil {
		tokenOpts = func(o *ssocreds.SSOTokenProviderOptions) { o.CachedTokenFilepath = cache }
	}
	oidcClient := ssooidc.NewFromConfig(oidcCfg, func(o *ssooidc.Options) {
		if cl.endpoint != "" {
			o.BaseEndpoint = aws.String(cl.endpoint)
		}
	})
	ssoClient := sso.NewFromConfig(ssoCfg, func(o *sso.Options) {
		if cl.endpoint != "" {
			o.BaseEndpoint = aws.String(cl.endpoint)
		}
	})
	return ssocreds.New(ssoClient, c.Account, c.Role, c.SSOStartURL, func(o *ssocreds.Options) {
		o.SSOTokenProvider = ssocreds.NewSSOTokenProvider(oidcClient, cache, tokenOpts)
	})
}

func (c *awsClient) Region() string { return c.cfg.Region }

func (c *awsClient) In(region string) AWS {
	if region == "" || region == c.cfg.Region {
		return c
	}
	cfg := c.cfg.Copy()
	cfg.Region = region
	return &awsClient{cfg: cfg, endpoint: c.endpoint}
}

func (c *awsClient) Account(ctx context.Context) (string, error) {
	out, err := c.sts().GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", signInError(err)
	}
	return aws.ToString(out.Account), nil
}

func (c *awsClient) sts() *sts.Client {
	return sts.NewFromConfig(c.cfg, func(o *sts.Options) {
		if c.endpoint != "" {
			o.BaseEndpoint = aws.String(c.endpoint)
		}
	})
}

// signInError wraps the SDK's many ways of saying "no usable credentials" in
// ErrNotSignedIn, and leaves every other error alone.
func signInError(err error) error {
	if err == nil || errors.Is(err, ErrNotSignedIn) {
		return err
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "ExpiredToken", "ExpiredTokenException", "InvalidGrantException",
			"UnauthorizedException", "AccessDeniedException", "InvalidClientException":
			return fmt.Errorf("%w: %w", ErrNotSignedIn, err)
		}
	}
	// ssocreds reports a cache miss and an expired cached token as plain errors.
	for _, s := range []string{
		"cached SSO token",
		"failed to read cached SSO token",
		"SSO session has expired",
		"failed to refresh cached credentials",
		"no EC2 IMDS role found",
		"failed to retrieve credentials",
	} {
		if strings.Contains(err.Error(), s) {
			return fmt.Errorf("%w: %w", ErrNotSignedIn, err)
		}
	}
	return err
}
