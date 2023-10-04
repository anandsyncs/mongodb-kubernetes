package om

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/cluster"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/annotations"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	userv1 "github.com/10gen/ops-manager-kubernetes/api/v1/user"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	mdbc "github.com/mongodb/mongodb-kubernetes-operator/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func init() {
	v1.SchemeBuilder.Register(&MongoDBOpsManager{}, &MongoDBOpsManagerList{})
}

const (
	queryableBackupConfigPath  string = "brs.queryable.proxyPort"
	queryableBackupDefaultPort int32  = 25999
)

// The MongoDBOpsManager resource allows you to deploy Ops Manager within your Kubernetes cluster
// +k8s:deepcopy-gen=true
// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=opsmanagers,scope=Namespaced,shortName=om,singular=opsmanager
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".spec.replicas",description="The number of replicas of MongoDBOpsManager."
// +kubebuilder:printcolumn:name="Version",type="string",JSONPath=".spec.version",description="The version of MongoDBOpsManager."
// +kubebuilder:printcolumn:name="State (OpsManager)",type="string",JSONPath=".status.opsManager.phase",description="The current state of the MongoDBOpsManager."
// +kubebuilder:printcolumn:name="State (AppDB)",type="string",JSONPath=".status.applicationDatabase.phase",description="The current state of the MongoDBOpsManager Application Database."
// +kubebuilder:printcolumn:name="State (Backup)",type="string",JSONPath=".status.backup.phase",description="The current state of the MongoDBOpsManager Backup Daemon."
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="The time since the MongoDBOpsManager resource was created."
// +kubebuilder:printcolumn:name="Warnings",type="string",JSONPath=".status.warnings",description="Warnings."
type MongoDBOpsManager struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MongoDBOpsManagerSpec `json:"spec"`
	// +optional
	Status MongoDBOpsManagerStatus `json:"status"`
}

func (om *MongoDBOpsManager) AddValidationToManager(mgr manager.Manager, _ map[string]cluster.Cluster) error {
	return ctrl.NewWebhookManagedBy(mgr).For(om).Complete()
}

func (om *MongoDBOpsManager) GetAppDBProjectConfig(client secrets.SecretClient) (mdbv1.ProjectConfig, error) {
	var operatorVaultSecretPath string
	if client.VaultClient != nil {
		operatorVaultSecretPath = client.VaultClient.OperatorSecretPath()
	}
	secretName, err := om.APIKeySecretName(client, operatorVaultSecretPath)
	if err != nil {
		return mdbv1.ProjectConfig{}, err
	}

	return mdbv1.ProjectConfig{
		BaseURL:     om.CentralURL(),
		ProjectName: om.Spec.AppDB.Name(),
		Credentials: secretName,
	}, nil
}

// +k8s:deepcopy-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MongoDBOpsManagerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MongoDBOpsManager `json:"items"`
}

type MongoDBOpsManagerSpec struct {
	// The configuration properties passed to Ops Manager/Backup Daemon
	// +optional
	Configuration map[string]string `json:"configuration,omitempty"`

	Version string `json:"version"`
	// +optional
	// +kubebuilder:validation:Minimum=1
	Replicas int `json:"replicas"`
	// Deprecated: This has been replaced by the ClusterDomain which should be
	// used instead
	// +kubebuilder:validation:Format="hostname"
	ClusterName string `json:"clusterName,omitempty"`
	// +optional
	// +kubebuilder:validation:Format="hostname"
	ClusterDomain string `json:"clusterDomain,omitempty"`

	// AdminSecret is the secret for the first admin user to create
	// has the fields: "Username", "Password", "FirstName", "LastName"
	AdminSecret string    `json:"adminCredentials,omitempty"`
	AppDB       AppDBSpec `json:"applicationDatabase"`

	// Custom JVM parameters passed to the Ops Manager JVM
	// +optional
	JVMParams []string `json:"jvmParameters,omitempty"`

	// Backup
	// +optional
	Backup *MongoDBOpsManagerBackup `json:"backup,omitempty"`

	// MongoDBOpsManagerExternalConnectivity if sets allows for the creation of a Service for
	// accessing this Ops Manager resource from outside the Kubernetes cluster.
	// +optional
	MongoDBOpsManagerExternalConnectivity *MongoDBOpsManagerServiceDefinition `json:"externalConnectivity,omitempty"`

	// Configure HTTPS.
	// +optional
	Security *MongoDBOpsManagerSecurity `json:"security,omitempty"`

	// Configure custom StatefulSet configuration
	// +optional
	StatefulSetConfiguration *mdbc.StatefulSetConfiguration `json:"statefulSet,omitempty"`
}

