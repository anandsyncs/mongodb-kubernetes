package om

import (
	"encoding/json"
	"fmt"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	userv1 "github.com/10gen/ops-manager-kubernetes/api/v1/user"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connectionstring"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/authentication/authtypes"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/constants"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	appDBKeyfilePath = "/var/lib/mongodb-mms-automation/authentication/keyfile"
)

type AppDBSpec struct {
	// +kubebuilder:validation:Pattern=^[0-9]+.[0-9]+.[0-9]+(-.+)?$|^$
	Version string `json:"version"`
	// Amount of members for this MongoDB Replica Set
	// +kubebuilder:validation:Maximum=50
	// +kubebuilder:validation:Minimum=3
	Members                     int                   `json:"members,omitempty"`
	PodSpec                     *mdbv1.MongoDbPodSpec `json:"podSpec,omitempty"`
	FeatureCompatibilityVersion *string               `json:"featureCompatibilityVersion,omitempty"`

	// +optional
	Security      *mdbv1.Security `json:"security,omitempty"`
	ClusterDomain string          `json:"clusterDomain,omitempty"`
	// +kubebuilder:validation:Enum=Standalone;ReplicaSet;ShardedCluster
	ResourceType mdbv1.ResourceType `json:"type,omitempty"`

	Connectivity *mdbv1.MongoDBConnectivity `json:"connectivity,omitempty"`
	// AdditionalMongodConfig is additional configuration that can be passed to
	// each data-bearing mongod at runtime. Uses the same structure as the mongod
	// configuration file:
	// https://docs.mongodb.com/manual/reference/configuration-options/
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	AdditionalMongodConfig *mdbv1.AdditionalMongodConfig `json:"additionalMongodConfig,omitempty"`

	// specify startup flags for the AutomationAgent and MonitoringAgent
	AutomationAgent mdbv1.AgentConfig `json:"agent,omitempty"`

	// specify startup flags for just the MonitoringAgent. These take precedence over
	// the flags set in AutomationAgent
	MonitoringAgent mdbv1.AgentConfig `json:"monitoringAgent,omitempty"`
	ConnectionSpec  `json:",inline"`

	// PasswordSecretKeyRef contains a reference to the secret which contains the password
	// for the mongodb-ops-manager SCRAM-SHA user
	PasswordSecretKeyRef *userv1.SecretKeyRef `json:"passwordSecretKeyRef,omitempty"`

	// Enables Prometheus integration on the AppDB.
	Prometheus *mdbcv1.Prometheus `json:"prometheus,omitempty"`

	// transient fields. These fields are cleaned before serialization, see 'MarshalJSON()'
	// note, that we cannot include the 'OpsManager' instance here as this creates circular dependency and problems with
	// 'DeepCopy'

	OpsManagerName string `json:"-"`
	Namespace      string `json:"-"`
	// this is an optional service, it will get the name "<rsName>-service" in case not provided
	Service string `json:"service,omitempty"`

	// AutomationConfigOverride holds any fields that will be merged on top of the Automation Config
	// that the operator creates for the AppDB. Currently only the process.disabled field is recognized.
	AutomationConfigOverride *mdbcv1.AutomationConfigOverride `json:"automationConfig,omitempty"`

	UpdateStrategyType appsv1.StatefulSetUpdateStrategyType `json:"-"`

	// MemberConfig
	// +optional
	MemberConfig []automationconfig.MemberOptions `json:"memberConfig,omitempty"`
}

func (m *AppDBSpec) GetAgentLogLevel() mdbcv1.LogLevel {
	return mdbcv1.LogLevel(m.AutomationAgent.LogLevel)
}

func (m *AppDBSpec) GetAgentMaxLogFileDurationHours() int {
	return m.AutomationAgent.MaxLogFileDurationHours
}

// ObjectKey returns the client.ObjectKey with m.OpsManagerName because the name is used to identify the object to enqueue and reconcile.
func (m *AppDBSpec) ObjectKey() client.ObjectKey {
	return kube.ObjectKey(m.Namespace, m.OpsManagerName)
}

