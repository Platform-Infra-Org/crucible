package awsops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/aws"
	cip "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	ciptypes "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	rgttypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// ErrUserNotFound is an email that has no account in the user pool. Invite
// branches on it to choose between creating a user and resending an invite.
var ErrUserNotFound = errors.New("user not found in the pool")

func (c *awsClient) s3() *s3.Client {
	return s3.NewFromConfig(c.cfg, func(o *s3.Options) {
		if c.endpoint != "" {
			o.BaseEndpoint = aws.String(c.endpoint)
			o.UsePathStyle = true // one httptest host serves every bucket
		}
	})
}

// HeadObject is whether the object is there. A 404 is reported as false with no
// error; anything else — access denied above all — is an error, because "cannot
// tell" must never read as "no backup exists".
func (c *awsClient) HeadObject(ctx context.Context, bucket, key string) (bool, error) {
	_, err := c.s3().HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err == nil {
		return true, nil
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "NotFound", "NoSuchKey", "NoSuchBucket":
			return false, nil
		}
	}
	var status interface{ HTTPStatusCode() int }
	if errors.As(err, &status) && status.HTTPStatusCode() == http.StatusNotFound {
		return false, nil
	}
	return false, signInError(err)
}

func (c *awsClient) GetObject(ctx context.Context, bucket, key, toFile string) error {
	out, err := c.s3().GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return signInError(err)
	}
	defer out.Body.Close()
	f, err := os.CreateTemp(filepath.Dir(toFile), ".crucible-get-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := io.Copy(f, out.Body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), toFile) // the file appears whole or not at all
}

func (c *awsClient) PutObject(ctx context.Context, bucket, key, fromFile string) error {
	f, err := os.Open(fromFile)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	_, err = c.s3().PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(key),
		Body:          f,
		ContentLength: aws.Int64(st.Size()),
	})
	return signInError(err)
}

func (c *awsClient) cognito() *cip.Client {
	return cip.NewFromConfig(c.cfg, func(o *cip.Options) {
		if c.endpoint != "" {
			o.BaseEndpoint = aws.String(c.endpoint)
		}
	})
}

// CreateUser adds someone to the pool with a verified email and lets Cognito
// mail them a temporary password. email_verified is never optional: without it
// they cannot reset their own password.
func (c *awsClient) CreateUser(ctx context.Context, poolID, email, name string) error {
	attrs := []ciptypes.AttributeType{
		{Name: aws.String("email"), Value: aws.String(email)},
		{Name: aws.String("email_verified"), Value: aws.String("true")},
	}
	if name != "" {
		attrs = append(attrs, ciptypes.AttributeType{Name: aws.String("name"), Value: aws.String(name)})
	}
	_, err := c.cognito().AdminCreateUser(ctx, &cip.AdminCreateUserInput{
		UserPoolId:             aws.String(poolID),
		Username:               aws.String(email),
		UserAttributes:         attrs,
		DesiredDeliveryMediums: []ciptypes.DeliveryMediumType{ciptypes.DeliveryMediumTypeEmail},
	})
	return signInError(err)
}

func (c *awsClient) ResendInvite(ctx context.Context, poolID, email string) error {
	_, err := c.cognito().AdminCreateUser(ctx, &cip.AdminCreateUserInput{
		UserPoolId:             aws.String(poolID),
		Username:               aws.String(email),
		MessageAction:          ciptypes.MessageActionTypeResend,
		DesiredDeliveryMediums: []ciptypes.DeliveryMediumType{ciptypes.DeliveryMediumTypeEmail},
	})
	return signInError(err)
}

func (c *awsClient) VerifyEmail(ctx context.Context, poolID, email string) error {
	_, err := c.cognito().AdminUpdateUserAttributes(ctx, &cip.AdminUpdateUserAttributesInput{
		UserPoolId:     aws.String(poolID),
		Username:       aws.String(email),
		UserAttributes: []ciptypes.AttributeType{{Name: aws.String("email_verified"), Value: aws.String("true")}},
	})
	return signInError(err)
}

func (c *awsClient) UserStatus(ctx context.Context, poolID, email string) (string, error) {
	out, err := c.cognito().AdminGetUser(ctx, &cip.AdminGetUserInput{
		UserPoolId: aws.String(poolID),
		Username:   aws.String(email),
	})
	if err != nil {
		var missing *ciptypes.UserNotFoundException
		if errors.As(err, &missing) {
			return "", fmt.Errorf("%w: %s", ErrUserNotFound, email)
		}
		return "", signInError(err)
	}
	return string(out.UserStatus), nil
}

func (c *awsClient) TaggedResources(ctx context.Context, tag string) ([]string, error) {
	cl := resourcegroupstaggingapi.NewFromConfig(c.cfg, func(o *resourcegroupstaggingapi.Options) {
		if c.endpoint != "" {
			o.BaseEndpoint = aws.String(c.endpoint)
		}
	})
	var arns []string
	p := resourcegroupstaggingapi.NewGetResourcesPaginator(cl, &resourcegroupstaggingapi.GetResourcesInput{
		TagFilters: []rgttypes.TagFilter{{Key: aws.String(tag)}},
	})
	for p.HasMorePages() {
		out, err := p.NextPage(ctx)
		if err != nil {
			return nil, signInError(err)
		}
		for _, m := range out.ResourceTagMappingList {
			arns = append(arns, aws.ToString(m.ResourceARN))
		}
	}
	return arns, nil
}
