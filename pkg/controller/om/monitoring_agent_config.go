package om

import (
	"encoding/json"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

type MonitoringAgentConfig struct {
	MonitoringAgentTemplate *MonitoringAgentTemplate
	BackingMap              map[string]interface{}
}

type MonitoringAgentTemplate struct {
	Username      string `json:"username,omitempty"`
	Password      string `json:"password"`
	SSLPemKeyFile string `json:"sslPEMKeyFile,omitempty"`
}

func (m *MonitoringAgentConfig) Apply() error {
	merged, err := util.MergeWith(m.MonitoringAgentTemplate, m.BackingMap, &util.AutomationConfigTransformer{})
	if err != nil {
		return err
	}
	m.BackingMap = merged
	return nil
}

func (m *MonitoringAgentConfig) SetAgentUserName(MonitoringAgentSubject string) {
	m.MonitoringAgentTemplate.Username = MonitoringAgentSubject
}

func (m *MonitoringAgentConfig) UnsetAgentUsername() {
	m.MonitoringAgentTemplate.Username = util.MergoDelete
}

func (m *MonitoringAgentConfig) SetAgentPassword(pwd string) {
	m.MonitoringAgentTemplate.Password = pwd
}

func (m *MonitoringAgentConfig) UnsetAgentPassword() {
	m.MonitoringAgentTemplate.Password = util.MergoDelete
}

func (m *MonitoringAgentConfig) EnableX509Authentication(MonitoringAgentSubject string) {
	m.MonitoringAgentTemplate.SSLPemKeyFile = util.MonitoringAgentPemFilePath
	m.SetAgentUserName(MonitoringAgentSubject)
}

func (m *MonitoringAgentConfig) DisableX509Authentication() {
	m.MonitoringAgentTemplate.SSLPemKeyFile = util.MergoDelete
	m.UnsetAgentUsername()
}

func (m *MonitoringAgentConfig) EnableLdapAuthentication(monitoringAgentSubject string, monitoringAgentPwd string) {
	m.SetAgentUserName(monitoringAgentSubject)
	m.SetAgentPassword(monitoringAgentPwd)
}

func (m *MonitoringAgentConfig) DisableLdapAuthentication() {
	m.UnsetAgentUsername()
	m.UnsetAgentPassword()
}

// BuildMonitoringAgentConfigFromBytes
func BuildMonitoringAgentConfigFromBytes(jsonBytes []byte) (*MonitoringAgentConfig, error) {
	fullMap := make(map[string]interface{})
	if err := json.Unmarshal(jsonBytes, &fullMap); err != nil {
		return nil, err
	}

	config := &MonitoringAgentConfig{BackingMap: fullMap}
	template := &MonitoringAgentTemplate{}
	if username, ok := fullMap["username"].(string); ok {
		template.Username = username
	}

	if sslPemKeyfile, ok := fullMap["sslPEMKeyFile"].(string); ok {
		template.SSLPemKeyFile = sslPemKeyfile
	}

	config.MonitoringAgentTemplate = template
	return config, nil
}