// GetConnectionSpec returns nil because no connection spec for appDB is implemented for the watcher setup
func (m *AppDBSpec) GetConnectionSpec() *mdbv1.ConnectionSpec {
	return nil
}

func (m *AppDBSpec) GetExternalDomain() *string {
	return nil
}

func (m *AppDBSpec) GetMongodConfiguration() mdbcv1.MongodConfiguration {
	mongodConfig := mdbcv1.NewMongodConfiguration()
	if m.GetAdditionalMongodConfig() == nil || m.AdditionalMongodConfig.ToMap() == nil {
		return mongodConfig
	}
	for k, v := range m.AdditionalMongodConfig.ToMap() {
		mongodConfig.SetOption(k, v)
	}
	return mongodConfig
}

func (m *AppDBSpec) GetHorizonConfig() []mdbv1.MongoDBHorizonConfig {
	return nil // no horizon support for AppDB currently
}

func (m *AppDBSpec) GetAdditionalMongodConfig() *mdbv1.AdditionalMongodConfig {
	if m.AdditionalMongodConfig != nil {
		return m.AdditionalMongodConfig
	}
	return &mdbv1.AdditionalMongodConfig{}
}

func (m *AppDBSpec) GetMemberOptions() []automationconfig.MemberOptions {
	return m.MemberConfig
}

// GetAgentPasswordSecretNamespacedName returns the NamespacedName for the secret
// which contains the Automation Agent's password.
func (m *AppDBSpec) GetAgentPasswordSecretNamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: m.Namespace,
		Name:      m.Name() + "-agent-password",
	}
}

// GetAgentKeyfileSecretNamespacedName returns the NamespacedName for the secret
// which contains the keyfile.
func (m *AppDBSpec) GetAgentKeyfileSecretNamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: m.Namespace,
		Name:      m.Name() + "-keyfile",
	}
}

// GetAuthOptions returns a set of Options which is used to configure Scram Sha authentication
// in the AppDB.
func (m *AppDBSpec) GetAuthOptions() authtypes.Options {
	return authtypes.Options{
		AuthoritativeSet: false,
		KeyFile:          appDBKeyfilePath,
		AuthMechanisms: []string{
			constants.Sha256,
			constants.Sha1,
		},
		AgentName:         util.AutomationAgentName,
		AutoAuthMechanism: constants.Sha1,
	}
}

// GetAuthUsers returns a list of all scram users for this deployment.
// in this case it is just the Ops Manager user for the AppDB.
func (m *AppDBSpec) GetAuthUsers() []authtypes.User {
	passwordSecretName := m.GetOpsManagerUserPasswordSecretName()
	if m.PasswordSecretKeyRef != nil && m.PasswordSecretKeyRef.Name != "" {
		passwordSecretName = m.PasswordSecretKeyRef.Name
	}
	return []authtypes.User{
		{
			Username: util.OpsManagerMongoDBUserName,
			Database: util.DefaultUserDatabase,
			// required roles for the AppDB user are outlined in the documentation
			// https://docs.opsmanager.mongodb.com/current/tutorial/prepare-backing-mongodb-instances/#replica-set-security
			Roles: []authtypes.Role{
				{
					Name:     "readWriteAnyDatabase",
					Database: "admin",
				},
				{
					Name:     "dbAdminAnyDatabase",
					Database: "admin",
				},
				{
					Name:     "clusterMonitor",
					Database: "admin",
				},
				// Enables backup and restoration roles
				// https://docs.mongodb.com/manual/reference/built-in-roles/#backup-and-restoration-roles
				{
					Name:     "backup",
					Database: "admin",
				},
				{
					Name:     "restore",
					Database: "admin",
				},
				// Allows user to do db.fsyncLock required by CLOUDP-78890
				// https://docs.mongodb.com/manual/reference/built-in-roles/#hostManager
				{
					Name:     "hostManager",
					Database: "admin",
				},
			},
			PasswordSecretKey:          m.GetOpsManagerUserPasswordSecretKey(),
			PasswordSecretName:         passwordSecretName,
			ScramCredentialsSecretName: m.OpsManagerUserScramCredentialsName(),
		},
	}
}

func (m *AppDBSpec) NamespacedName() types.NamespacedName {
	return types.NamespacedName{Name: m.Name(), Namespace: m.Namespace}
}