type MongoDBOpsManagerSecurity struct {
	// +optional
	TLS MongoDBOpsManagerTLS `json:"tls"`

	// +optional
	CertificatesSecretsPrefix string `json:"certsSecretPrefix"`
}

type MongoDBOpsManagerTLS struct {
	// +optional
	SecretRef TLSSecretRef `json:"secretRef"`
	// +optional
	CA string `json:"ca"`
}

type TLSSecretRef struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

func (ms MongoDBOpsManagerSpec) IsKmipEnabled() bool {
	if ms.Backup == nil || !ms.Backup.Enabled || ms.Backup.Encryption == nil || ms.Backup.Encryption.Kmip == nil {
		return false
	}
	return true
}

func (ms MongoDBOpsManagerSpec) GetClusterDomain() string {
	if ms.ClusterDomain != "" {
		return ms.ClusterDomain
	}
	if ms.ClusterName != "" {
		return ms.ClusterName
	}
	return "cluster.local"
}

func (ms MongoDBOpsManagerSpec) GetOpsManagerCA() string {
	if ms.Security != nil {
		return ms.Security.TLS.CA
	}
	return ""
}

func (ms MongoDBOpsManagerSpec) GetAppDbCA() string {
	if ms.AppDB.Security != nil && ms.AppDB.Security.TLSConfig != nil {
		return ms.AppDB.Security.TLSConfig.CA
	}
	return ""
}

func (om *MongoDBOpsManager) ObjectKey() client.ObjectKey {
	return kube.ObjectKey(om.Namespace, om.Name)
}

func (om *MongoDBOpsManager) AppDBStatefulSetObjectKey(memberClusterNum int) client.ObjectKey {
	return kube.ObjectKey(om.Namespace, om.Spec.AppDB.NameForCluster(memberClusterNum))
}

// MongoDBOpsManagerServiceDefinition struct that defines the mechanism by which this Ops Manager resource
// is exposed, via a Service, to the outside of the Kubernetes Cluster.
type MongoDBOpsManagerServiceDefinition struct {
	// Type of the `Service` to be created.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=LoadBalancer;NodePort
	Type corev1.ServiceType `json:"type"`

	// Port in which this `Service` will listen to, this applies to `NodePort`.
	Port int32 `json:"port,omitempty"`

	// LoadBalancerIP IP that will be assigned to this LoadBalancer.
	LoadBalancerIP string `json:"loadBalancerIP,omitempty"`

	// ExternalTrafficPolicy mechanism to preserve the client source IP.
	// Only supported on GCE and Google Kubernetes Engine.
	// +kubebuilder:validation:Enum=Cluster;Local
	ExternalTrafficPolicy corev1.ServiceExternalTrafficPolicyType `json:"externalTrafficPolicy,omitempty"`

	// Annotations is a list of annotations to be directly passed to the Service object.
	Annotations map[string]string `json:"annotations,omitempty"`
}

