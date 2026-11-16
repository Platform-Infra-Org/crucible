package awsops

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is crucible.yaml: everything an operator decides, and nothing the
// stacks compute. Values the stacks own — the Cognito client, the public URL,
// the lab_* wiring, the k3s and sysbox versions — stay out of it, because an
// install whose settings people can edit by hand is an install that stops
// matching itself.
type Config struct {
	Install struct {
		Name   string `yaml:"name"`
		Region string `yaml:"region"`
	} `yaml:"install"`

	// AWS is optional. Left out, credentials come from the default chain, which
	// reads ~/.aws/config exactly as every other AWS tool does.
	AWS struct {
		SSOStartURL string `yaml:"sso_start_url"`
		SSORegion   string `yaml:"sso_region"`
		SSOSession  string `yaml:"sso_session"`
		Account     string `yaml:"account"`
		Role        string `yaml:"role"`
		Profile     string `yaml:"profile"`
	} `yaml:"aws"`

	Access struct {
		Mode          string `yaml:"mode"`
		SubnetID      string `yaml:"subnet_id"`
		LocalPort     int    `yaml:"local_port"`
		Domain        string `yaml:"domain"`
		ACMEEmail     string `yaml:"acme_email"`
		Route53ZoneID string `yaml:"route53_zone_id"`
	} `yaml:"access"`

	Server struct {
		BootstrapAdmin string `yaml:"bootstrap_admin"`
		GHCRPullToken  string `yaml:"ghcr_pull_token"`
		GitCredentials string `yaml:"git_credentials"`
		GitBotName     string `yaml:"git_bot_name"`
		GitBotEmail    string `yaml:"git_bot_email"`
	} `yaml:"server"`

	Machine struct {
		InstanceType string `yaml:"instance_type"`
		DiskGB       int    `yaml:"disk_gb"`
	} `yaml:"machine"`

	Schedule struct {
		Enabled     *bool  `yaml:"enabled"`
		Timezone    string `yaml:"timezone"`
		LabsBedtime string `yaml:"labs_bedtime"`
		Backup      string `yaml:"backup"`
		Sleep       string `yaml:"sleep"`
		Wake        string `yaml:"wake"`
	} `yaml:"schedule"`

	Email struct {
		SMTPAddr     string `yaml:"smtp_addr"`
		SMTPFrom     string `yaml:"smtp_from"`
		SMTPUsername string `yaml:"smtp_username"`
		SMTPPassword string `yaml:"smtp_password"`
	} `yaml:"email"`

	Labs struct {
		Regions                  []string `yaml:"regions"`
		VPCID                    string   `yaml:"vpc_id"`
		SubnetIDs                []string `yaml:"subnet_ids"`
		BudgetUSD                *float64 `yaml:"budget_usd"`
		BudgetEmails             []string `yaml:"budget_emails"`
		MonthlyAccountLimitUSD   *float64 `yaml:"monthly_account_limit_usd"`
		BillingExport            *bool    `yaml:"billing_export"`
		AccountPublicAccessBlock *bool    `yaml:"account_public_access_block"`
	} `yaml:"labs"`

	// TerraformVars reaches any stack variable the typed schema above does not
	// name, so an operator is never blocked on a schema change and a release.
	TerraformVars map[string]string `yaml:"terraform_vars"`
}

