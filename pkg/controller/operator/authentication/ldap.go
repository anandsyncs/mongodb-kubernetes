package authentication

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
	"go.uber.org/zap"
)

type ldapAuthMechanism struct {
	AutomationConfig *om.AutomationConfig
	Conn             om.Connection
	Options          Options
}

func NewLdap(conn om.Connection, ac *om.AutomationConfig, opts Options) Mechanism {
	return &ldapAuthMechanism{
		AutomationConfig: ac,
		Conn:             conn,
		Options:          opts,
	}
}

func (l *ldapAuthMechanism) EnableAgentAuthentication(opts Options, log *zap.SugaredLogger) error {
	log.Info("Configuring LDAP authentication")
	err := l.Conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		if err := ac.EnsureKeyFileContents(); err != nil {
			return err
		}
		auth := ac.Auth
		auth.AutoPwd = opts.AutoPwd
		auth.Disabled = false
		auth.AuthoritativeSet = opts.AuthoritativeSet
		auth.KeyFile = util.AutomationAgentKeyFilePathInContainer
		auth.KeyFileWindows = util.AutomationAgentWindowsKeyFilePath

		for _, user := range buildScramAgentUsers("") {
			auth.EnsureUserRemoved(user.Username, user.Database)
		}

		for _, user := range buildX509AgentUsers(l.Options.UserOptions) {
			auth.EnsureUserRemoved(user.Username, user.Database)
		}

		auth.AutoUser = l.Options.AutomationSubject
		auth.LdapGroupDN = opts.AutoLdapGroupDN
		auth.AutoAuthMechanisms = []string{string(LDAPPlain)}
		return nil
	}, log)

	if err != nil {
		return err
	}

	log.Info("Configuring backup agent user")
	err = l.Conn.ReadUpdateBackupAgentConfig(func(config *om.BackupAgentConfig) error {
		config.EnableLdapAuthentication(l.Options.BackupSubject, opts.AutoPwd)
		config.SetLdapGroupDN(opts.AutoLdapGroupDN)
		return nil
	}, log)

	if err != nil {
		return err
	}

	log.Info("Configuring monitoring agent user")
	return l.Conn.ReadUpdateMonitoringAgentConfig(func(config *om.MonitoringAgentConfig) error {
		config.EnableLdapAuthentication(l.Options.MonitoringSubject, opts.AutoPwd)
		config.SetLdapGroupDN(opts.AutoLdapGroupDN)
		return nil
	}, log)
}

func (l *ldapAuthMechanism) DisableAgentAuthentication(log *zap.SugaredLogger) error {
	err := l.Conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {

		if stringutil.Contains(ac.Auth.AutoAuthMechanisms, string(LDAPPlain)) {
			ac.Auth.AutoAuthMechanisms = stringutil.Remove(ac.Auth.AutoAuthMechanisms, string(LDAPPlain))
		}
		return nil

	}, log)

	if err != nil {
		return err
	}

	err = l.Conn.ReadUpdateMonitoringAgentConfig(func(config *om.MonitoringAgentConfig) error {
		config.DisableLdapAuthentication()
		return nil
	}, log)

	if err != nil {
		return err
	}

	return l.Conn.ReadUpdateBackupAgentConfig(func(config *om.BackupAgentConfig) error {
		config.DisableLdapAuthentication()
		return nil
	}, log)
}

func (l *ldapAuthMechanism) EnableDeploymentAuthentication(opts Options) error {
	ac := l.AutomationConfig
	ac.Ldap = opts.Ldap
	if !stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, string(LDAPPlain)) {
		ac.Auth.DeploymentAuthMechanisms = append(ac.Auth.DeploymentAuthMechanisms, string(LDAPPlain))
	}

	return nil
}

func (l *ldapAuthMechanism) DisableDeploymentAuthentication() error {
	ac := l.AutomationConfig
	ac.Ldap = nil
	ac.Auth.DeploymentAuthMechanisms = stringutil.Remove(ac.Auth.DeploymentAuthMechanisms, string(LDAPPlain))
	return nil
}

func (l *ldapAuthMechanism) IsAgentAuthenticationConfigured() bool {
	ac := l.AutomationConfig
	if ac.Auth.Disabled {
		return false
	}

	if !stringutil.Contains(ac.Auth.AutoAuthMechanisms, string(LDAPPlain)) {
		return false
	}

	if ac.Auth.AutoUser == "" || ac.Auth.AutoPwd == "" {
		return false
	}

	return true
}

func (l *ldapAuthMechanism) IsDeploymentAuthenticationConfigured() bool {
	ac := l.AutomationConfig
	return stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, string(LDAPPlain)) && ac.Ldap != nil
}