// MongoDBOpsManagerBackup backup structure for Ops Manager resources
type MongoDBOpsManagerBackup struct {
	// Enabled indicates if Backups will be enabled for this Ops Manager.
	Enabled                bool  `json:"enabled"`
	ExternalServiceEnabled *bool `json:"externalServiceEnabled,omitempty"`

	// Members indicate the number of backup daemon pods to create.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Members int `json:"members,omitempty"`

	// Assignment Labels set in the Ops Manager
	// +optional
	AssignmentLabels []string `json:"assignmentLabels,omitempty"`

	// HeadDB specifies configuration options for the HeadDB
	HeadDB    *mdbv1.PersistenceConfig `json:"headDB,omitempty"`
	JVMParams []string                 `json:"jvmParameters,omitempty"`

	// S3OplogStoreConfigs describes the list of s3 oplog store configs used for backup.
	S3OplogStoreConfigs []S3Config `json:"s3OpLogStores,omitempty"`

	// OplogStoreConfigs describes the list of oplog store configs used for backup
	OplogStoreConfigs        []DataStoreConfig              `json:"opLogStores,omitempty"`
	BlockStoreConfigs        []DataStoreConfig              `json:"blockStores,omitempty"`
	S3Configs                []S3Config                     `json:"s3Stores,omitempty"`
	FileSystemStoreConfigs   []FileSystemStoreConfig        `json:"fileSystemStores,omitempty"`
	StatefulSetConfiguration *mdbc.StatefulSetConfiguration `json:"statefulSet,omitempty"`

	// QueryableBackupSecretRef references the secret which contains the pem file which is used
	// for queryable backup. This will be mounted into the Ops Manager pod.
	// +optional
	QueryableBackupSecretRef SecretRef `json:"queryableBackupSecretRef,omitempty"`

	// Encryption settings
	// +optional
	Encryption *Encryption `json:"encryption,omitempty"`
}

// Encryption contains encryption settings
type Encryption struct {
	// Kmip corresponds to the KMIP configuration assigned to the Ops Manager Project's configuration.
	// +optional
	Kmip *KmipConfig `json:"kmip,omitempty"`
}

// KmipConfig contains Project-level KMIP configuration
type KmipConfig struct {
	// KMIP Server configuration
	Server v1.KmipServerConfig `json:"server"`
}

type MongoDBOpsManagerStatus struct {
	OpsManagerStatus OpsManagerStatus `json:"opsManager,omitempty"`
	AppDbStatus      AppDbStatus      `json:"applicationDatabase,omitempty"`
	BackupStatus     BackupStatus     `json:"backup,omitempty"`
}

type OpsManagerStatus struct {
	status.Common `json:",inline"`
	Replicas      int              `json:"replicas,omitempty"`
	Version       string           `json:"version,omitempty"`
	Url           string           `json:"url,omitempty"`
	Warnings      []status.Warning `json:"warnings,omitempty"`
}

type OpsManagerAgentVersionMapping []struct {
	OpsManagerVersion string `json:"ops_manager_version"`
	AgentVersion      string `json:"agent_version"`
}

// FindAgentVersionForOpsManager finds an agent version that corresponds to a version
// of Ops Manager passed as parameter.
func (m OpsManagerAgentVersionMapping) FindAgentVersionForOpsManager(omVersion string) string {
	for _, v := range m {
		if v.OpsManagerVersion == omVersion {
			return v.AgentVersion
		}
	}

	return ""
}

type AppDbStatus struct {
	mdbv1.MongoDbStatus `json:",inline"`
}

type BackupStatus struct {
	status.Common `json:",inline"`
	Version       string           `json:"version,omitempty"`
	Warnings      []status.Warning `json:"warnings,omitempty"`
}

type FileSystemStoreConfig struct {
	Name string `json:"name"`
}

// DataStoreConfig is the description of the config used to reference to database. Reused by Oplog and Block stores
// Optionally references the user if the Mongodb is configured with authentication
type DataStoreConfig struct {
	Name               string                    `json:"name"`
	MongoDBResourceRef userv1.MongoDBResourceRef `json:"mongodbResourceRef"`
	MongoDBUserRef     *MongoDBUserRef           `json:"mongodbUserRef,omitempty"`
	// Assignment Labels set in the Ops Manager
	// +optional
	AssignmentLabels []string `json:"assignmentLabels,omitempty"`
}

func (f DataStoreConfig) Identifier() interface{} {
	return f.Name
}

type SecretRef struct {
	Name string `json:"name"`
}

