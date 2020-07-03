package authentication

import (
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

func TestConfigureScramSha1(t *testing.T) {
	dep := om.NewDeployment()
	conn := om.NewMockedOmConnection(dep)

	opts := Options{
		MinimumMajorVersion: 3,
		AuthoritativeSet:    true,
		ProcessNames:        []string{"process-1", "process-2", "process-3"},
		Mechanisms:          []string{"SCRAM"},
		AgentMechanism:      "SCRAM",
	}

	if err := Configure(conn, opts, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig()

	if err != nil {
		t.Fatal(err)
	}

	assertAuthenticationEnabled(t, ac.Auth)
	assertAuthenticationMechanism(t, ac.Auth, "MONGODB-CR")

}

func TestConfigureScramSha256(t *testing.T) {

	dep := om.NewDeployment()
	conn := om.NewMockedOmConnection(dep)

	opts := Options{
		MinimumMajorVersion: 4,
		AuthoritativeSet:    true,
		ProcessNames:        []string{"process-1", "process-2", "process-3"},
		Mechanisms:          []string{"SCRAM"},
		AgentMechanism:      "SCRAM",
	}

	if err := Configure(conn, opts, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig()

	if err != nil {
		t.Fatal(err)
	}

	assertAuthenticationEnabled(t, ac.Auth)
	assertAuthenticationMechanism(t, ac.Auth, "SCRAM-SHA-256")
}

func TestConfigureX509(t *testing.T) {

	dep := om.NewDeployment()
	conn := om.NewMockedOmConnection(dep)

	opts := Options{
		MinimumMajorVersion: 4,
		AuthoritativeSet:    true,
		ProcessNames:        []string{"process-1", "process-2", "process-3"},
		Mechanisms:          []string{"X509"},
		AgentMechanism:      "X509",
		ClientCertificates:  util.RequireClientCertificates,
		UserOptions: UserOptions{
			AutomationSubject: validSubject("automation"),
			BackupSubject:     validSubject("backup"),
			MonitoringSubject: validSubject("monitoring"),
		},
	}

	if err := Configure(conn, opts, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig()

	if err != nil {
		t.Fatal(err)
	}

	assertAuthenticationEnabled(t, ac.Auth)
	assertAuthenticationMechanism(t, ac.Auth, "MONGODB-X509")
}

func TestConfigureMultipleAuthenticationMechanisms(t *testing.T) {

	dep := om.NewDeployment()
	conn := om.NewMockedOmConnection(dep)

	opts := Options{
		MinimumMajorVersion: 4,
		AuthoritativeSet:    true,
		ProcessNames:        []string{"process-1", "process-2", "process-3"},
		Mechanisms:          []string{"X509", "SCRAM"},
		AgentMechanism:      "SCRAM",
		UserOptions: UserOptions{
			AutomationSubject: validSubject("automation"),
			BackupSubject:     validSubject("backup"),
			MonitoringSubject: validSubject("monitoring"),
		},
	}

	if err := Configure(conn, opts, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig()

	if err != nil {
		t.Fatal(err)
	}

	assertAuthenticationEnabled(t, ac.Auth)

	assert.Contains(t, ac.Auth.AutoAuthMechanisms, "SCRAM-SHA-256")

	assert.Len(t, ac.Auth.DeploymentAuthMechanisms, 2)
	assert.Len(t, ac.Auth.AutoAuthMechanisms, 1)
	assert.Contains(t, ac.Auth.DeploymentAuthMechanisms, "SCRAM-SHA-256")
	assert.Contains(t, ac.Auth.DeploymentAuthMechanisms, "MONGODB-X509")
}

func TestScramSha1MongoDBUpgrade(t *testing.T) {

	dep := om.NewDeployment()
	conn := om.NewMockedOmConnection(dep)

	opts := Options{
		MinimumMajorVersion: 3,
		AuthoritativeSet:    true,
		ProcessNames:        []string{"process-1", "process-2", "process-3"},
		Mechanisms:          []string{"SCRAM"},
		AgentMechanism:      "SCRAM",
	}

	if err := Configure(conn, opts, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig()

	if err != nil {
		t.Fatal(err)
	}

	assertAuthenticationEnabled(t, ac.Auth)
	assertAuthenticationMechanism(t, ac.Auth, "MONGODB-CR")

	opts = Options{
		MinimumMajorVersion: 4,
		AuthoritativeSet:    true,
		ProcessNames:        []string{"process-1", "process-2", "process-3"},
		Mechanisms:          []string{"SCRAM"},
		AgentMechanism:      "SCRAM",
	}

	if err := Configure(conn, opts, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err = conn.ReadAutomationConfig()

	if err != nil {
		t.Fatal(err)
	}

	assertAuthenticationEnabled(t, ac.Auth)
	assertAuthenticationMechanism(t, ac.Auth, "MONGODB-CR")
}

func TestConfigureAndDisable(t *testing.T) {
	dep := om.NewDeployment()
	conn := om.NewMockedOmConnection(dep)

	opts := Options{
		MinimumMajorVersion: 3,
		AuthoritativeSet:    true,
		ProcessNames:        []string{"process-1", "process-2", "process-3"},
		Mechanisms:          []string{"SCRAM"},
		AgentMechanism:      "SCRAM",
		UserOptions: UserOptions{
			AutomationSubject: validSubject("automation"),
			BackupSubject:     validSubject("backup"),
			MonitoringSubject: validSubject("monitoring"),
		},
	}

	if err := Configure(conn, opts, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig()

	if err != nil {
		t.Fatal(err)
	}

	assertAuthenticationEnabled(t, ac.Auth)
	assertAuthenticationMechanism(t, ac.Auth, "MONGODB-CR")

	if err := Disable(conn, opts, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err = conn.ReadAutomationConfig()
	if err != nil {
		t.Fatal(err)
	}

	assertAuthenticationDisabled(t, ac.Auth)

}

func TestDisableAuthentication(t *testing.T) {
	dep := om.NewDeployment()
	conn := om.NewMockedOmConnection(dep)

	// enable authentication
	_ = conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Auth.Enable()
		return nil
	}, zap.S())

	if err := Disable(conn, Options{}, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		t.Fatal(err)
	}

	assertAuthenticationDisabled(t, ac.Auth)
}

func TestGetCorrectAuthMechanismFromVersion(t *testing.T) {

	conn := om.NewMockedOmConnection(om.NewDeployment())
	ac, _ := conn.ReadAutomationConfig()

	mechanismNames := getMechanismNames(ac, 3, []string{"X509"})

	assert.Len(t, mechanismNames, 1)
	assert.Contains(t, mechanismNames, MechanismName("MONGODB-X509"))

	mechanismNames = getMechanismNames(ac, 3, []string{"SCRAM", "X509"})

	assert.Contains(t, mechanismNames, MechanismName("MONGODB-CR"))
	assert.Contains(t, mechanismNames, MechanismName("MONGODB-X509"))

	mechanismNames = getMechanismNames(ac, 4, []string{"SCRAM", "X509"})

	assert.Contains(t, mechanismNames, MechanismName("SCRAM-SHA-256"))
	assert.Contains(t, mechanismNames, MechanismName("MONGODB-X509"))

	// enable MONGODB-CR
	ac.Auth.AutoAuthMechanism = "MONGODB-CR"
	ac.Auth.Enable()

	mechanismNames = getMechanismNames(ac, 4, []string{"SCRAM", "X509"})

	assert.Contains(t, mechanismNames, MechanismName("MONGODB-CR"))
	assert.Contains(t, mechanismNames, MechanismName("MONGODB-X509"))
}

func TestOneAgentOption(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())

	opts := Options{
		MinimumMajorVersion: 3, // SCRAM-SHA-1/MONGODB-CR
		AuthoritativeSet:    true,
		ProcessNames:        []string{"process-1", "process-2", "process-3"},
		Mechanisms:          []string{"SCRAM"},
		OneAgent:            true,
		AgentMechanism:      "SCRAM",
	}

	if err := Configure(conn, opts, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, _ := conn.ReadAutomationConfig()

	assert.Empty(t, ac.Auth.Users)

	opts.OneAgent = false // there should be 3 agents (2 users in the list)

	if err := Configure(conn, opts, zap.S()); err != nil {
		t.Fatal(err)
	}

	ac, _ = conn.ReadAutomationConfig()
	assert.Len(t, ac.Auth.Users, 2)
}

func assertAuthenticationEnabled(t *testing.T, auth *om.Auth) {
	assertAuthenticationEnabledWithUsers(t, auth, 2)
}

func assertAuthenticationEnabledWithUsers(t *testing.T, auth *om.Auth, numUsers int) {
	assert.True(t, auth.AuthoritativeSet)
	assert.False(t, auth.Disabled)
	assert.NotEmpty(t, auth.Key)
	assert.NotEmpty(t, auth.KeyFileWindows)
	assert.NotEmpty(t, auth.KeyFile)
	assert.Len(t, auth.Users, numUsers)
	assert.True(t, noneNil(auth.Users))
}

func assertAuthenticationDisabled(t *testing.T, auth *om.Auth) {
	assert.True(t, auth.Disabled)
	assert.Empty(t, auth.DeploymentAuthMechanisms)
	assert.Empty(t, auth.AutoAuthMechanisms)
	assert.Equal(t, auth.AutoUser, util.AutomationAgentName)
	assert.NotEmpty(t, auth.Key)
	assert.NotEmpty(t, auth.AutoPwd)
	assert.True(t, len(auth.Users) == 0 || allNil(auth.Users))
}

func assertAuthenticationMechanism(t *testing.T, auth *om.Auth, mechanism string) {
	assert.Len(t, auth.DeploymentAuthMechanisms, 1)
	assert.Len(t, auth.AutoAuthMechanisms, 1)
	assert.Len(t, auth.Users, 2)
	assert.Contains(t, auth.DeploymentAuthMechanisms, mechanism)
	assert.Contains(t, auth.AutoAuthMechanisms, mechanism)
}

func assertDeploymentMechanismsConfigured(t *testing.T, authMechanism Mechanism) {
	_ = authMechanism.EnableDeploymentAuthentication(Options{})
	assert.True(t, authMechanism.IsDeploymentAuthenticationConfigured())
}

func assertAgentAuthenticationDisabled(t *testing.T, authMechanism Mechanism, opts Options) {
	_ = authMechanism.EnableAgentAuthentication(opts, zap.S())
	assert.True(t, authMechanism.IsAgentAuthenticationConfigured())

	_ = authMechanism.DisableAgentAuthentication(zap.S())
	assert.False(t, authMechanism.IsAgentAuthenticationConfigured())
}

func noneNil(users []*om.MongoDBUser) bool {
	for i := range users {
		if users[i] == nil {
			return false
		}
	}
	return true
}

func allNil(users []*om.MongoDBUser) bool {
	for i := range users {
		if users[i] != nil {
			return false
		}
	}
	return true
}

func createConnectionAndAutomationConfig() (om.Connection, *om.AutomationConfig) {
	conn := om.NewMockedOmConnection(om.NewDeployment())
	ac, _ := conn.ReadAutomationConfig()
	return conn, ac
}