// GetOpsManagerUserPasswordSecretName returns the name of the secret
// that will store the Ops Manager user's password.
func (m *AppDBSpec) GetOpsManagerUserPasswordSecretName() string {
	return m.Name() + "-om-password"
}

// GetOpsManagerUserPasswordSecretKey returns the key that should be used to map to the Ops Manager user's
// password in the secret.
func (m *AppDBSpec) GetOpsManagerUserPasswordSecretKey() string {
	if m.PasswordSecretKeyRef != nil && m.PasswordSecretKeyRef.Key != "" {
		return m.PasswordSecretKeyRef.Key
	}
	return util.DefaultAppDbPasswordKey
}

// OpsManagerUserScramCredentialsName returns the name of the Secret
// which will store the Ops Manager MongoDB user's scram credentials.
func (m *AppDBSpec) OpsManagerUserScramCredentialsName() string {
	return m.Name() + "-om-user-scram-credentials"
}

type ConnectionSpec struct {
	mdbv1.SharedConnectionSpec `json:",inline"`

	// Credentials differ to mdbv1.ConnectionSpec because they are optional here.

	// Name of the Secret holding credentials information
	Credentials string `json:"credentials,omitempty"`
}

type AppDbBuilder struct {
	appDb *AppDBSpec
}

// GetMongoDBVersion returns the version of the MongoDB.
func (m *AppDBSpec) GetMongoDBVersion() string {
	return m.Version
}

func (m *AppDBSpec) GetClusterDomain() string {
	if m.ClusterDomain != "" {
		return m.ClusterDomain
	}
	return "cluster.local"
}

// Replicas returns the number of "user facing" replicas of the MongoDB resource. This method can be used for
// constructing the mongodb URL for example.
// 'Members' would be a more consistent function but go doesn't allow to have the same
// For AppDB there is a validation that number of members is in the range [3, 50]
func (m *AppDBSpec) Replicas() int {
	return m.Members
}

func (m *AppDBSpec) GetSecurityAuthenticationModes() []string {
	return m.GetSecurity().Authentication.GetModes()
}

func (m *AppDBSpec) GetResourceType() mdbv1.ResourceType {
	return m.ResourceType
}

func (m *AppDBSpec) IsSecurityTLSConfigEnabled() bool {
	return m.GetSecurity().IsTLSEnabled()
}

func (m *AppDBSpec) GetFeatureCompatibilityVersion() *string {
	return m.FeatureCompatibilityVersion
}

func (m *AppDBSpec) GetSecurity() *mdbv1.Security {
	if m.Security == nil {
		return &mdbv1.Security{}
	}
	return m.Security
}

func (m *AppDBSpec) GetTLSConfig() *mdbv1.TLSConfig {
	if m.Security == nil || m.Security.TLSConfig == nil {
		return &mdbv1.TLSConfig{}
	}

	return m.Security.TLSConfig
}
func DefaultAppDbBuilder() *AppDbBuilder {
	appDb := &AppDBSpec{
		Version:              "4.2.0",
		Members:              3,
		PodSpec:              &mdbv1.MongoDbPodSpec{},
		PasswordSecretKeyRef: &userv1.SecretKeyRef{},
	}
	return &AppDbBuilder{appDb: appDb}
}

func (b *AppDbBuilder) Build() *AppDBSpec {
	return b.appDb.DeepCopy()
}

func (m *AppDBSpec) GetSecretName() string {
	return m.Name() + "-password"
}

func (m *AppDBSpec) UnmarshalJSON(data []byte) error {
	type MongoDBJSON *AppDBSpec
	if err := json.Unmarshal(data, (MongoDBJSON)(m)); err != nil {
		return err
	}

	// if a reference is specified without a key, we will default to "password"
	if m.PasswordSecretKeyRef != nil && m.PasswordSecretKeyRef.Key == "" {
		m.PasswordSecretKeyRef.Key = util.DefaultAppDbPasswordKey
	}

	m.ConnectionSpec.Credentials = ""
	m.ConnectionSpec.CloudManagerConfig = nil
	m.ConnectionSpec.OpsManagerConfig = nil

	// all resources have a pod spec
	if m.PodSpec == nil {
		m.PodSpec = mdbv1.NewMongoDbPodSpec()
	}
	return nil
}