type S3Config struct {
	MongoDBResourceRef     *userv1.MongoDBResourceRef `json:"mongodbResourceRef,omitempty"`
	MongoDBUserRef         *MongoDBUserRef            `json:"mongodbUserRef,omitempty"`
	S3SecretRef            SecretRef                  `json:"s3SecretRef"`
	Name                   string                     `json:"name"`
	PathStyleAccessEnabled bool                       `json:"pathStyleAccessEnabled"`
	S3BucketEndpoint       string                     `json:"s3BucketEndpoint"`
	S3BucketName           string                     `json:"s3BucketName"`
	// +optional
	S3RegionOverride string `json:"s3RegionOverride"`
	// Set this to "true" to use the appDBCa as a CA to access S3.
	// Deprecated: This has been replaced by CustomCertificateSecretRefs,
	// In the future all custom certificates, which includes the appDBCa
	// for s3Config should be configured in CustomCertificateSecretRefs instead.
	// +optional
	CustomCertificate bool `json:"customCertificate"`
	// This is only set to "true" when a user is running in EKS and is using AWS IRSA to configure
	// S3 snapshot store. For more details refer this: https://aws.amazon.com/blogs/opensource/introducing-fine-grained-iam-roles-service-accounts/
	// +optional
	IRSAEnabled bool `json:"irsaEnabled"`
	// Assignment Labels set in the Ops Manager
	// +optional
	AssignmentLabels []string `json:"assignmentLabels"`
	// CustomCertificateSecretRefs is a list of valid Certificate Authority certificate secrets
	// that apply to the associated S3 bucket.
	// +optional
	CustomCertificateSecretRefs []corev1.SecretKeySelector `json:"customCertificateSecretRefs"`
}

func (s S3Config) Identifier() interface{} {
	return s.Name
}

// MongodbResourceObjectKey returns the "name-namespace" object key. Uses the AppDB name if the mongodb resource is not
// specified
func (s S3Config) MongodbResourceObjectKey(opsManager *MongoDBOpsManager) client.ObjectKey {
	ns := opsManager.Namespace
	if s.MongoDBResourceRef == nil {
		return client.ObjectKey{}
	}
	if s.MongoDBResourceRef.Namespace != "" {
		ns = s.MongoDBResourceRef.Namespace
	}
	return client.ObjectKey{Name: s.MongoDBResourceRef.Name, Namespace: ns}
}

func (s S3Config) MongodbUserObjectKey(defaultNamespace string) client.ObjectKey {
	ns := defaultNamespace
	if s.MongoDBResourceRef == nil {
		return client.ObjectKey{}
	}
	if s.MongoDBResourceRef.Namespace != "" {
		ns = s.MongoDBResourceRef.Namespace
	}
	return client.ObjectKey{Name: s.MongoDBUserRef.Name, Namespace: ns}
}

// MongodbResourceObjectKey returns the object key for the mongodb resource referenced by the dataStoreConfig.
// It uses the "parent" object namespace if it is not overriden by 'MongoDBResourceRef.namespace'
func (f *DataStoreConfig) MongodbResourceObjectKey(defaultNamespace string) client.ObjectKey {
	ns := defaultNamespace
	if f.MongoDBResourceRef.Namespace != "" {
		ns = f.MongoDBResourceRef.Namespace
	}
	return client.ObjectKey{Name: f.MongoDBResourceRef.Name, Namespace: ns}
}

func (f *DataStoreConfig) MongodbUserObjectKey(defaultNamespace string) client.ObjectKey {
	ns := defaultNamespace
	if f.MongoDBResourceRef.Namespace != "" {
		ns = f.MongoDBResourceRef.Namespace
	}
	return client.ObjectKey{Name: f.MongoDBUserRef.Name, Namespace: ns}
}

type MongoDBUserRef struct {
	Name string `json:"name"`
}

func (om *MongoDBOpsManager) UnmarshalJSON(data []byte) error {
	type MongoDBJSON *MongoDBOpsManager
	if err := json.Unmarshal(data, (MongoDBJSON)(om)); err != nil {
		return err
	}
	om.InitDefaultFields()

	return nil
}