// ConfigPath is the file LoadConfig reads when given no path: $CRUCIBLE_CONFIG,
// else crucible.yaml in the configuration directory.
func ConfigPath() (string, error) {
	if p := os.Getenv("CRUCIBLE_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "crucible.yaml"), nil
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// LoadConfig reads crucible.yaml. A missing file yields a zero Config and no
// error: an operator may pass everything as flags. An unknown key is an error,
// because a typo that silently drops a budget is money.
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		p, err := ConfigPath()
		if err != nil {
			return nil, err
		}
		path = p
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	expanded, err := expandEnv(string(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(expanded))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// expandEnv replaces ${VAR} with the environment's value, so a token need not
// sit in the file. An unset variable is an error: an empty token fails later,
// further from its cause.
func expandEnv(s string) (string, error) {
	var missing []string
	out := envRef.ReplaceAllStringFunc(s, func(m string) string {
		name := envRef.FindStringSubmatch(m)[1]
		v, ok := os.LookupEnv(name)
		if !ok || v == "" {
			missing = append(missing, name)
			return ""
		}
		return v
	})
	if len(missing) > 0 {
		sort.Strings(missing)
		return "", fmt.Errorf("these environment variables are referenced but unset: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

func (c *Config) validate() error {
	switch c.Access.Mode {
	case "", "ssm", "public":
	default:
		return fmt.Errorf("access.mode is %q: it is ssm or public", c.Access.Mode)
	}
	if c.Access.Mode == "ssm" && (c.Access.Domain != "" || c.Access.ACMEEmail != "") {
		return errors.New("access.mode ssm takes no domain or acme_email: an ssm install has no public address")
	}
	if c.Access.Mode == "public" && c.Access.SubnetID != "" {
		return errors.New("access.subnet_id is only for mode ssm: public uses the default VPC")
	}
	if c.Access.Mode == "public" && c.Access.Domain == "" {
		return errors.New("access.mode public needs a domain")
	}
	return nil
}

// AWSConfig is who to sign in as. With no aws block it is empty but for the
// region, so NewAWS falls back to the shared configuration.
func (c *Config) AWSConfig() AWSConfig {
	return AWSConfig{
		Region:      c.Install.Region,
		Profile:     c.AWS.Profile,
		SSOStartURL: c.AWS.SSOStartURL,
		SSORegion:   c.AWS.SSORegion,
		Account:     c.AWS.Account,
		Role:        c.AWS.Role,
	}
}

// LoginOpts is what `crucible aws login` needs from the file.
func (c *Config) LoginOpts() LoginOpts {
	return LoginOpts{
		StartURL:  c.AWS.SSOStartURL,
		SSORegion: cmp2(c.AWS.SSORegion, c.Install.Region),
		Session:   c.AWS.SSOSession,
		Account:   c.AWS.Account,
		Role:      c.AWS.Role,
	}
}

// typedVars are the stack variables the schema owns. terraform_vars may not
// name one: two sources for a single variable is how an install stops matching
// itself.
var typedVars = map[string]bool{
	"region": true, "name": true, "access": true, "subnet_id": true, "local_port": true,
	"domain": true, "acme_email": true, "route53_zone_id": true,
	"bootstrap_admin": true, "ghcr_pull_token": true, "git_credentials": true,
	"git_bot_name": true, "git_bot_email": true,
	"instance_type": true, "disk_gb": true,
	"schedule_enabled": true, "schedule_timezone": true, "labs_sleep_cron": true,
	"backup_cron": true, "sleep_cron": true, "wake_cron": true,
	"smtp_addr": true, "smtp_from": true, "smtp_username": true, "smtp_password": true,
}

// TFVars is the variables an apply passes, built from the file over what is
// already deployed.
//
// **An absent key keeps the deployed value.** Today an omitted flag on a re-run
// of init or labs-init keeps the stack's current value and never resets it; a
// declarative file would otherwise invert that, so that deleting a labs block
// destroyed the AWS Budget and deleting an access block flipped the install
// back to public. Precedence is flag (the caller's own override, applied after
// this) > config file > deployed value > Terraform default.
func (c *Config) TFVars(deployed map[string]string) (map[string]string, error) {
	v := map[string]string{}
	for k, val := range deployed {
		v[k] = val
	}
	set := func(key, val string) {
		if val != "" {
			v[key] = val
		}
	}
	setInt := func(key string, val int) {
		if val != 0 {
			v[key] = strconv.Itoa(val)
		}
	}
	setBool := func(key string, val *bool) {
		if val != nil {
			v[key] = strconv.FormatBool(*val)
		}
	}

	set("region", c.Install.Region)
	set("name", c.Install.Name)

	set("access", c.Access.Mode)
	set("subnet_id", c.Access.SubnetID)
	setInt("local_port", c.Access.LocalPort)
	set("domain", c.Access.Domain)
	set("acme_email", c.Access.ACMEEmail)
	set("route53_zone_id", c.Access.Route53ZoneID)

	set("bootstrap_admin", c.Server.BootstrapAdmin)
	set("ghcr_pull_token", c.Server.GHCRPullToken)
	set("git_credentials", c.Server.GitCredentials)
	set("git_bot_name", c.Server.GitBotName)
	set("git_bot_email", c.Server.GitBotEmail)

	set("instance_type", c.Machine.InstanceType)
	setInt("disk_gb", c.Machine.DiskGB)

	setBool("schedule_enabled", c.Schedule.Enabled)
	set("schedule_timezone", c.Schedule.Timezone)
	set("labs_sleep_cron", c.Schedule.LabsBedtime)
	set("backup_cron", c.Schedule.Backup)
	set("sleep_cron", c.Schedule.Sleep)
	set("wake_cron", c.Schedule.Wake)

	set("smtp_addr", c.Email.SMTPAddr)
	set("smtp_from", c.Email.SMTPFrom)
	set("smtp_username", c.Email.SMTPUsername)
	set("smtp_password", c.Email.SMTPPassword)

	for k, val := range c.TerraformVars {
		if typedVars[k] {
			return nil, fmt.Errorf("terraform_vars.%s names a setting crucible.yaml already owns: set it there instead", k)
		}
		v[k] = val
	}
	return v, nil
}

// LabsVars are the labs stack's own settings, which labs-init applies. They are
// separate because the labs stack is applied by its own command, with the same
// "absent keeps what is deployed" rule.
func (c *Config) LabsVars(deployed map[string]string) map[string]string {
	v := map[string]string{}
	for k, val := range deployed {
		v[k] = val
	}
	if len(c.Labs.Regions) > 0 {
		v["allowed_regions"] = strings.Join(c.Labs.Regions, ",")
	}
	if c.Labs.VPCID != "" {
		v["vpc_id"] = c.Labs.VPCID
	}
	if len(c.Labs.SubnetIDs) > 0 {
		v["subnet_ids"] = strings.Join(c.Labs.SubnetIDs, ",")
	}
	if c.Labs.BudgetUSD != nil {
		v["budget_usd"] = strconv.FormatFloat(*c.Labs.BudgetUSD, 'f', -1, 64)
	}
	if len(c.Labs.BudgetEmails) > 0 {
		v["budget_emails"] = strings.Join(c.Labs.BudgetEmails, ",")
	}
	if c.Labs.MonthlyAccountLimitUSD != nil {
		v["monthly_account_limit_usd"] = strconv.FormatFloat(*c.Labs.MonthlyAccountLimitUSD, 'f', -1, 64)
	}
	if c.Labs.BillingExport != nil {
		v["billing_export"] = strconv.FormatBool(*c.Labs.BillingExport)
	}
	if c.Labs.AccountPublicAccessBlock != nil {
		v["account_public_access_block"] = strconv.FormatBool(*c.Labs.AccountPublicAccessBlock)
	}
	return v
}