// Name returns the name of the StatefulSet for the AppDB
func (m *AppDBSpec) Name() string {
	return m.OpsManagerName + "-db"
}

func (m *AppDBSpec) ProjectIDConfigMapName() string {
	return m.Name() + "-project-id"
}

func (m *AppDBSpec) ServiceName() string {
	if m.Service == "" {
		return m.Name() + "-svc"
	}
	return m.Service
}

func (m *AppDBSpec) AutomationConfigSecretName() string {
	return m.Name() + "-config"
}

func (m *AppDBSpec) MonitoringAutomationConfigSecretName() string {
	return m.Name() + "-monitoring-config"
}

// This function is used in community to determine whether we need to create a single
// volume for data+logs or two separate ones
// unless spec.PodSpec.Persistence.MultipleConfig is set, a single volume will be created
func (m *AppDBSpec) HasSeparateDataAndLogsVolumes() bool {
	p := m.PodSpec.Persistence
	return p != nil && (p.MultipleConfig != nil && p.SingleConfig == nil)
}

func (m *AppDBSpec) GetUpdateStrategyType() appsv1.StatefulSetUpdateStrategyType {
	return m.UpdateStrategyType
}

// GetCAConfigMapName returns the name of the ConfigMap which contains
// the CA which will recognize the certificates used to connect to the AppDB
// deployment
func (m *AppDBSpec) GetCAConfigMapName() string {
	security := m.Security
	if security != nil && security.TLSConfig != nil {
		return security.TLSConfig.CA
	}
	return ""
}

// GetTlsCertificatesSecretName returns the name of the secret
// which holds the certificates used to connect to the AppDB
func (m *AppDBSpec) GetTlsCertificatesSecretName() string {
	return m.GetSecurity().MemberCertificateSecretName(m.Name())
}

func (m *AppDBSpec) GetName() string {
	return m.Name()
}
func (m *AppDBSpec) GetNamespace() string {
	return m.Namespace
}

func (m *AppDBSpec) DataVolumeName() string {
	return "data"
}

func (m *AppDBSpec) LogsVolumeName() string {
	return "logs"
}

func (m *AppDBSpec) NeedsAutomationConfigVolume() bool {
	return !vault.IsVaultSecretBackend()
}

func (m *AppDBSpec) AutomationConfigConfigMapName() string {
	return fmt.Sprintf("%s-automation-config-version", m.Name())
}

func (m *AppDBSpec) MonitoringAutomationConfigConfigMapName() string {
	return fmt.Sprintf("%s-monitoring-automation-config-version", m.Name())
}

// GetSecretsMountedIntoPod returns the list of strings mounted into the pod that we need to watch.
func (m *AppDBSpec) GetSecretsMountedIntoPod() []string {
	secrets := []string{}
	if m.PasswordSecretKeyRef != nil {
		secrets = append(secrets, m.PasswordSecretKeyRef.Name)
	}

	if m.Security.IsTLSEnabled() {
		secrets = append(secrets, m.GetTlsCertificatesSecretName())
	}
	return secrets
}

func (m *AppDBSpec) BuildConnectionURL(username, password string, scheme connectionstring.Scheme, connectionParams map[string]string) string {
	builder := connectionstring.Builder().
		SetName(m.Name()).
		SetNamespace(m.Namespace).
		SetUsername(username).
		SetPassword(password).
		SetReplicas(m.Replicas()).
		SetService(m.ServiceName()).
		SetVersion(m.GetMongoDBVersion()).
		SetAuthenticationModes(m.GetSecurityAuthenticationModes()).
		SetClusterDomain(m.GetClusterDomain()).
		SetIsReplicaSet(true).
		SetIsTLSEnabled(m.IsSecurityTLSConfigEnabled()).
		SetConnectionParams(connectionParams).
		SetScheme(scheme)

	return builder.Build()
}

func GetAppDBCaPemPath() string {
	return util.AppDBMmsCaFileDirInContainer + "ca-pem"
}