func (om *MongoDBOpsManager) InitDefaultFields() {
	// providing backward compatibility for the deployments which didn't specify the 'replicas' before Operator 1.3.1
	// This doesn't update the object in Api server so the real spec won't change
	// All newly created resources will pass through the normal validation so 'replicas' will never be 0
	if om.Spec.Replicas == 0 {
		om.Spec.Replicas = 1
	}

	if om.Spec.Backup == nil {
		om.Spec.Backup = newBackup()
	}

	if om.Spec.Backup.Members == 0 {
		om.Spec.Backup.Members = 1
	}

	om.Spec.AppDB.Security = ensureSecurityWithSCRAM(om.Spec.AppDB.Security)

	// setting ops manager name, namespace and ClusterDomain for the appdb (transient fields)
	om.Spec.AppDB.OpsManagerName = om.Name
	om.Spec.AppDB.Namespace = om.Namespace
	om.Spec.AppDB.ClusterDomain = om.Spec.GetClusterDomain()
	om.Spec.AppDB.ResourceType = mdbv1.ReplicaSet
}

func ensureSecurityWithSCRAM(specSecurity *mdbv1.Security) *mdbv1.Security {
	if specSecurity == nil {
		specSecurity = &mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{}}
	}
	// the only allowed authentication is SCRAM - it's implicit to the user and hidden from him
	specSecurity.Authentication = &mdbv1.Authentication{Modes: []mdbv1.AuthMode{util.SCRAM}}
	return specSecurity
}

func (om *MongoDBOpsManager) SvcName() string {
	return om.Name + "-svc"
}

func (om *MongoDBOpsManager) AppDBMongoConnectionStringSecretName() string {
	return om.Spec.AppDB.Name() + "-connection-string"
}

func (om *MongoDBOpsManager) BackupServiceName() string {
	return om.BackupStatefulSetName() + "-svc"
}

func (ms MongoDBOpsManagerSpec) BackupSvcPort() (int32, error) {
	if port, ok := ms.Configuration[queryableBackupConfigPath]; ok {
		val, err := strconv.Atoi(port)
		if err != nil {
			return -1, err
		}
		return int32(val), nil
	}
	return queryableBackupDefaultPort, nil
}

func (om *MongoDBOpsManager) AddConfigIfDoesntExist(key, value string) bool {
	if om.Spec.Configuration == nil {
		om.Spec.Configuration = make(map[string]string)
	}
	if _, ok := om.Spec.Configuration[key]; !ok {
		om.Spec.Configuration[key] = value
		return true
	}
	return false
}

func (om *MongoDBOpsManager) UpdateStatus(phase status.Phase, statusOptions ...status.Option) {
	var statusPart status.Part
	if option, exists := status.GetOption(statusOptions, status.OMPartOption{}); exists {
		statusPart = option.(status.OMPartOption).StatusPart
	}

	switch statusPart {
	case status.AppDb:
		om.updateStatusAppDb(phase, statusOptions...)
	case status.OpsManager:
		om.updateStatusOpsManager(phase, statusOptions...)
	case status.Backup:
		om.updateStatusBackup(phase, statusOptions...)
	}
}

func (om *MongoDBOpsManager) updateStatusAppDb(phase status.Phase, statusOptions ...status.Option) {
	om.Status.AppDbStatus.UpdateCommonFields(phase, om.GetGeneration(), statusOptions...)

	if option, exists := status.GetOption(statusOptions, status.ReplicaSetMembersOption{}); exists {
		om.Status.AppDbStatus.Members = option.(status.ReplicaSetMembersOption).Members
	}

	if option, exists := status.GetOption(statusOptions, status.WarningsOption{}); exists {
		om.Status.AppDbStatus.Warnings = append(om.Status.AppDbStatus.Warnings, option.(status.WarningsOption).Warnings...)
	}

	if phase == status.PhaseRunning {
		spec := om.Spec.AppDB
		om.Status.AppDbStatus.Version = spec.GetMongoDBVersion()
		om.Status.AppDbStatus.Message = ""
	}
}

func (om *MongoDBOpsManager) updateStatusOpsManager(phase status.Phase, statusOptions ...status.Option) {
	om.Status.OpsManagerStatus.UpdateCommonFields(phase, om.GetGeneration(), statusOptions...)

	if option, exists := status.GetOption(statusOptions, status.BaseUrlOption{}); exists {
		om.Status.OpsManagerStatus.Url = option.(status.BaseUrlOption).BaseUrl
	}

	if option, exists := status.GetOption(statusOptions, status.WarningsOption{}); exists {
		om.Status.OpsManagerStatus.Warnings = append(om.Status.OpsManagerStatus.Warnings, option.(status.WarningsOption).Warnings...)
	}

	if phase == status.PhaseRunning {
		om.Status.OpsManagerStatus.Replicas = om.Spec.Replicas
		om.Status.OpsManagerStatus.Version = om.Spec.Version
		om.Status.OpsManagerStatus.Message = ""
	}
}

func (om *MongoDBOpsManager) updateStatusBackup(phase status.Phase, statusOptions ...status.Option) {
	om.Status.BackupStatus.UpdateCommonFields(phase, om.GetGeneration(), statusOptions...)

	if option, exists := status.GetOption(statusOptions, status.WarningsOption{}); exists {
		om.Status.BackupStatus.Warnings = append(om.Status.BackupStatus.Warnings, option.(status.WarningsOption).Warnings...)
	}
	if phase == status.PhaseRunning {
		om.Status.BackupStatus.Message = ""
		om.Status.BackupStatus.Version = om.Spec.Version
	}
}

func (om *MongoDBOpsManager) SetWarnings(warnings []status.Warning, options ...status.Option) {
	for _, part := range getPartsFromStatusOptions(options...) {
		switch part {
		case status.OpsManager:
			om.Status.OpsManagerStatus.Warnings = warnings
		case status.Backup:
			om.Status.BackupStatus.Warnings = warnings
		case status.AppDb:
			om.Status.AppDbStatus.Warnings = warnings
		}
	}
}

func (om *MongoDBOpsManager) AddOpsManagerWarningIfNotExists(warning status.Warning) {
	om.Status.OpsManagerStatus.Warnings = status.Warnings(om.Status.OpsManagerStatus.Warnings).AddIfNotExists(warning)
}
func (om *MongoDBOpsManager) AddAppDBWarningIfNotExists(warning status.Warning) {
	om.Status.AppDbStatus.Warnings = status.Warnings(om.Status.AppDbStatus.Warnings).AddIfNotExists(warning)
}
func (om *MongoDBOpsManager) AddBackupWarningIfNotExists(warning status.Warning) {
	om.Status.BackupStatus.Warnings = status.Warnings(om.Status.BackupStatus.Warnings).AddIfNotExists(warning)
}

func (om *MongoDBOpsManager) GetPlural() string {
	return "opsmanagers"
}

func (om *MongoDBOpsManager) GetStatus(options ...status.Option) interface{} {
	if part, exists := status.GetOption(options, status.OMPartOption{}); exists {
		switch part.Value().(status.Part) {
		case status.OpsManager:
			return om.Status.OpsManagerStatus
		case status.AppDb:
			return om.Status.AppDbStatus
		case status.Backup:
			return om.Status.BackupStatus
		}
	}
	return om.Status
}

func (om *MongoDBOpsManager) GetStatusPath(options ...status.Option) string {
	if part, exists := status.GetOption(options, status.OMPartOption{}); exists {
		switch part.Value().(status.Part) {
		case status.OpsManager:
			return "/status/opsManager"
		case status.AppDb:
			return "/status/applicationDatabase"
		case status.Backup:
			return "/status/backup"
		}
	}
	// we should never actually reach this
	return "/status"
}

// APIKeySecretName returns the secret object name to store the API key to communicate to ops-manager.
// To ensure backward compatibility it checks if a secret key is present with the old format name({$ops-manager-name}-admin-key),
// if not it returns the new name format ({$ops-manager-namespace}-${ops-manager-name}-admin-key), to have multiple om deployments
// with the same name.
func (om *MongoDBOpsManager) APIKeySecretName(client secrets.SecretClientInterface, operatorSecretPath string) (string, error) {
	oldAPISecretName := fmt.Sprintf("%s-admin-key", om.Name)
	operatorNamespace := env.ReadOrPanic(util.CurrentNamespace)
	oldAPIKeySecretNamespacedName := types.NamespacedName{Name: oldAPISecretName, Namespace: operatorNamespace}

	_, err := client.ReadSecret(oldAPIKeySecretNamespacedName, fmt.Sprintf("%s/%s/%s", operatorSecretPath, operatorNamespace, oldAPISecretName))
	if err != nil {
		if secrets.SecretNotExist(err) {
			return fmt.Sprintf("%s-%s-admin-key", om.Namespace, om.Name), nil
		}

		return "", err
	}
	return oldAPISecretName, nil
}

func (om *MongoDBOpsManager) GetSecurity() MongoDBOpsManagerSecurity {
	if om.Spec.Security == nil {
		return MongoDBOpsManagerSecurity{}
	}
	return *om.Spec.Security
}

func (om *MongoDBOpsManager) BackupStatefulSetName() string {
	return fmt.Sprintf("%s-backup-daemon", om.GetName())
}

func (om *MongoDBOpsManager) GetSchemePort() (corev1.URIScheme, int) {
	if om.IsTLSEnabled() {
		return SchemePortFromAnnotation("https")
	}
	return SchemePortFromAnnotation("http")
}

func (om *MongoDBOpsManager) IsTLSEnabled() bool {
	return om.Spec.Security != nil && (om.Spec.Security.TLS.SecretRef.Name != "" || om.Spec.Security.CertificatesSecretsPrefix != "")
}

func (om *MongoDBOpsManager) TLSCertificateSecretName() string {
	// The old field has the precedence
	if om.GetSecurity().TLS.SecretRef.Name != "" {
		return om.GetSecurity().TLS.SecretRef.Name
	}
	if om.GetSecurity().CertificatesSecretsPrefix != "" {
		return fmt.Sprintf("%s-%s-cert", om.GetSecurity().CertificatesSecretsPrefix, om.Name)
	}
	return ""
}

func (om *MongoDBOpsManager) CentralURL() string {
	fqdn := dns.GetServiceFQDN(om.SvcName(), om.Namespace, om.Spec.GetClusterDomain())
	scheme, port := om.GetSchemePort()

	// TODO use url.URL to build the url
	return fmt.Sprintf("%s://%s:%d", strings.ToLower(string(scheme)), fqdn, port)
}

func (om *MongoDBOpsManager) BackupDaemonFQDNs() []string {
	hostnames, _ := dns.GetDNSNames(om.BackupStatefulSetName(), om.BackupServiceName(), om.Namespace, om.Spec.GetClusterDomain(), om.Spec.Backup.Members, nil)
	return hostnames
}

// VersionedImplForMemberCluster is a proxy type for implementing community's annotations.Versioned.
// Originally it was implemented directly in MongoDBOpsManager, but we need to have different implementations
// returning name of stateful set in different member clusters.
// +k8s:deepcopy-gen=false
type VersionedImplForMemberCluster struct {
	client.Object
	memberClusterNum int
	opsManager       *MongoDBOpsManager
}

func (v VersionedImplForMemberCluster) NamespacedName() types.NamespacedName {
	return types.NamespacedName{Name: v.opsManager.Spec.AppDB.NameForCluster(v.memberClusterNum), Namespace: v.opsManager.Namespace}
}

func (v VersionedImplForMemberCluster) GetMongoDBVersionForAnnotation() string {
	return v.opsManager.Spec.AppDB.Version
}

func (v VersionedImplForMemberCluster) IsChangingVersion() bool {
	return v.opsManager.IsChangingVersion()
}

func (om *MongoDBOpsManager) GetVersionedImplForMemberCluster(memberClusterNum int) *VersionedImplForMemberCluster {
	return &VersionedImplForMemberCluster{
		Object:           om,
		memberClusterNum: memberClusterNum,
		opsManager:       om,
	}
}

func (om *MongoDBOpsManager) IsChangingVersion() bool {
	prevVersion := om.getPreviousVersion()
	return prevVersion != "" && prevVersion != om.Spec.AppDB.Version
}

func (om *MongoDBOpsManager) getPreviousVersion() string {
	return annotations.GetAnnotation(om, annotations.LastAppliedMongoDBVersion)
}

// GetAppDBUpdateStrategyType returns the update strategy type the AppDB Statefulset needs to be configured with.
// This depends on whether a version change is in progress.
func (om *MongoDBOpsManager) GetAppDBUpdateStrategyType() appsv1.StatefulSetUpdateStrategyType {
	if !om.IsChangingVersion() {
		return appsv1.RollingUpdateStatefulSetStrategyType
	}
	return appsv1.OnDeleteStatefulSetStrategyType
}

// GetSecretsMountedIntoPod returns the list of strings mounted into the pod that we need to watch.
func (om *MongoDBOpsManager) GetSecretsMountedIntoPod() []string {
	var secretNames []string
	tls := om.TLSCertificateSecretName()
	if tls != "" {
		secretNames = append(secretNames, tls)
	}

	if om.Spec.AdminSecret != "" {
		secretNames = append(secretNames, om.Spec.AdminSecret)
	}

	if om.Spec.Backup != nil {
		for _, config := range om.Spec.Backup.S3Configs {
			if config.S3SecretRef.Name != "" {
				secretNames = append(secretNames, config.S3SecretRef.Name)
			}
		}
	}

	return secretNames
}

// newBackup returns an empty backup object
func newBackup() *MongoDBOpsManagerBackup {
	return &MongoDBOpsManagerBackup{Enabled: true}
}

// ConvertToEnvVarFormat takes a property in the form of
// mms.mail.transport, and converts it into the expected env var format of
// OM_PROP_mms_mail_transport
func ConvertNameToEnvVarFormat(propertyFormat string) string {
	withPrefix := fmt.Sprintf("%s%s", util.OmPropertyPrefix, propertyFormat)
	return strings.Replace(withPrefix, ".", "_", -1)
}

func SchemePortFromAnnotation(annotation string) (corev1.URIScheme, int) {
	scheme := corev1.URISchemeHTTP
	port := util.OpsManagerDefaultPortHTTP
	if strings.ToUpper(annotation) == "HTTPS" {
		scheme = corev1.URISchemeHTTPS
		port = util.OpsManagerDefaultPortHTTPS
	}

	return scheme, port
}

func getPartsFromStatusOptions(options ...status.Option) []status.Part {
	var parts []status.Part
	for _, part := range options {
		if omPart, ok := part.(status.OMPartOption); ok {
			statusPart := omPart.Value().(status.Part)
			parts = append(parts, statusPart)
		}
	}
	return parts
}

// AppDBConfigurable holds information needed to enable SCRAM-SHA
// and combines AppDBSpec (includes SCRAM configuration)
// and MongoDBOpsManager instance (used as the owner reference for the SCRAM related resources)
type AppDBConfigurable struct {
	AppDBSpec
	OpsManager MongoDBOpsManager
}

// GetOwnerReferences returns the OwnerReferences pointing to the MongoDBOpsManager instance and used by SCRAM related resources.
func (m *AppDBConfigurable) GetOwnerReferences() []metav1.OwnerReference {
	groupVersionKind := schema.GroupVersionKind{
		Group:   GroupVersion.Group,
		Version: GroupVersion.Version,
		Kind:    m.OpsManager.Kind,
	}
	ownerReference := *metav1.NewControllerRef(&m.OpsManager, groupVersionKind)
	return []metav1.OwnerReference{ownerReference}
}
